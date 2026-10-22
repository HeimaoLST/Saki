use std::{io, path::PathBuf, time::Duration};

use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::json;
use tokio::{
    io::{AsyncBufRead, AsyncBufReadExt, AsyncWrite, AsyncWriteExt},
    sync::{Semaphore, mpsc, watch},
    task::JoinSet,
};

use super::config::ConfigPatch;
use super::runtime::{
    Association, Runtime, RuntimeCommand, RuntimeError, RuntimeReply, TaskEvent, TaskFinished,
};
use crate::agent::{
    AgentEvent,
    events::{BufferMetrics, BufferPermit, ByteCounter, OutputState},
    message::{ContextInput, ImageAttachment, UserMessage},
};

const MAX_FRAME_BYTES: usize = 8 * 1024 * 1024;
const OUTPUT_BYTES: usize = 8 * 1024 * 1024;
const OUTPUT_MESSAGES: usize = 32;
const MAX_REQUESTS: usize = 32;

#[derive(Clone, Copy)]
pub struct StdioOptions {
    pub write_timeout: Duration,
    pub max_frame_bytes: usize,
    pub output_bytes: usize,
    pub output_messages: usize,
}

impl Default for StdioOptions {
    fn default() -> Self {
        Self {
            write_timeout: Duration::from_secs(30),
            max_frame_bytes: MAX_FRAME_BYTES,
            output_bytes: OUTPUT_BYTES,
            output_messages: OUTPUT_MESSAGES,
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    version: u32,
    #[serde(rename = "id")]
    _id: String,
    method: String,
    #[serde(default = "empty_object")]
    params: serde_json::Value,
}
fn empty_object() -> serde_json::Value {
    json!({})
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Empty {}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SessionId {
    session_id: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Create {
    working_directory: Option<PathBuf>,
    system_prompt: Option<String>,
    config: Option<ConfigPatch>,
    storage_schema: Option<u32>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Rename {
    session_id: String,
    title: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct History {
    session_id: String,
    #[serde(default)]
    offset: usize,
    limit: Option<usize>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SendTask {
    session_id: String,
    message: Message,
    #[serde(default)]
    context: Vec<ContextInput>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Message {
    text: String,
    #[serde(default)]
    images: Vec<Image>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Image {
    name: String,
    data_url: String,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Models {
    #[serde(default = "default_connection")]
    connection_id: String,
}
fn default_connection() -> String {
    String::new()
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Tools {
    source_id: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct UpdateConfig {
    session_id: String,
    expected_revision: u64,
    patch: ConfigPatch,
    #[serde(default)]
    allow_schema_upgrade: bool,
    #[serde(default)]
    acknowledge_unknown_origin: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Rollback {
    session_id: String,
    expected_revision: u64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Cancel {
    session_id: String,
    task_id: String,
}

fn params<T: DeserializeOwned>(value: serde_json::Value) -> Result<T, RuntimeError> {
    serde_json::from_value(value).map_err(|e| RuntimeError::new("invalid_request", e.to_string()))
}

fn decode(bytes: &[u8]) -> (Option<String>, Result<RuntimeCommand, RuntimeError>) {
    let value = match serde_json::from_slice::<serde_json::Value>(bytes) {
        Ok(value) => value,
        Err(error) => {
            return (
                None,
                Err(RuntimeError::new("invalid_request", error.to_string())),
            );
        }
    };
    let id = value.get("id").and_then(|v| v.as_str()).map(str::to_owned);
    let result = (|| {
        let request: Request = params(value)?;
        if request.version != 1 {
            return Err(RuntimeError::new(
                "unsupported_version",
                "expected protocol version 1",
            ));
        }
        let value = request.params;
        Ok(match request.method.as_str() {
            "runtime.capabilities" => {
                let _: Empty = params(value)?;
                RuntimeCommand::Capabilities
            }
            "model.list" => RuntimeCommand::Models {
                connection_id: params::<Models>(value)?.connection_id,
            },
            "session.config.get" => RuntimeCommand::ConfigGet {
                session_id: params::<SessionId>(value)?.session_id,
            },
            "session.config.update" => {
                let p: UpdateConfig = params(value)?;
                RuntimeCommand::ConfigUpdate {
                    session_id: p.session_id,
                    expected_revision: p.expected_revision,
                    patch: p.patch,
                    allow_schema_upgrade: p.allow_schema_upgrade,
                    acknowledge_unknown_origin: p.acknowledge_unknown_origin,
                }
            }
            "tool.list" => RuntimeCommand::Tools {
                source_id: params::<Tools>(value)?.source_id,
            },
            "connection.list" => {
                let _: Empty = params(value)?;
                RuntimeCommand::Connections
            }
            "session.config.rollback" => {
                let p: Rollback = params(value)?;
                RuntimeCommand::Rollback {
                    session_id: p.session_id,
                    expected_revision: p.expected_revision,
                }
            }
            "session.create" => {
                let p: Create = params(value)?;
                if p.config.is_some() || p.storage_schema.is_some() {
                    let mut config = p.config.unwrap_or_default();
                    if (p.working_directory.is_some() && config.working_directory.is_some())
                        || (p.system_prompt.is_some() && config.system_prompt.is_some())
                    {
                        return Err(RuntimeError::new(
                            "invalid_request",
                            "do not repeat top-level create settings inside config",
                        ));
                    }
                    config.working_directory = config.working_directory.or(p.working_directory);
                    config.system_prompt = config.system_prompt.or(p.system_prompt);
                    RuntimeCommand::CreateConfigured {
                        config,
                        storage_schema: p.storage_schema.unwrap_or(1),
                    }
                } else {
                    RuntimeCommand::Create {
                        working_directory: p.working_directory,
                        system_prompt: p.system_prompt,
                    }
                }
            }
            "session.load" => RuntimeCommand::Load {
                session_id: params::<SessionId>(value)?.session_id,
            },
            "session.list" => {
                let _: Empty = params(value)?;
                RuntimeCommand::List
            }
            "session.rename" => {
                let p: Rename = params(value)?;
                RuntimeCommand::Rename {
                    session_id: p.session_id,
                    title: p.title,
                }
            }
            "session.history" => {
                let p: History = params(value)?;
                RuntimeCommand::History {
                    session_id: p.session_id,
                    offset: p.offset,
                    limit: p.limit.unwrap_or(100),
                }
            }
            "session.status" => RuntimeCommand::Status {
                session_id: params::<SessionId>(value)?.session_id,
            },
            "session.release" => RuntimeCommand::Release {
                session_id: params::<SessionId>(value)?.session_id,
            },
            "task.send" => {
                let p: SendTask = params(value)?;
                RuntimeCommand::Send {
                    session_id: p.session_id,
                    message: UserMessage {
                        text: p.message.text,
                        images: p
                            .message
                            .images
                            .into_iter()
                            .map(|i| ImageAttachment::new(i.name, i.data_url))
                            .collect(),
                        context: p.context,
                    },
                }
            }
            "task.status" => {
                let p: Cancel = params(value)?;
                RuntimeCommand::TaskStatus {
                    session_id: p.session_id,
                    task_id: p.task_id,
                }
            }
            "task.cancel" => {
                let p: Cancel = params(value)?;
                RuntimeCommand::Cancel {
                    session_id: p.session_id,
                    task_id: p.task_id,
                }
            }
            "runtime.shutdown" => {
                let _: Empty = params(value)?;
                RuntimeCommand::Shutdown
            }
            _ => {
                return Err(RuntimeError::new(
                    "unknown_method",
                    format!("unknown method: {}", request.method),
                ));
            }
        })
    })();
    (id, result)
}

struct Frame {
    bytes: Vec<u8>,
    _budget: BufferPermit,
    _event_budget: Option<BufferPermit>,
}
#[derive(Clone)]
struct Output {
    sender: mpsc::Sender<Frame>,
    budget: std::sync::Arc<Semaphore>,
    state: OutputState,
    limit: usize,
    metrics: BufferMetrics,
}
impl Output {
    async fn send<T: Serialize + Sync>(
        &self,
        value: &T,
        event_budget: Option<BufferPermit>,
    ) -> bool {
        if self.state.is_closed() {
            return false;
        }
        let mut count = ByteCounter::new(self.limit.saturating_sub(1));
        if serde_json::to_writer(&mut count, value).is_err() {
            self.state.close();
            return false;
        }
        let permits = count.len + 1;
        let permit = tokio::select! {
            biased;
            _ = self.state.closed() => return false,
            permit = self.metrics.wait(self.budget.clone().acquire_many_owned(permits as u32)) => match permit {
                Ok(permit) => permit,
                Err(_) => { self.state.close(); return false; }
            }
        };
        let mut bytes = Vec::with_capacity(permits);
        if serde_json::to_writer(&mut bytes, value).is_err() {
            self.state.close();
            return false;
        }
        bytes.push(b'\n');
        let frame = Frame {
            bytes,
            _budget: self.metrics.track(permit),
            _event_budget: event_budget,
        };
        tokio::select! {
            biased;
            _ = self.state.closed() => false,
            result = self.metrics.wait(self.sender.send(frame)) => {
                if result.is_err() { self.state.close(); }
                result.is_ok()
            }
        }
    }
}

#[derive(Serialize)]
struct Response<'a, T: Serialize> {
    version: u32,
    #[serde(rename = "type")]
    kind: &'static str,
    id: &'a Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    result: Option<T>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<RuntimeError>,
}
async fn reply<T: Serialize + Sync>(output: &Output, id: &Option<String>, value: T) -> bool {
    output
        .send(
            &Response {
                version: 1,
                kind: "response",
                id,
                result: Some(value),
                error: None,
            },
            None,
        )
        .await
}
async fn reject(output: &Output, id: &Option<String>, error: RuntimeError) {
    output
        .send(
            &Response::<()> {
                version: 1,
                kind: "response",
                id,
                result: None,
                error: Some(error),
            },
            None,
        )
        .await;
}
#[derive(Serialize)]
struct Envelope<'a, T: Serialize> {
    version: u32,
    #[serde(rename = "type")]
    kind: &'static str,
    #[serde(flatten)]
    association: &'a Association,
    event: T,
}
#[derive(Serialize)]
struct Finished {
    #[serde(rename = "type")]
    kind: &'static str,
    #[serde(flatten)]
    result: TaskFinished,
}

async fn deliver(
    output: Output,
    events: Output,
    id: Option<String>,
    result: Result<RuntimeReply, RuntimeError>,
) {
    let response = match result {
        Ok(result) => result,
        Err(error) => {
            reject(&output, &id, error).await;
            return;
        }
    };
    match response {
        RuntimeReply::Tools { builtin, sources } => {
            reply(&output, &id, json!({"builtin":builtin,"sources":sources})).await;
        }
        RuntimeReply::Connections(connections) => {
            reply(&output, &id, json!({"connections":connections})).await;
        }
        RuntimeReply::TaskStatus(value) => {
            reply(&output, &id, value).await;
        }
        RuntimeReply::Capabilities(value) => {
            reply(&output, &id, value).await;
        }
        RuntimeReply::Models {
            connection_id,
            models,
            configured_models,
        } => {
            #[derive(Serialize)]
            struct ModelList<T, C> {
                connection_id: String,
                models: T,
                configured_models: C,
            }
            reply(
                &output,
                &id,
                ModelList {
                    connection_id,
                    models,
                    configured_models,
                },
            )
            .await;
        }
        RuntimeReply::Config(value) => {
            reply(&output, &id, value).await;
        }
        RuntimeReply::ConfigUpdated {
            config,
            effective_from,
        } => {
            #[derive(Serialize)]
            struct Updated<T> {
                #[serde(flatten)]
                config: T,
                effective_from: &'static str,
            }
            reply(
                &output,
                &id,
                Updated {
                    config,
                    effective_from,
                },
            )
            .await;
        }
        RuntimeReply::Session(value) => {
            reply(&output, &id, value).await;
        }
        RuntimeReply::List { sessions } => {
            #[derive(Serialize)]
            struct List<T> {
                sessions: T,
            }
            reply(&output, &id, List { sessions }).await;
        }
        RuntimeReply::Renamed {
            session_id,
            metadata,
        } => {
            #[derive(Serialize)]
            struct Renamed<T> {
                session_id: String,
                metadata: T,
            }
            reply(
                &output,
                &id,
                Renamed {
                    session_id,
                    metadata,
                },
            )
            .await;
        }
        RuntimeReply::History(value) => {
            reply(&output, &id, value).await;
        }
        RuntimeReply::Released { session_id, loaded } => {
            #[derive(Serialize)]
            struct Released {
                session_id: String,
                loaded: bool,
            }
            reply(&output, &id, Released { session_id, loaded }).await;
        }
        RuntimeReply::Cancelled { status } => {
            #[derive(Serialize)]
            struct Cancelled {
                status: &'static str,
            }
            reply(&output, &id, Cancelled { status }).await;
        }
        RuntimeReply::Shutdown { state } => {
            #[derive(Serialize)]
            struct Draining {
                state: &'static str,
            }
            reply(&output, &id, Draining { state }).await;
        }
        RuntimeReply::Accepted(mut accepted) => {
            accepted.events.defer_terminal_ack();
            #[derive(Serialize)]
            struct Accepted<'a> {
                task_id: &'a str,
                message_id: &'a str,
            }
            if !reply(
                &output,
                &id,
                Accepted {
                    task_id: &accepted.association.task_id,
                    message_id: &accepted.association.message_id,
                },
            )
            .await
            {
                return;
            }
            loop {
                let event = tokio::select! {
                    biased;
                    _=events.state.closed()=>return,
                    event=accepted.events.next()=>match event {Some(event)=>event,None=>return},
                };
                let sent = match event {
                    TaskEvent::Event(packet) => {
                        // Keep the upstream byte permit until this frame is written.
                        if matches!(
                            packet.event,
                            AgentEvent::TurnCompleted
                                | AgentEvent::TurnInterrupted
                                | AgentEvent::TurnFailed { .. }
                        ) {
                            continue;
                        }
                        let payload = match packet.event {
                            AgentEvent::TaskStarted { .. } => json!({"type":"task_started"}),
                            other => match serde_json::to_value(other) {
                                Ok(value) => value,
                                Err(_) => {
                                    events.state.close();
                                    return;
                                }
                            },
                        };
                        events
                            .send(
                                &Envelope {
                                    version: 1,
                                    kind: "event",
                                    association: &accepted.association,
                                    event: payload,
                                },
                                Some(packet._bytes),
                            )
                            .await
                    }
                    TaskEvent::Finished(result) => {
                        let sent = events
                            .send(
                                &Envelope {
                                    version: 1,
                                    kind: "event",
                                    association: &accepted.association,
                                    event: Finished {
                                        kind: "task_finished",
                                        result,
                                    },
                                },
                                None,
                            )
                            .await;
                        if sent {
                            accepted.events.acknowledge_finished();
                        }
                        return;
                    }
                };
                if !sent {
                    return;
                }
            }
        }
    }
}

async fn write_output<W: AsyncWrite + Unpin>(
    mut writer: W,
    mut control: mpsc::Receiver<Frame>,
    mut events: mpsc::Receiver<Frame>,
    state: OutputState,
    timeout: Duration,
) -> anyhow::Result<()> {
    let mut control_closed = false;
    let mut events_closed = false;
    while !control_closed || !events_closed {
        let frame = tokio::select! {
            biased;
            _=state.closed()=>anyhow::bail!("runtime output channel closed"),
            frame=control.recv(),if !control_closed=>match frame {Some(frame)=>frame,None=>{control_closed=true;continue;}},
            frame=events.recv(),if !events_closed=>match frame {Some(frame)=>frame,None=>{events_closed=true;continue;}},
        };
        tokio::select! {
            biased;
            _=state.closed()=>anyhow::bail!("runtime output channel closed"),
            result=tokio::time::timeout(timeout,async {writer.write_all(&frame.bytes).await?;writer.flush().await})=>{
                result.map_err(|_|anyhow::anyhow!("runtime output write timed out"))??;
            }
        }
    }
    Ok(())
}

// Consume oversized frames without retaining their bytes or executing a prefix.
async fn read_frame<R: AsyncBufRead + Unpin>(input: &mut R) -> io::Result<Option<Vec<u8>>> {
    let mut bytes = Vec::new();
    let mut oversized = false;
    loop {
        let chunk = input.fill_buf().await?;
        if chunk.is_empty() {
            if oversized {
                return Err(io::Error::other(
                    "runtime request frame exceeds its byte limit",
                ));
            }
            return Ok((!bytes.is_empty()).then_some(bytes));
        }
        let newline = chunk.iter().position(|b| *b == b'\n');
        let len = newline.map_or(chunk.len(), |n| n + 1);
        if bytes.len().saturating_add(len) > MAX_FRAME_BYTES {
            oversized = true;
            bytes.clear();
        }
        if !oversized {
            bytes.extend_from_slice(&chunk[..len]);
        }
        input.consume(len);
        if newline.is_some() {
            if oversized {
                return Ok(Some(Vec::new()));
            }
            return Ok(Some(bytes));
        }
    }
}

async fn drain_output(
    mut work_drained: watch::Receiver<bool>,
    state: OutputState,
    timeout: Duration,
) {
    while !*work_drained.borrow_and_update() {
        if work_drained.changed().await.is_err() {
            return;
        }
    }
    tokio::time::sleep(timeout).await;
    state.close();
}

pub async fn run<R, W>(runtime: Runtime, input: R, writer: W) -> anyhow::Result<()>
where
    R: AsyncBufRead + Unpin,
    W: AsyncWrite + Unpin + Send + 'static,
{
    run_with_options(runtime, input, writer, StdioOptions::default()).await
}

pub async fn run_with_options<R, W>(
    runtime: Runtime,
    mut input: R,
    writer: W,
    options: StdioOptions,
) -> anyhow::Result<()>
where
    R: AsyncBufRead + Unpin,
    W: AsyncWrite + Unpin + Send + 'static,
{
    anyhow::ensure!(
        options.output_messages > 0
            && options.max_frame_bytes > 1
            && options.max_frame_bytes <= options.output_bytes
            && options.output_bytes <= u32::MAX as usize,
        "invalid stdio output limits"
    );
    let max_requests = MAX_REQUESTS + runtime.max_active_tasks();
    let api = runtime.api().as_str();
    let (handle, mut worker) = runtime.start();
    let state = handle.output_state();
    let drain_job = tokio::spawn(drain_output(
        handle.work_drained(),
        state.clone(),
        options.write_timeout,
    ));
    let (control_tx, control_rx) = mpsc::channel(options.output_messages);
    let (event_tx, event_rx) = mpsc::channel(options.output_messages);
    let control = Output {
        sender: control_tx,
        budget: std::sync::Arc::new(Semaphore::new(options.output_bytes)),
        state: state.clone(),
        limit: options.max_frame_bytes,
        metrics: BufferMetrics::default(),
    };
    let events = Output {
        sender: event_tx,
        budget: std::sync::Arc::new(Semaphore::new(options.output_bytes)),
        state: state.clone(),
        limit: options.max_frame_bytes,
        metrics: BufferMetrics::default(),
    };
    let writer_state = state.clone();
    let write_job = tokio::spawn(async move {
        let result = write_output(
            writer,
            control_rx,
            event_rx,
            writer_state.clone(),
            options.write_timeout,
        )
        .await;
        if result.is_err() {
            writer_state.close();
        }
        result
    });
    control
        .send(&json!({"version":1,"type":"ready","api":api}), None)
        .await;
    let mut jobs = JoinSet::new();
    let mut input_error = None;
    let mut service_result = None;
    loop {
        let bytes = tokio::select! {
            biased;
            _=state.closed()=>break,
            result=&mut worker=>{service_result=Some(result);break;},
            result=read_frame(&mut input)=>match result {Ok(Some(bytes))=>bytes,Ok(None)=>break,Err(error)=>{input_error=Some(error);break;}},
        };
        // Reap without cancelling a partial JSONL read: it may already have consumed bytes.
        while let Some(result) = jobs.try_join_next() {
            if result.is_err() {
                state.close();
            }
        }
        if state.is_closed() {
            break;
        }
        let (id, command) = decode(&bytes);
        if jobs.len() >= max_requests {
            if let Ok(command @ (RuntimeCommand::Cancel { .. } | RuntimeCommand::Shutdown)) =
                command
            {
                let _ = handle.submit(command);
            }
            state.close();
            break;
        }
        let response = command.and_then(|command| handle.submit(command));
        let output = control.clone();
        let task_events = events.clone();
        jobs.spawn(async move {
            let result = match response {
                Ok(receiver) => receiver.await.unwrap_or_else(|_| {
                    Err(RuntimeError::new(
                        "io_error",
                        "runtime disconnected before the response",
                    ))
                }),
                Err(error) => Err(error),
            };
            deliver(output, task_events, id, result).await;
        });
    }
    let _ = handle.call(RuntimeCommand::Shutdown).await;
    drop(handle);
    while let Some(result) = jobs.join_next().await {
        if result.is_err() {
            state.close();
        }
    }
    let service_result = match service_result {
        Some(result) => result,
        None => worker.await,
    };
    if service_result.is_err() {
        state.close();
    }
    drop(control);
    drop(events);
    let writer_result = write_job.await;
    drain_job.abort();
    let writer_result = writer_result?;
    writer_result?;
    service_result??;
    if let Some(error) = input_error {
        return Err(error.into());
    }
    Ok(())
}

#[cfg(unix)]
pub enum NativeStdout {
    Pollable(tokio::io::unix::AsyncFd<std::fs::File>),
    File(tokio::fs::File),
}

#[cfg(unix)]
impl NativeStdout {
    pub fn new() -> io::Result<Self> {
        use std::os::fd::FromRawFd;
        // A Tokio blocking stdout write cannot be aborted on a full OS pipe.
        let fd = unsafe { libc::fcntl(libc::STDOUT_FILENO, libc::F_DUPFD_CLOEXEC, 0) };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        let file = unsafe { std::fs::File::from_raw_fd(fd) };
        Self::from_file(file)
    }

    fn from_file(file: std::fs::File) -> io::Result<Self> {
        use std::os::fd::AsRawFd;
        let fd = file.as_raw_fd();
        let flags = unsafe { libc::fcntl(fd, libc::F_GETFL) };
        if flags < 0 || unsafe { libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
            return Err(io::Error::last_os_error());
        }
        // Linux epoll does not accept regular files; redirection to a file still works.
        if file.metadata()?.is_file() {
            return Ok(Self::File(tokio::fs::File::from_std(file)));
        }
        Ok(Self::Pollable(tokio::io::unix::AsyncFd::new(file)?))
    }
}
#[cfg(unix)]
impl AsyncWrite for NativeStdout {
    fn poll_write(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
        bytes: &[u8],
    ) -> std::task::Poll<io::Result<usize>> {
        use std::io::Write;
        let this = self.get_mut();
        let descriptor = match this {
            Self::File(file) => return std::pin::Pin::new(file).poll_write(cx, bytes),
            Self::Pollable(descriptor) => descriptor,
        };
        loop {
            let mut ready = std::task::ready!(descriptor.poll_write_ready(cx))?;
            match ready.try_io(|inner| inner.get_ref().write(bytes)) {
                Ok(result) => return std::task::Poll::Ready(result),
                Err(_) => continue,
            }
        }
    }
    fn poll_flush(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<io::Result<()>> {
        match self.get_mut() {
            Self::File(file) => std::pin::Pin::new(file).poll_flush(cx),
            Self::Pollable(_) => std::task::Poll::Ready(Ok(())),
        }
    }
    fn poll_shutdown(
        self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<io::Result<()>> {
        match self.get_mut() {
            Self::File(file) => std::pin::Pin::new(file).poll_shutdown(cx),
            Self::Pollable(_) => std::task::Poll::Ready(Ok(())),
        }
    }
}

#[cfg(test)]
#[path = "../../tests/session/stdio.rs"]
mod tests;
