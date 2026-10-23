use std::{sync::Arc, time::Duration};

use anyhow::{Result, anyhow, bail};
use serde::Serialize;
use tokio::{
    sync::{Semaphore, mpsc},
    time::{Instant, timeout_at},
};

use crate::{
    agent::{
        events::{BufferMetrics, BufferPermit, EventPacket, OutputState},
        journal::{JournalRecord, TaskStatus},
    },
    protocol::{ByteCounter, InputItem},
    session::{runtime::Association, store::SessionMetadata},
};

use super::SessionEvent;

const MAX_MESSAGES: usize = 32;
const MAX_BYTES: usize = 8 * 1024 * 1024;
const MAX_EVENT_BYTES: usize = 8 * 1024 * 1024;
const ENQUEUE_TIMEOUT: Duration = Duration::from_secs(30);

pub fn channel(state: OutputState) -> (SessionEventSender, SessionEventReceiver) {
    let (sender, receiver) = mpsc::channel(MAX_MESSAGES);
    (
        SessionEventSender {
            sender,
            bytes: Arc::new(Semaphore::new(MAX_BYTES)),
            metrics: BufferMetrics::default(),
            state: state.clone(),
        },
        SessionEventReceiver { receiver, state },
    )
}

pub struct SessionEventPacket {
    pub event: SessionEvent,
    pub association: Option<Association>,
    pub request_id: Option<u64>,
    _display: BufferPermit,
    _original: Option<BufferPermit>,
}

#[derive(Clone)]
pub struct SessionEventSender {
    sender: mpsc::Sender<SessionEventPacket>,
    bytes: Arc<Semaphore>,
    metrics: BufferMetrics,
    state: OutputState,
}

impl SessionEventSender {
    pub async fn send(&self, event: SessionEvent) -> Result<()> {
        self.enqueue(event, None, None, None).await
    }

    pub async fn send_associated(
        &self,
        association: Association,
        event: SessionEvent,
    ) -> Result<()> {
        self.enqueue(event, None, Some(association), None).await
    }

    pub(crate) async fn send_task(
        &self,
        association: Association,
        packet: EventPacket,
    ) -> Result<()> {
        let EventPacket {
            event,
            _bytes: original,
        } = packet;
        let session_id = association.session_id.clone();
        let event = SessionEvent::Agent { session_id, event };
        self.enqueue(event, None, Some(association), Some(original))
            .await
    }

    pub async fn send_reply(
        &self,
        request_id: u64,
        association: Option<Association>,
        event: SessionEvent,
    ) -> Result<()> {
        self.enqueue(event, Some(request_id), association, None)
            .await
    }

    pub fn try_send_reply(&self, request_id: u64, event: SessionEvent) -> Result<()> {
        self.try_enqueue(event, Some(request_id))
    }

    #[cfg(test)]
    pub fn try_send(&self, event: SessionEvent) -> Result<()> {
        self.try_enqueue(event, None)
    }

    fn try_enqueue(&self, event: SessionEvent, request_id: Option<u64>) -> Result<()> {
        if self.state.is_closed() {
            bail!("session event output is closed");
        }
        let bytes = match count_event(&event, request_id, None) {
            Ok(bytes) => bytes,
            Err(error) => {
                self.state.close();
                return Err(error);
            }
        };
        let permit = match self.bytes.clone().try_acquire_many_owned(bytes) {
            Ok(permit) => permit,
            Err(_) => bail!("session event byte budget is full"),
        };
        let display = self.metrics.track(permit);
        let packet = SessionEventPacket {
            event,
            association: None,
            request_id,
            _display: display,
            _original: None,
        };
        match self.sender.try_send(packet) {
            Ok(()) => Ok(()),
            Err(mpsc::error::TrySendError::Full(_)) => bail!("session event queue is full"),
            Err(mpsc::error::TrySendError::Closed(_)) => {
                self.state.close();
                bail!("session event output is closed");
            }
        }
    }

    pub fn state(&self) -> OutputState {
        self.state.clone()
    }

    pub async fn closed(&self) {
        self.state.closed().await;
    }

    #[cfg(test)]
    pub(crate) fn buffer_stats(&self) -> crate::agent::events::BufferStats {
        self.metrics.snapshot()
    }

    #[cfg(test)]
    pub(crate) fn metrics(&self) -> BufferMetrics {
        self.metrics.clone()
    }

    async fn enqueue(
        &self,
        event: SessionEvent,
        request_id: Option<u64>,
        association: Option<Association>,
        original: Option<BufferPermit>,
    ) -> Result<()> {
        if self.state.is_closed() {
            bail!("session event output is closed");
        }
        let deadline = Instant::now() + ENQUEUE_TIMEOUT;
        let bytes = match count_event(&event, request_id, association.as_ref()) {
            Ok(bytes) => bytes,
            Err(error) => {
                self.state.close();
                return Err(error);
            }
        };
        let delivery = async {
            let permit = self
                .bytes
                .clone()
                .acquire_many_owned(bytes)
                .await
                .map_err(|_| anyhow!("session event byte budget is closed"))?;
            let display = self.metrics.track(permit);
            self.sender
                .send(SessionEventPacket {
                    event,
                    association,
                    request_id,
                    _display: display,
                    _original: original,
                })
                .await
                .map_err(|_| anyhow!("session event output is closed"))?;
            Ok::<(), anyhow::Error>(())
        };
        let result = tokio::select! {
            biased;
            _ = self.state.closed() => Err(anyhow!("session event output is closed")),
            result = timeout_at(deadline, self.metrics.wait(delivery)) => match result {
                Ok(result) => result,
                Err(_) => Err(anyhow!("session event enqueue timed out")),
            },
        };
        if result.is_err() {
            self.state.close();
        }
        result
    }
}

pub struct SessionEventReceiver {
    receiver: mpsc::Receiver<SessionEventPacket>,
    state: OutputState,
}

impl SessionEventReceiver {
    pub async fn recv(&mut self) -> Option<super::SessionEventPacket> {
        if self.state.is_closed() {
            self.drain();
            return None;
        }
        tokio::select! {
            biased;
            _ = self.state.closed() => {
                self.drain();
                None
            }
            packet = self.receiver.recv() => packet,
        }
    }

    fn drain(&mut self) {
        self.receiver.close();
        while self.receiver.try_recv().is_ok() {}
    }
}

impl Drop for SessionEventReceiver {
    fn drop(&mut self) {
        self.state.close();
    }
}

fn count_event(
    event: &SessionEvent,
    request_id: Option<u64>,
    association: Option<&Association>,
) -> Result<u32> {
    let mut counter = ByteCounter::new(MAX_EVENT_BYTES);
    match event {
        SessionEvent::Opened {
            metadata,
            replay,
            context,
            state,
            stream,
        } => {
            serde_json::to_writer(
                &mut counter,
                &(
                    "opened",
                    metadata,
                    ReplayBudget {
                        context: &replay.context,
                        history: &replay.history,
                        history_timestamps: &replay.history_timestamps,
                        status: &replay.status,
                    },
                    context,
                    state,
                    stream,
                ),
            )?;
        }
        SessionEvent::HistoryRestored {
            metadata,
            replay,
            context,
            state,
        } => {
            serde_json::to_writer(
                &mut counter,
                &(
                    "history_restored",
                    metadata,
                    ReplayBudget {
                        context: &replay.context,
                        history: &replay.history,
                        history_timestamps: &replay.history_timestamps,
                        status: &replay.status,
                    },
                    context,
                    state,
                ),
            )?;
        }
        SessionEvent::Accepted => {
            serde_json::to_writer(&mut counter, &"accepted")?;
        }
        SessionEvent::Finished(finished) => {
            serde_json::to_writer(&mut counter, &("finished", finished))?;
        }
        SessionEvent::Cancelled { status } => {
            serde_json::to_writer(&mut counter, &("cancelled", status))?;
        }
        SessionEvent::Listed(items) => {
            serde_json::to_writer(&mut counter, &("listed", items.len()))?;
            for item in items {
                let summary = SummaryBudget {
                    metadata: &item.metadata,
                    updated_at: item.updated_at,
                    status: &item.status,
                    in_use: item.in_use,
                    unavailable: &item.unavailable,
                };
                serde_json::to_writer(&mut counter, &summary)?;
            }
        }
        SessionEvent::Renamed(metadata) => {
            serde_json::to_writer(&mut counter, &("renamed", metadata))?;
        }
        SessionEvent::TitleUpdated(metadata) => {
            serde_json::to_writer(&mut counter, &("title_updated", metadata))?;
        }
        SessionEvent::Agent { session_id, event } => {
            serde_json::to_writer(&mut counter, &("agent", session_id, event))?;
        }
        SessionEvent::Error { operation, error } => {
            serde_json::to_writer(&mut counter, &("error", operation, error))?;
        }
    }
    serde_json::to_writer(&mut counter, &request_id)?;
    if let Some(association) = association {
        serde_json::to_writer(&mut counter, association)?;
    }
    u32::try_from(counter.len).map_err(|_| anyhow!("session event exceeds the byte limit"))
}

#[derive(Serialize)]
struct ReplayBudget<'a> {
    context: &'a [InputItem],
    history: &'a [JournalRecord],
    history_timestamps: &'a [u64],
    status: &'a TaskStatus,
}

#[derive(Serialize)]
struct SummaryBudget<'a> {
    metadata: &'a SessionMetadata,
    updated_at: u64,
    status: &'a TaskStatus,
    in_use: bool,
    unavailable: &'a Option<String>,
}

#[cfg(test)]
#[path = "../../tests/session/channel.rs"]
mod tests;
