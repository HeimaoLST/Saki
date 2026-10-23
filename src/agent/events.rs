use std::sync::{
    Arc,
    atomic::{AtomicBool, AtomicUsize, Ordering},
};

use tokio::sync::{OwnedSemaphorePermit, Semaphore, mpsc, watch};

use super::AgentEvent;
pub(crate) use crate::protocol::ByteCounter;

#[derive(Clone)]
pub struct OutputState(watch::Sender<bool>);

impl Default for OutputState {
    fn default() -> Self {
        Self(watch::channel(false).0)
    }
}

impl OutputState {
    pub fn close(&self) {
        self.0.send_replace(true);
    }

    pub fn is_closed(&self) -> bool {
        *self.0.borrow()
    }

    pub async fn closed(&self) {
        let mut receiver = self.0.subscribe();
        while !*receiver.borrow_and_update() {
            if receiver.changed().await.is_err() {
                return;
            }
        }
    }
}

#[derive(Debug, Clone, Copy)]
pub struct EventLimits {
    pub messages: usize,
    pub bytes: usize,
    pub max_event_bytes: usize,
}

impl Default for EventLimits {
    fn default() -> Self {
        Self {
            messages: 32,
            bytes: 8 * 1024 * 1024,
            max_event_bytes: 4 * 1024 * 1024,
        }
    }
}

#[derive(Clone, Default)]
pub struct BufferMetrics(Arc<BufferCounters>);

#[derive(Default)]
struct BufferCounters {
    bytes: AtomicUsize,
    messages: AtomicUsize,
    peak_bytes: AtomicUsize,
    peak_messages: AtomicUsize,
    waiting_producers: AtomicUsize,
}

#[derive(Debug, Clone, Copy, serde::Serialize)]
pub struct BufferStats {
    pub bytes: usize,
    pub messages: usize,
    pub peak_bytes: usize,
    pub peak_messages: usize,
    pub waiting_producers: usize,
}
impl BufferMetrics {
    pub fn snapshot(&self) -> BufferStats {
        BufferStats {
            bytes: self.0.bytes.load(Ordering::Relaxed),
            messages: self.0.messages.load(Ordering::Relaxed),
            peak_bytes: self.0.peak_bytes.load(Ordering::Relaxed),
            peak_messages: self.0.peak_messages.load(Ordering::Relaxed),
            waiting_producers: self.0.waiting_producers.load(Ordering::Relaxed),
        }
    }
    pub(crate) async fn wait<F: std::future::Future>(&self, future: F) -> F::Output {
        let mut future = std::pin::pin!(future);
        let mut waiter = BufferWaiter {
            metrics: self,
            waiting: false,
        };
        std::future::poll_fn(|context| {
            let result = future.as_mut().poll(context);
            if result.is_pending() && !waiter.waiting {
                self.0.waiting_producers.fetch_add(1, Ordering::Relaxed);
                waiter.waiting = true;
            }
            result
        })
        .await
    }
    pub(crate) fn track(&self, permit: OwnedSemaphorePermit) -> BufferPermit {
        let bytes = self
            .0
            .bytes
            .fetch_add(permit.num_permits(), Ordering::Relaxed)
            + permit.num_permits();
        let messages = self.0.messages.fetch_add(1, Ordering::Relaxed) + 1;
        self.0.peak_bytes.fetch_max(bytes, Ordering::Relaxed);
        self.0.peak_messages.fetch_max(messages, Ordering::Relaxed);
        BufferPermit {
            permit,
            metrics: self.clone(),
        }
    }
}
struct BufferWaiter<'a> {
    metrics: &'a BufferMetrics,
    waiting: bool,
}
impl Drop for BufferWaiter<'_> {
    fn drop(&mut self) {
        if self.waiting {
            self.metrics
                .0
                .waiting_producers
                .fetch_sub(1, Ordering::Relaxed);
        }
    }
}

pub(crate) struct BufferPermit {
    permit: OwnedSemaphorePermit,
    metrics: BufferMetrics,
}
impl Drop for BufferPermit {
    fn drop(&mut self) {
        self.metrics
            .0
            .bytes
            .fetch_sub(self.permit.num_permits(), Ordering::Relaxed);
        self.metrics.0.messages.fetch_sub(1, Ordering::Relaxed);
    }
}

pub struct EventPacket {
    pub event: AgentEvent,
    // The permit follows the event through the adapter, not just its first queue.
    pub(crate) _bytes: BufferPermit,
}

#[derive(Clone)]
pub struct EventSink {
    target: Target,
    incomplete: Arc<AtomicBool>,
    metrics: BufferMetrics,
}

#[derive(Clone)]
enum Target {
    Legacy(mpsc::UnboundedSender<AgentEvent>),
    Bounded {
        sender: mpsc::Sender<EventPacket>,
        bytes: Arc<Semaphore>,
        limit: usize,
        cancel: watch::Receiver<bool>,
        output: OutputState,
    },
}

impl EventSink {
    pub fn legacy(sender: mpsc::UnboundedSender<AgentEvent>) -> Self {
        Self {
            target: Target::Legacy(sender),
            incomplete: Arc::new(AtomicBool::new(false)),
            metrics: BufferMetrics::default(),
        }
    }

    pub fn bounded(
        limits: EventLimits,
        cancel: watch::Receiver<bool>,
        output: OutputState,
    ) -> (Self, mpsc::Receiver<EventPacket>) {
        assert!(limits.messages > 0 && limits.bytes > 0);
        assert!(limits.max_event_bytes > 0 && limits.max_event_bytes <= limits.bytes);
        assert!(limits.bytes <= u32::MAX as usize);
        let (sender, receiver) = mpsc::channel(limits.messages);
        (
            Self {
                target: Target::Bounded {
                    sender,
                    bytes: Arc::new(Semaphore::new(limits.bytes)),
                    limit: limits.max_event_bytes,
                    cancel,
                    output,
                },
                incomplete: Arc::new(AtomicBool::new(false)),
                metrics: BufferMetrics::default(),
            },
            receiver,
        )
    }

    pub(crate) fn metrics(&self) -> BufferMetrics {
        self.metrics.clone()
    }

    pub fn history_required(&self) -> bool {
        self.incomplete.load(Ordering::Relaxed)
    }

    pub async fn send(&self, event: AgentEvent) {
        let Target::Bounded {
            sender,
            bytes,
            limit,
            cancel,
            output,
        } = &self.target
        else {
            if let Target::Legacy(sender) = &self.target {
                let _ = sender.send(event);
            }
            return;
        };
        if output.is_closed() {
            return;
        }
        let mut counter = ByteCounter::new(*limit);
        if serde_json::to_writer(&mut counter, &event).is_err() {
            output.close();
            return;
        }
        let mut observation = EventDelivery {
            incomplete: &self.incomplete,
            output,
            sent: false,
        };
        let delivery = async {
            let permit = bytes.clone().acquire_many_owned(counter.len as u32).await;
            if let Ok(permit) = permit
                && sender
                    .send(EventPacket {
                        event,
                        _bytes: self.metrics.track(permit),
                    })
                    .await
                    .is_err()
            {
                output.close();
            }
        };
        let mut cancellation = Some(cancel.clone());
        tokio::select! {
            biased;
            _ = output.closed() => {}
            _ = super::cancelled(&mut cancellation) => {}
            _ = self.metrics.wait(delivery) => { observation.sent = true; }
        }
    }
}

struct EventDelivery<'a> {
    incomplete: &'a AtomicBool,
    output: &'a OutputState,
    sent: bool,
}
impl Drop for EventDelivery<'_> {
    fn drop(&mut self) {
        // The enclosing model/cancel select may drop this send before it observes cancellation.
        if !self.sent && !self.output.is_closed() {
            self.incomplete.store(true, Ordering::Relaxed);
        }
    }
}

#[cfg(test)]
#[path = "../../tests/agent/events.rs"]
mod tests;
