use std::{collections::HashMap, fs::File, path::PathBuf};

use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use tokio::{
    io::{AsyncBufRead, AsyncBufReadExt, AsyncWrite, AsyncWriteExt},
    sync::{mpsc, watch},
    task::JoinHandle,
};

use super::{
    SYSTEM_PROMPT, build_agent,
    store::{SessionMetadata, SessionStore},
};
use crate::{
    agent::{
        Agent, AgentEvent, ModelRequestPurpose,
        conversation::CompactionConfig,
        is_interrupted,
        journal::{self, Replay},
        message::{ImageAttachment, UserMessage},
    },
    client::{ModelApi, ModelClient},
};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    version: u32,
    id: String,
    method: String,
    #[serde(default = "empty_object")]
    params: Value,
}
fn empty_object() -> Value {
    json!({})
}

#[derive(Debug, Serialize)]
struct Error {
    code: &'static str,
    message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    session_id: Option<String>,
}
impl Error {
    fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            session_id: None,
        }
    }
    fn session(mut self, id: &str) -> Self {
        self.session_id = Some(id.into());
        self
    }
}
type Result<T> = std::result::Result<T, Error>;
fn io_error(error: anyhow::Error) -> Error {
    let code = if error.chain().any(|e| {
        e.downcast_ref::<std::io::Error>()
            .is_some_and(|e| e.kind() == std::io::ErrorKind::NotFound)
    }) {
        "not_found"
    } else {
        "io_error"
    };
    Error::new(code, format!("{error:#}"))
}
fn params<T: DeserializeOwned>(value: Value) -> Result<T> {
    serde_json::from_value(value).map_err(|e| Error::new("invalid_request", e.to_string()))
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
struct Cancel {
    session_id: String,
    task_id: String,
}

struct Session {
    metadata: SessionMetadata,
    agent: Option<Agent>,
}
#[derive(Clone)]
struct Association {
    session_id: String,
    task_id: String,
    message_id: String,
}
struct Active {
    association: Association,
    cancel: watch::Sender<bool>,
    worker: JoinHandle<(Agent, anyhow::Result<()>)>,
}

/// The machine host keeps loaded sessions independent of the single execution slot.
/// It delegates all execution and persistence to the existing Agent and journal.
pub struct Runtime {
    store: SessionStore,
    client: ModelClient,
    cwd: PathBuf,
    model: String,
    config: CompactionConfig,
    sessions: HashMap<String, Session>,
    active: Option<Active>,
}
impl Runtime {
    pub fn new(
        store: SessionStore,
        client: ModelClient,
        cwd: PathBuf,
        model: String,
        config: CompactionConfig,
    ) -> Self {
        Self {
            store,
            client,
            cwd,
            model,
            config,
            sessions: HashMap::new(),
            active: None,
        }
    }
    fn metadata(&self, id: &str) -> Result<SessionMetadata> {
        self.store
            .directory(id)
            .map_err(|e| Error::new("invalid_request", e.to_string()).session(id))?;
        let metadata = self.store.metadata(id).map_err(|e| {
            let is_io = e
                .chain()
                .any(|cause| cause.downcast_ref::<std::io::Error>().is_some());
            if is_io {
                io_error(e).session(id)
            } else {
                Error::new("invalid_history", format!("{e:#}")).session(id)
            }
        })?;
        if metadata.api != self.client.api().as_str() {
            return Err(Error::new(
                "api_mismatch",
                format!(
                    "session uses {}; runtime uses {}",
                    metadata.api,
                    self.client.api().as_str()
                ),
            )
            .session(id));
        }
        Ok(metadata)
    }
    fn replay(&self, id: &str) -> Result<Replay> {
        self.metadata(id)?;
        let path = self
            .store
            .directory(id)
            .map_err(io_error)?
            .join("history.jsonl");
        let file = File::open(&path).map_err(|e| io_error(e.into()).session(id))?;
        journal::read(&file, &path)
            .map_err(|e| Error::new("invalid_history", format!("{e:#}")).session(id))
    }
    fn status(&self, id: &str) -> Result<Value> {
        let replay = self.replay(id)?;
        let task = self.active.as_ref().filter(|a| a.association.session_id == id).map(|a| json!({"task_id":a.association.task_id,"message_id":a.association.message_id,"cancel_requested":*a.cancel.borrow()}));
        Ok(
            json!({"session_id":id,"loaded":self.sessions.contains_key(id),"state":if task.is_some(){"running"}else{"idle"},"active_task":task,"status":if task.is_some(){json!("running")}else{json!(replay.status)}}),
        )
    }
    fn load(&mut self, id: &str) -> Result<Value> {
        let metadata = self.metadata(id)?;
        if !self.sessions.contains_key(id) {
            let loaded = self
                .store
                .open_for_api(id, self.client.api().as_str())
                .map_err(|e| {
                    let message = format!("{e:#}");
                    if matches!(
                        e.downcast_ref::<std::fs::TryLockError>(),
                        Some(std::fs::TryLockError::WouldBlock)
                    ) {
                        Error::new("locked", message).session(id)
                    } else if e
                        .chain()
                        .any(|e| e.downcast_ref::<std::io::Error>().is_some())
                    {
                        io_error(e).session(id)
                    } else {
                        Error::new("invalid_history", message).session(id)
                    }
                })?;
            let agent = build_agent(&self.store, &self.client, loaded).map_err(io_error)?;
            self.sessions.insert(
                id.into(),
                Session {
                    metadata: metadata.clone(),
                    agent: Some(agent),
                },
            );
        }
        let mut result = self.status(id)?;
        result["metadata"] = json!(metadata);
        Ok(result)
    }
    fn request(
        &mut self,
        request: Request,
        events: &mpsc::UnboundedSender<AgentEvent>,
        output: &mpsc::UnboundedSender<Value>,
    ) -> bool {
        let mut shutdown = false;
        let result = if request.version != 1 {
            Err(Error::new(
                "unsupported_version",
                "expected protocol version 1",
            ))
        } else {
            self.dispatch(&request.method, request.params, events, &mut shutdown)
        };
        // Agent events are forwarded only after this response is enqueued.
        let response = match result {
            Ok(value) => json!({"version":1,"type":"response","id":request.id,"result":value}),
            Err(error) => json!({"version":1,"type":"response","id":request.id,"error":error}),
        };
        let _ = output.send(response);
        shutdown
    }
    fn dispatch(
        &mut self,
        method: &str,
        value: Value,
        events: &mpsc::UnboundedSender<AgentEvent>,
        shutdown: &mut bool,
    ) -> Result<Value> {
        match method {
            "session.create" => {
                let p: Create = params(value)?;
                let cwd = p.working_directory.unwrap_or_else(|| self.cwd.clone());
                if !cwd.is_dir() {
                    return Err(Error::new(
                        "invalid_request",
                        "working_directory must exist and be a directory",
                    ));
                }
                let id = self
                    .store
                    .create_with_api(
                        &cwd,
                        &self.model,
                        p.system_prompt.as_deref().unwrap_or(SYSTEM_PROMPT),
                        self.client.api().as_str(),
                        self.config,
                    )
                    .map_err(io_error)?;
                self.load(&id)
            }
            "session.load" => {
                let p: SessionId = params(value)?;
                self.load(&p.session_id)
            }
            "session.list" => {
                let _: Empty = params(value)?;
                let summaries = self.store.list().map_err(io_error)?.into_iter().map(|s| {
                    let active = self.active.as_ref().filter(|a| a.association.session_id == s.metadata.id).map(|a| &a.association.task_id);
                    json!({"metadata":s.metadata,"updated_at":s.updated_at,"status":if active.is_some(){json!("running")}else{json!(s.status)},"in_use":s.in_use,"unavailable":s.unavailable,"loaded":self.sessions.contains_key(&s.metadata.id),"state":if active.is_some(){"running"}else{"idle"},"active_task_id":active})
                }).collect::<Vec<_>>();
                Ok(json!({"sessions":summaries}))
            }
            "session.rename" => {
                let p: Rename = params(value)?;
                self.metadata(&p.session_id)?;
                if p.title.trim().is_empty() || p.title.contains(['\r', '\n']) {
                    return Err(Error::new(
                        "invalid_request",
                        "title must be a nonempty single line",
                    )
                    .session(&p.session_id));
                }
                let metadata = self
                    .store
                    .rename(&p.session_id, &p.title)
                    .map_err(io_error)?;
                if let Some(session) = self.sessions.get_mut(&p.session_id) {
                    session.metadata = metadata.clone();
                }
                Ok(json!({"session_id":p.session_id,"metadata":metadata}))
            }
            "session.history" => {
                let p: History = params(value)?;
                let replay = self.replay(&p.session_id)?;
                let limit = p.limit.unwrap_or(100);
                if limit == 0 {
                    return Err(Error::new(
                        "invalid_request",
                        "history limit must be positive",
                    ));
                }
                let end = p.offset.saturating_add(limit).min(replay.history.len());
                let records = replay.history.iter().zip(&replay.history_timestamps).enumerate().skip(p.offset).take(limit).map(|(offset,(record,timestamp_ms))| json!({"offset":offset,"timestamp_ms":timestamp_ms,"record":record})).collect::<Vec<_>>();
                let active = self
                    .active
                    .as_ref()
                    .filter(|a| a.association.session_id == p.session_id)
                    .map(|a| &a.association.task_id);
                Ok(
                    json!({"session_id":p.session_id,"records":records,"next_offset":if end<replay.history.len(){Some(end)}else{None},"status":if active.is_some(){json!("running")}else{json!(replay.status)},"state":if active.is_some(){"running"}else{"idle"},"active_task_id":active}),
                )
            }
            "session.status" => {
                let p: SessionId = params(value)?;
                self.status(&p.session_id)
            }
            "session.release" => {
                let p: SessionId = params(value)?;
                self.metadata(&p.session_id)?;
                if self
                    .active
                    .as_ref()
                    .is_some_and(|a| a.association.session_id == p.session_id)
                {
                    return Err(
                        Error::new("busy", "session has an active task").session(&p.session_id)
                    );
                }
                self.sessions.remove(&p.session_id);
                Ok(json!({"session_id":p.session_id,"loaded":false}))
            }
            "task.send" => {
                let p: SendTask = params(value)?;
                if self.active.is_some() {
                    return Err(Error::new("busy", "runtime execution slot is occupied")
                        .session(&p.session_id));
                }
                if p.message.text.is_empty() && p.message.images.is_empty() {
                    return Err(
                        Error::new("invalid_request", "message is empty").session(&p.session_id)
                    );
                }
                if self.client.api() == ModelApi::ChatCompletions && !p.message.images.is_empty() {
                    return Err(Error::new(
                        "invalid_request",
                        "images are unsupported by this API",
                    )
                    .session(&p.session_id));
                }
                let session = self.sessions.get_mut(&p.session_id).ok_or_else(|| {
                    Error::new("not_found", "session is not loaded").session(&p.session_id)
                })?;
                let task_id = journal::new_id().map_err(io_error)?;
                let message_id = journal::new_id().map_err(io_error)?;
                let (cancel, cancel_rx) = watch::channel(false);
                let mut agent = session
                    .agent
                    .take()
                    .ok_or_else(|| {
                        Error::new("busy", "session execution is unavailable")
                            .session(&p.session_id)
                    })?
                    .with_events(events.clone())
                    .with_cancel(cancel_rx);
                let message = UserMessage {
                    text: p.message.text,
                    images: p
                        .message
                        .images
                        .into_iter()
                        .map(|i| ImageAttachment::new(i.name, i.data_url))
                        .collect(),
                };
                let association = Association {
                    session_id: p.session_id,
                    task_id: task_id.clone(),
                    message_id: message_id.clone(),
                };
                let worker = tokio::spawn(async move {
                    let result = agent.handle_task(message, task_id, message_id).await;
                    (agent, result)
                });
                let response =
                    json!({"task_id":association.task_id,"message_id":association.message_id});
                self.active = Some(Active {
                    association,
                    cancel,
                    worker,
                });
                Ok(response)
            }
            "task.cancel" => {
                let p: Cancel = params(value)?;
                let status = match &self.active {
                    Some(a)
                        if a.association.session_id == p.session_id
                            && a.association.task_id == p.task_id
                            && !a.worker.is_finished() =>
                    {
                        if *a.cancel.borrow() {
                            "already_requested"
                        } else {
                            a.cancel.send_replace(true);
                            "requested"
                        }
                    }
                    _ => "not_running",
                };
                Ok(json!({"status":status}))
            }
            "runtime.shutdown" => {
                let _: Empty = params(value)?;
                *shutdown = true;
                Ok(json!({"state":"draining"}))
            }
            _ => Err(Error::new(
                "unknown_method",
                format!("unknown method: {method}"),
            )),
        }
    }
    pub async fn run<R, W>(mut self, input: R, mut writer: W) -> anyhow::Result<()>
    where
        R: AsyncBufRead + Unpin,
        W: AsyncWrite + Unpin + Send + 'static,
    {
        let (output, mut messages) = mpsc::unbounded_channel::<Value>();
        let mut write_job = tokio::spawn(async move {
            while let Some(message) = messages.recv().await {
                let mut bytes = serde_json::to_vec(&message)?;
                bytes.push(b'\n');
                writer.write_all(&bytes).await?;
                writer.flush().await?;
            }
            Ok::<_, anyhow::Error>(())
        });
        let _ = output.send(json!({"version":1,"type":"ready","api":self.client.api().as_str()}));
        let (events, mut event_rx) = mpsc::unbounded_channel();
        let mut lines = input.lines();
        let mut accepting = true;
        let mut writer_result = None;
        let mut input_error = None;
        while accepting || self.active.is_some() {
            tokio::select! {
                biased;
                result = &mut write_job, if writer_result.is_none() => {
                    writer_result = Some(result); accepting = false;
                }
                line = lines.next_line(), if accepting => {
                    match line {
                        Ok(Some(line)) => {
                            let parsed = serde_json::from_str::<Value>(&line);
                            let id = parsed.as_ref().ok().and_then(|v| v.get("id")).filter(|v| v.is_string()).cloned().unwrap_or(Value::Null);
                            match parsed.map_err(|e| e.to_string()).and_then(|v| serde_json::from_value::<Request>(v).map_err(|e| e.to_string())) {
                                Ok(request) => {if self.request(request, &events, &output) { accepting = false; }}
                                Err(message) => { let _ = output.send(json!({"version":1,"type":"response","id":id,"error":{"code":"invalid_request","message":message}})); }
                            }
                        }
                        Ok(None) => accepting = false,
                        Err(error) => {input_error=Some(error); accepting=false;}
                    }
                }
                Some(event) = event_rx.recv(), if self.active.is_some() => {
                    if let Some(active) = &self.active {send_event(&output, &active.association, event);}
                }
                result = async { (&mut self.active.as_mut().unwrap().worker).await }, if self.active.is_some() => {
                    let active = self.active.take().unwrap();
                    while let Ok(event) = event_rx.try_recv() {send_event(&output,&active.association,event);}
                    let terminal = match result {
                        Ok((agent, result)) => {
                            self.sessions.get_mut(&active.association.session_id).unwrap().agent = Some(agent);
                            match result {
                                Ok(()) => json!({"type":"task_finished","status":"completed"}),
                                Err(error) if is_interrupted(&error) => json!({"type":"task_finished","status":"interrupted"}),
                                Err(error) => {
                                    let code = if error.chain().any(|e| e.downcast_ref::<std::io::Error>().is_some()) {"io_error"} else {"model_error"};
                                    json!({"type":"task_finished","status":"failed","error":{"code":code,"message":format!("{error:#}")}})
                                }
                            }
                        }
                        Err(error) => json!({"type":"task_finished","status":"failed","error":{"code":"runtime_error","message":error.to_string()}})
                    };
                    send_machine_event(&output,&active.association,terminal);
                }
            }
        }
        drop(self);
        drop(output);
        match writer_result {
            Some(result) => result??,
            None => write_job.await??,
        }
        if let Some(error) = input_error {
            return Err(error.into());
        }
        Ok(())
    }
}
fn send_machine_event(output: &mpsc::UnboundedSender<Value>, a: &Association, event: Value) {
    let _ = output.send(json!({"version":1,"type":"event","session_id":a.session_id,"task_id":a.task_id,"message_id":a.message_id,"event":event}));
}
fn send_event(output: &mpsc::UnboundedSender<Value>, a: &Association, event: AgentEvent) {
    let event = match event {
        AgentEvent::TaskStarted { .. } => json!({"type":"task_started"}),
        AgentEvent::ModelRequestStarted { purpose } => {
            json!({"type":"model_request_started","purpose":match purpose {ModelRequestPurpose::Generation=>"generation",ModelRequestPurpose::Compaction=>"compaction"}})
        }
        AgentEvent::ContextUpdated {
            model,
            context_tokens,
            context_window,
            input_limit,
        } => {
            json!({"type":"context_updated","model":model,"context_tokens":context_tokens,"context_window":context_window,"input_limit":input_limit})
        }
        AgentEvent::ResponseText { item_id, text } => {
            json!({"type":"response_text","item_id":item_id,"text":text})
        }
        AgentEvent::ResponseTextDelta { item_id, delta } => {
            json!({"type":"response_text_delta","item_id":item_id,"delta":delta})
        }
        AgentEvent::Reasoning { item_id, text } => {
            json!({"type":"reasoning","item_id":item_id,"text":text})
        }
        AgentEvent::ReasoningDelta { item_id, delta } => {
            json!({"type":"reasoning_delta","item_id":item_id,"delta":delta})
        }
        AgentEvent::ReasoningCompleted { item_id } => {
            json!({"type":"reasoning_completed","item_id":item_id})
        }
        AgentEvent::ToolStarted {
            call_id,
            name,
            arguments,
        } => json!({"type":"tool_started","call_id":call_id,"name":name,"arguments":arguments}),
        AgentEvent::ToolFinished {
            call_id,
            name,
            output,
        } => json!({"type":"tool_finished","call_id":call_id,"name":name,"output":output}),
        AgentEvent::Compaction {
            tokens_before,
            tokens_after,
            summary_output_tokens,
        } => {
            json!({"type":"compaction","tokens_before":tokens_before,"tokens_after":tokens_after,"summary_output_tokens":summary_output_tokens})
        }
        AgentEvent::Usage {
            input_tokens,
            output_tokens,
        } => json!({"type":"usage","input_tokens":input_tokens,"output_tokens":output_tokens}),
        AgentEvent::TurnCompleted | AgentEvent::TurnInterrupted | AgentEvent::TurnFailed { .. } => {
            return;
        }
    };
    send_machine_event(output, a, event);
}

#[cfg(test)]
#[path = "../../tests/session/runtime.rs"]
mod tests;
