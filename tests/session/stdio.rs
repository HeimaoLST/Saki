use super::*;
use crate::{
    agent::{
        conversation::CompactionConfig,
        events::EventLimits,
        journal::{self, JournalRecord, TaskStatus},
    },
    client::ModelClient,
    session::store::SessionStore,
    test_support::{completed, read_request, scripted_stream, sse},
};
use serde_json::Value;
use std::{
    pin::Pin,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    task::{Context, Poll},
};
use tokio::{
    io::{BufReader, DuplexStream},
    sync::Notify,
};

struct Workspace(PathBuf);
impl Workspace {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("saki-stdio-{}", journal::new_id().unwrap()));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }
    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }
    fn runtime(&self, client: ModelClient) -> Runtime {
        Runtime::new(
            self.store(),
            client,
            self.0.clone(),
            "test".into(),
            CompactionConfig::new(65536),
        )
        .with_event_limits(EventLimits {
            messages: 1,
            bytes: 4096,
            max_event_bytes: 2048,
        })
    }
}
impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

struct ObservedWriter {
    inner: DuplexStream,
    waker: Arc<futures_util::task::AtomicWaker>,
    blocked: Arc<Notify>,
    stall: Arc<AtomicBool>,
}
impl AsyncWrite for ObservedWriter {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        bytes: &[u8],
    ) -> Poll<io::Result<usize>> {
        self.waker.register(cx.waker());
        if self.stall.load(Ordering::SeqCst) {
            self.blocked.notify_one();
            return Poll::Pending;
        }
        Pin::new(&mut self.inner).poll_write(cx, bytes)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.inner).poll_shutdown(cx)
    }
}
async fn command(input: &mut DuplexStream, id: &str, method: &str, params: Value) {
    let mut bytes =
        serde_json::to_vec(&json!({"version":1,"id":id,"method":method,"params":params})).unwrap();
    bytes.push(b'\n');
    input.write_all(&bytes).await.unwrap();
}
async fn row(output: &mut tokio::io::Lines<BufReader<DuplexStream>>) -> Value {
    serde_json::from_str(
        &tokio::time::timeout(Duration::from_secs(5), output.next_line())
            .await
            .unwrap()
            .unwrap()
            .unwrap(),
    )
    .unwrap()
}

#[tokio::test]
async fn blocked_stdout_accepts_cancel_and_eof_then_drains_after_write_timeout() {
    let workspace = Workspace::new();
    let body = (0..100)
        .map(|_| sse(json!({"type":"response.output_text.delta","item_id":"m","delta":"chunk"})))
        .collect::<String>()
        + &sse(json!({"type":"response.completed","response":completed("done")}));
    let (client, mut server) = scripted_stream(vec![body]).await;
    let (mut input, runtime_input) = tokio::io::duplex(16384);
    let (runtime_output, output) = tokio::io::duplex(64);
    let blocked = Arc::new(Notify::new());
    let stall = Arc::new(AtomicBool::new(false));
    let writer_waker = Arc::new(futures_util::task::AtomicWaker::new());
    let worker = tokio::spawn(run_with_options(
        workspace.runtime(client),
        BufReader::new(runtime_input),
        ObservedWriter {
            inner: runtime_output,
            waker: writer_waker.clone(),
            blocked: blocked.clone(),
            stall: stall.clone(),
        },
        StdioOptions {
            write_timeout: Duration::from_secs(30),
            max_frame_bytes: 2048,
            output_bytes: 4096,
            output_messages: 1,
        },
    ));
    let mut output = BufReader::new(output).lines();
    assert_eq!(row(&mut output).await["type"], "ready");
    command(&mut input, "create", "session.create", json!({})).await;
    let id = row(&mut output).await["result"]["session_id"]
        .as_str()
        .unwrap()
        .to_owned();
    command(
        &mut input,
        "send",
        "task.send",
        json!({"session_id":id,"message":{"text":"stream"}}),
    )
    .await;
    let accepted = row(&mut output).await;
    let task = accepted["result"]["task_id"].as_str().unwrap();
    let captured = tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            tokio::select! {
                biased;
                result=&mut server=>break result.unwrap(),
                _=row(&mut output)=>{}
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(captured.len(), 1);
    stall.store(true, Ordering::SeqCst);
    writer_waker.wake();
    tokio::time::timeout(Duration::from_secs(5), blocked.notified())
        .await
        .unwrap();
    command(
        &mut input,
        "cancel",
        "task.cancel",
        json!({"session_id":id,"task_id":task}),
    )
    .await;
    drop(input);
    tokio::time::timeout(Duration::from_secs(5), async {
        loop {
            if workspace.store().list().unwrap().iter().any(|session| {
                session.metadata.id == id && session.status == TaskStatus::Interrupted
            }) {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    tokio::time::pause();
    tokio::time::advance(Duration::from_secs(31)).await;
    let error = tokio::time::timeout(Duration::from_secs(5), worker)
        .await
        .unwrap()
        .unwrap()
        .unwrap_err();
    assert!(error.to_string().contains("timed out"), "{error:#}");
    tokio::time::resume();
    drop(output);
    let loaded = workspace.store().open(&id).unwrap();
    assert_eq!(loaded.replay.status, TaskStatus::Interrupted);
    assert!(loaded.replay.active_task.is_none());
    assert_eq!(
        loaded
            .replay
            .history
            .iter()
            .filter(|record| matches!(record, JournalRecord::TaskFinished { .. }))
            .count(),
        1
    );
}

#[tokio::test]
async fn eof_with_blocked_stdout_completes_business_without_cancelling_it() {
    let workspace = Workspace::new();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (arrived_tx, arrived) = tokio::sync::oneshot::channel();
    let (release, released) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        arrived_tx.send(()).unwrap();
        released.await.unwrap();
        let body = serde_json::to_vec(&completed("done")).unwrap();
        socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
        request
    });
    let (mut input, runtime_input) = tokio::io::duplex(16384);
    let (runtime_output, output) = tokio::io::duplex(64);
    let worker = tokio::spawn(run_with_options(
        workspace.runtime(client),
        BufReader::new(runtime_input),
        runtime_output,
        StdioOptions {
            write_timeout: Duration::from_millis(100),
            ..StdioOptions::default()
        },
    ));
    let mut output = BufReader::new(output).lines();
    row(&mut output).await;
    command(&mut input, "create", "session.create", json!({})).await;
    let id = row(&mut output).await["result"]["session_id"]
        .as_str()
        .unwrap()
        .to_owned();
    command(
        &mut input,
        "send",
        "task.send",
        json!({"session_id":id,"message":{"text":"finish"}}),
    )
    .await;
    let accepted = row(&mut output).await;
    assert!(accepted["result"]["task_id"].is_string());
    arrived.await.unwrap();
    drop(input);
    release.send(()).unwrap();
    assert!(
        tokio::time::timeout(Duration::from_secs(5), worker)
            .await
            .unwrap()
            .unwrap()
            .is_err()
    );
    drop(output);
    server.await.unwrap();
    let loaded = workspace.store().open(&id).unwrap();
    assert_eq!(loaded.replay.status, TaskStatus::Completed);
    assert!(loaded.replay.active_task.is_none());
}

#[cfg(unix)]
#[tokio::test]
async fn native_full_os_pipe_write_obeys_the_absolute_deadline() {
    use std::{fs::File, io::Write, os::fd::FromRawFd};
    let mut descriptors = [-1; 2];
    assert_eq!(unsafe { libc::pipe(descriptors.as_mut_ptr()) }, 0);
    let _reader = unsafe { File::from_raw_fd(descriptors[0]) };
    let file = unsafe { File::from_raw_fd(descriptors[1]) };
    let writer = NativeStdout::from_file(file).unwrap();
    let NativeStdout::Pollable(descriptor) = &writer else {
        panic!("pipe must use nonblocking IO")
    };
    let mut file = descriptor.get_ref();
    let bytes = [0u8; 4096];
    loop {
        match file.write(&bytes) {
            Ok(_) => {}
            Err(error) if error.kind() == io::ErrorKind::WouldBlock => break,
            Err(error) => panic!("{error}"),
        }
    }
    let (control, control_rx) = mpsc::channel(1);
    let (events, events_rx) = mpsc::channel(1);
    let budget = Arc::new(Semaphore::new(1024));
    let permit = budget.acquire_many_owned(4).await.unwrap();
    control
        .send(Frame {
            bytes: b"row\n".to_vec(),
            _budget: BufferMetrics::default().track(permit),
            _event_budget: None,
        })
        .await
        .unwrap();
    drop(control);
    drop(events);
    let result = tokio::time::timeout(
        Duration::from_secs(5),
        write_output(
            writer,
            control_rx,
            events_rx,
            OutputState::default(),
            Duration::from_millis(30),
        ),
    )
    .await
    .unwrap();
    assert!(result.unwrap_err().to_string().contains("timed out"));
}

#[cfg(unix)]
#[tokio::test]
async fn native_stdout_supports_regular_file_redirection() {
    let workspace = Workspace::new();
    let path = workspace.0.join("output.jsonl");
    let file = std::fs::File::create(&path).unwrap();
    let mut writer = NativeStdout::from_file(file).unwrap();
    writer.write_all(b"row\n").await.unwrap();
    writer.flush().await.unwrap();
    drop(writer);
    assert_eq!(std::fs::read(path).unwrap(), b"row\n");
}

#[tokio::test]
async fn serialized_output_keeps_its_byte_budget_until_the_writer_drops_the_frame() {
    let state = OutputState::default();
    let (sender, mut receiver) = mpsc::channel(1);
    let output = Output {
        sender,
        budget: Arc::new(Semaphore::new(64)),
        state,
        limit: 64,
        metrics: BufferMetrics::default(),
    };
    assert!(output.send(&json!({"marker":"value"}), None).await);
    let frame = receiver.recv().await.unwrap();
    let stats = output.metrics.snapshot();
    assert_eq!(stats.bytes, frame.bytes.len());
    assert_eq!(stats.messages, 1);
    assert_eq!(stats.peak_bytes, frame.bytes.len());
    drop(frame);
    assert_eq!(output.metrics.snapshot().bytes, 0);
}

#[tokio::test]
async fn finishing_a_task_does_not_discard_a_partially_received_request() {
    let workspace = Workspace::new();
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (arrived_tx, arrived) = tokio::sync::oneshot::channel();
    let (release, released) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        read_request(&mut socket).await;
        arrived_tx.send(()).unwrap();
        released.await.unwrap();
        let body = serde_json::to_vec(&completed("done")).unwrap();
        socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await.unwrap();
        socket.write_all(&body).await.unwrap();
    });
    let (mut input, runtime_input) = tokio::io::duplex(32);
    let (runtime_output, output) = tokio::io::duplex(65536);
    let worker = tokio::spawn(run(
        workspace.runtime(client),
        BufReader::new(runtime_input),
        runtime_output,
    ));
    let mut output = BufReader::new(output).lines();
    row(&mut output).await;
    command(&mut input, "create", "session.create", json!({})).await;
    let id = row(&mut output).await["result"]["session_id"]
        .as_str()
        .unwrap()
        .to_owned();
    command(
        &mut input,
        "send",
        "task.send",
        json!({"session_id":id,"message":{"text":"finish"}}),
    )
    .await;
    let accepted = row(&mut output).await;
    assert_eq!(accepted["id"], "send");
    arrived.await.unwrap();
    let mut request = serde_json::to_vec(
        &json!({"version":1,"id":"after","method":"session.status","params":{"session_id":id}}),
    )
    .unwrap();
    request.push(b'\n');
    // More than the input pipe's capacity: some prefix has been consumed before completion.
    let cut = request.len() / 2;
    assert!(cut > 32);
    input.write_all(&request[..cut]).await.unwrap();
    release.send(()).unwrap();
    loop {
        if row(&mut output).await["event"]["type"] == "task_finished" {
            break;
        }
    }
    input.write_all(&request[cut..]).await.unwrap();
    let status = row(&mut output).await;
    assert_eq!(status["id"], "after");
    assert_eq!(status["result"]["status"], "completed");
    drop(input);
    while output.next_line().await.unwrap().is_some() {}
    worker.await.unwrap().unwrap();
    server.await.unwrap();
}

struct SlowWriter {
    delay: Pin<Box<tokio::time::Sleep>>,
    completed: Arc<std::sync::atomic::AtomicUsize>,
}
impl AsyncWrite for SlowWriter {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        bytes: &[u8],
    ) -> Poll<io::Result<usize>> {
        use std::future::Future;
        std::task::ready!(self.delay.as_mut().poll(cx));
        self.completed.fetch_add(1, Ordering::SeqCst);
        self.delay
            .as_mut()
            .reset(tokio::time::Instant::now() + Duration::from_secs(2));
        Poll::Ready(Ok(bytes.len()))
    }
    fn poll_flush(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
    fn poll_shutdown(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
}

#[tokio::test(start_paused = true)]
async fn drain_deadline_bounds_the_whole_backlog_despite_progress_on_each_frame() {
    let state = OutputState::default();
    let (sender, control_rx) = mpsc::channel(8);
    let (event_tx, event_rx) = mpsc::channel(1);
    let output = Output {
        sender,
        budget: Arc::new(Semaphore::new(1024)),
        state: state.clone(),
        limit: 1024,
        metrics: BufferMetrics::default(),
    };
    for _ in 0..5 {
        assert!(output.send(&json!({"text":"frame"}), None).await);
    }
    let metrics = output.metrics.clone();
    drop(output);
    drop(event_tx);
    let completed = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let writer = SlowWriter {
        delay: Box::pin(tokio::time::sleep(Duration::from_secs(2))),
        completed: completed.clone(),
    };
    let (drained_tx, drained_rx) = watch::channel(false);
    let deadline = tokio::spawn(drain_output(
        drained_rx,
        state.clone(),
        Duration::from_secs(3),
    ));
    let started = tokio::time::Instant::now();
    drained_tx.send_replace(true);
    let result = write_output(writer, control_rx, event_rx, state, Duration::from_secs(3)).await;
    deadline.await.unwrap();
    assert!(result.is_err());
    assert_eq!(
        tokio::time::Instant::now() - started,
        Duration::from_secs(3)
    );
    assert_eq!(completed.load(Ordering::SeqCst), 1);
    assert_eq!(metrics.snapshot().bytes, 0);
}

struct GateWriter {
    mode: Arc<std::sync::atomic::AtomicUsize>,
    waker: Arc<futures_util::task::AtomicWaker>,
    bytes: Arc<std::sync::Mutex<Vec<u8>>>,
}
impl AsyncWrite for GateWriter {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        bytes: &[u8],
    ) -> Poll<io::Result<usize>> {
        self.waker.register(cx.waker());
        match self.mode.load(Ordering::SeqCst) {
            0 => {
                self.bytes.lock().unwrap().extend_from_slice(bytes);
                Poll::Ready(Ok(bytes.len()))
            }
            1 => Poll::Pending,
            _ => Poll::Ready(Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "injected output loss",
            ))),
        }
    }
    fn poll_flush(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
    fn poll_shutdown(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
}

async fn until(mut predicate: impl FnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(5), async {
        while !predicate() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("controlled pipeline never reached the required boundary");
}

#[tokio::test]
async fn two_task_pipeline_backpressure_keeps_control_independent_and_releases_all_budgets() {
    use crate::{agent::message::UserMessage, session::runtime::RuntimeHandle};
    use tokio::sync::oneshot;
    for end in ["recover", "disconnect", "deadline"] {
        let workspace = Workspace::new();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let client = ModelClient::new(&crate::client::ClientConfig::new(
            crate::client::ModelApi::Responses,
            "fixture",
            format!("http://{}/v1", listener.local_addr().unwrap()),
        ))
        .unwrap();
        let (arrived_tx, arrived) = oneshot::channel();
        let (release, released) = oneshot::channel();
        let server = tokio::spawn(async move {
            let mut sockets = Vec::new();
            for _ in 0..2 {
                let (mut socket, _) = listener.accept().await.unwrap();
                read_request(&mut socket).await;
                sockets.push(socket);
            }
            arrived_tx.send(()).unwrap();
            released.await.unwrap();
            let body = (0..1000).map(|_| sse(json!({"type":"response.output_text.delta","item_id":"same-id","delta":"text"}))).collect::<String>()
                + &sse(json!({"type":"response.completed","response":completed("done")}));
            for mut socket in sockets {
                let header = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                );
                let _ = socket.write_all(header.as_bytes()).await;
                let _ = socket.write_all(body.as_bytes()).await;
            }
        });
        let (handle, actor) = workspace
            .runtime(client)
            .with_max_active_tasks(2)
            .unwrap()
            .start();
        let state = handle.output_state();
        let (control_tx, control_rx) = mpsc::channel(1);
        let (event_tx, event_rx) = mpsc::channel(1);
        let make_output = |sender| Output {
            sender,
            budget: Arc::new(Semaphore::new(4096)),
            state: state.clone(),
            limit: 2048,
            metrics: BufferMetrics::default(),
        };
        let control = make_output(control_tx);
        let events = make_output(event_tx);
        let mode = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let waker = Arc::new(futures_util::task::AtomicWaker::new());
        let bytes = Arc::new(std::sync::Mutex::new(Vec::new()));
        let writer_state = state.clone();
        let writer_mode = mode.clone();
        let writer_waker = waker.clone();
        let writer_bytes = bytes.clone();
        let writer = tokio::spawn(async move {
            let result = write_output(
                GateWriter {
                    mode: writer_mode,
                    waker: writer_waker,
                    bytes: writer_bytes,
                },
                control_rx,
                event_rx,
                writer_state.clone(),
                Duration::from_secs(30),
            )
            .await;
            if result.is_err() {
                writer_state.close();
            }
            result
        });
        let mut tasks = Vec::new();
        let mut jobs = JoinSet::new();
        for index in 0..2 {
            let id = match handle
                .call(RuntimeCommand::Create {
                    working_directory: None,
                    system_prompt: None,
                })
                .await
                .unwrap()
            {
                RuntimeReply::Session(value) => value.session_id,
                _ => panic!("session expected"),
            };
            let task = match handle
                .call(RuntimeCommand::Send {
                    session_id: id.clone(),
                    message: UserMessage::text("stream"),
                })
                .await
                .unwrap()
            {
                RuntimeReply::Accepted(task) => task,
                _ => panic!("accepted expected"),
            };
            tasks.push((
                id,
                task.association.task_id.clone(),
                task.events.buffer_metrics(),
            ));
            jobs.spawn(deliver(
                control.clone(),
                events.clone(),
                Some(format!("send-{index}")),
                Ok(RuntimeReply::Accepted(task)),
            ));
        }
        arrived.await.unwrap();
        mode.store(1, Ordering::SeqCst);
        release.send(()).unwrap();
        until(|| {
            tasks
                .iter()
                .all(|task| task.2.snapshot().waiting_producers > 0)
                && events.metrics.snapshot().waiting_producers > 0
        })
        .await;
        // Both decoders await their Agent callback, and the dispatcher awaits output capacity.
        for (_, _, metrics) in &tasks {
            let stats = metrics.snapshot();
            assert!(stats.peak_bytes <= 4096);
            // One producer, source queue, dispatcher, output queue and writer may each own an event.
            assert!(stats.peak_messages <= 5);
        }
        assert!(events.metrics.snapshot().peak_bytes <= 4096);
        assert!(events.metrics.snapshot().peak_messages <= 4);
        // Fill control output too; control execution must still not wait on its response writer.
        for index in 0..4 {
            let output = control.clone();
            jobs.spawn(async move {
                reply(
                    &output,
                    &Some(format!("control-{index}")),
                    json!({"ok":true}),
                )
                .await;
            });
        }
        until(|| control.metrics.snapshot().waiting_producers > 0).await;
        assert!(control.metrics.snapshot().peak_bytes <= 4096);
        assert!(control.metrics.snapshot().peak_messages <= 6);
        async fn status(
            handle: &RuntimeHandle,
            id: &str,
            task: &str,
        ) -> crate::session::runtime::TaskState {
            match handle
                .call(RuntimeCommand::TaskStatus {
                    session_id: id.into(),
                    task_id: task.into(),
                })
                .await
                .unwrap()
            {
                RuntimeReply::TaskStatus(value) => value,
                _ => panic!("status expected"),
            }
        }
        assert_eq!(
            status(&handle, &tasks[1].0, &tasks[1].1).await.phase,
            "running"
        );
        assert!(matches!(
            handle
                .call(RuntimeCommand::Cancel {
                    session_id: tasks[0].0.clone(),
                    task_id: tasks[0].1.clone()
                })
                .await
                .unwrap(),
            RuntimeReply::Cancelled {
                status: "requested"
            }
        ));
        until(|| tasks[0].2.snapshot().waiting_producers == 0).await;
        let mut a_status = status(&handle, &tasks[0].0, &tasks[0].1).await;
        while a_status.phase != "finished" {
            tokio::task::yield_now().await;
            a_status = status(&handle, &tasks[0].0, &tasks[0].1).await;
        }
        assert_eq!(a_status.status, "interrupted");
        assert_eq!(
            status(&handle, &tasks[1].0, &tasks[1].1).await.phase,
            "running"
        );
        assert!(tasks[1].2.snapshot().waiting_producers > 0);
        handle.call(RuntimeCommand::Shutdown).await.unwrap();
        assert!(
            matches!(handle.call(RuntimeCommand::Create {working_directory:None,system_prompt:None}).await,Err(error) if error.code=="io_error")
        );
        if end == "deadline" {
            tokio::time::pause();
            tokio::time::advance(Duration::from_secs(31)).await;
        } else {
            mode.store(if end == "recover" { 0 } else { 2 }, Ordering::SeqCst);
            waker.wake();
        }
        drop(handle);
        while let Some(result) = jobs.join_next().await {
            result.unwrap();
        }
        actor.await.unwrap().unwrap();
        drop(control);
        drop(events);
        let result = writer.await.unwrap();
        if end == "recover" {
            result.unwrap();
        } else {
            assert!(result.is_err());
        }
        if end == "deadline" {
            tokio::time::resume();
        }
        server.await.unwrap();
        for (index, (id, _, metrics)) in tasks.iter().enumerate() {
            let stats = metrics.snapshot();
            assert_eq!(stats.bytes, 0);
            assert_eq!(stats.messages, 0);
            assert_eq!(stats.waiting_producers, 0);
            let loaded = workspace.store().open(id).unwrap();
            assert_eq!(
                loaded.replay.status,
                if index == 0 {
                    TaskStatus::Interrupted
                } else {
                    TaskStatus::Completed
                }
            );
            assert_eq!(
                loaded
                    .replay
                    .history
                    .iter()
                    .filter(|record| matches!(record, JournalRecord::TaskFinished { .. }))
                    .count(),
                1
            );
        }
        if end == "recover" {
            let rows = bytes
                .lock()
                .unwrap()
                .split(|byte| *byte == b'\n')
                .filter(|line| !line.is_empty())
                .map(|line| serde_json::from_slice::<Value>(line).unwrap())
                .collect::<Vec<_>>();
            for (id, task, _) in &tasks {
                let own = rows
                    .iter()
                    .filter(|row| row["task_id"] == *task)
                    .collect::<Vec<_>>();
                assert!(own.iter().all(|row| row["session_id"] == *id));
                assert_eq!(
                    own.iter()
                        .filter(|row| row["event"]["type"] == "task_finished")
                        .count(),
                    1
                );
                assert_eq!(own.first().unwrap()["event"]["type"], "task_started");
                assert_eq!(own.last().unwrap()["event"]["type"], "task_finished");
            }
            assert!(rows.iter().any(
                |row| row["task_id"] == tasks[0].1 && row["event"]["history_required"] == true
            ));
        }
    }
}

#[tokio::test]
async fn eof_drains_both_accepted_tasks_with_open_blocked_and_disconnected_output() {
    use tokio::sync::oneshot;
    for end in ["open", "blocked", "disconnected"] {
        let workspace = Workspace::new();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let client = ModelClient::new(&crate::client::ClientConfig::new(
            crate::client::ModelApi::Responses,
            "fixture",
            format!("http://{}/v1", listener.local_addr().unwrap()),
        ))
        .unwrap();
        let (arrived_tx, mut arrived) = oneshot::channel();
        let (release, released) = oneshot::channel();
        let server = tokio::spawn(async move {
            let mut sockets = Vec::new();
            for _ in 0..2 {
                let (mut socket, _) = listener.accept().await.unwrap();
                read_request(&mut socket).await;
                sockets.push(socket);
            }
            arrived_tx.send(()).unwrap();
            released.await.unwrap();
            let body = serde_json::to_vec(&completed("done")).unwrap();
            for mut socket in sockets {
                let header = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    body.len()
                );
                let _ = socket.write_all(header.as_bytes()).await;
                let _ = socket.write_all(&body).await;
            }
        });
        let (mut input, runtime_input) = tokio::io::duplex(16384);
        let (runtime_output, output) = tokio::io::duplex(1024);
        let blocked = Arc::new(Notify::new());
        let stall = Arc::new(AtomicBool::new(false));
        let writer_waker = Arc::new(futures_util::task::AtomicWaker::new());
        let worker = tokio::spawn(run_with_options(
            workspace.runtime(client).with_max_active_tasks(2).unwrap(),
            BufReader::new(runtime_input),
            ObservedWriter {
                inner: runtime_output,
                waker: writer_waker.clone(),
                blocked: blocked.clone(),
                stall: stall.clone(),
            },
            StdioOptions {
                write_timeout: Duration::from_secs(30),
                ..StdioOptions::default()
            },
        ));
        let mut output = BufReader::new(output).lines();
        row(&mut output).await;
        let mut ids = Vec::new();
        for index in 0..2 {
            let request = format!("create-{index}");
            command(&mut input, &request, "session.create", json!({})).await;
            loop {
                let value = row(&mut output).await;
                if value["id"] == request {
                    ids.push(value["result"]["session_id"].as_str().unwrap().to_owned());
                    break;
                }
            }
        }
        for (index, id) in ids.iter().enumerate() {
            let request = format!("send-{index}");
            command(
                &mut input,
                &request,
                "task.send",
                json!({"session_id":id,"message":{"text":"finish"}}),
            )
            .await;
            loop {
                if row(&mut output).await["id"] == request {
                    break;
                }
            }
        }
        loop {
            tokio::select! {result=&mut arrived=>{result.unwrap();break;},_=row(&mut output)=>{}}
        }
        if end == "blocked" {
            stall.store(true, Ordering::SeqCst);
            writer_waker.wake();
        }
        drop(input);
        release.send(()).unwrap();
        let mut terminals = Vec::new();
        if end == "open" {
            while let Some(line) = output.next_line().await.unwrap() {
                let value: Value = serde_json::from_str(&line).unwrap();
                if value["event"]["type"] == "task_finished" {
                    terminals.push(value);
                }
            }
            assert_eq!(terminals.len(), 2);
            assert!(
                terminals
                    .iter()
                    .all(|value| value["event"]["status"] == "completed")
            );
        } else if end == "blocked" {
            tokio::time::timeout(Duration::from_secs(5), blocked.notified())
                .await
                .unwrap();
            tokio::time::pause();
            tokio::time::advance(Duration::from_secs(31)).await;
        } else {
            drop(output);
        }
        let result = worker.await.unwrap();
        if end == "open" {
            result.unwrap();
        } else {
            assert!(result.is_err());
        }
        if end == "blocked" {
            tokio::time::resume();
        }
        server.await.unwrap();
        for id in ids {
            let loaded = workspace.store().open(&id).unwrap();
            assert_eq!(loaded.replay.status, TaskStatus::Completed);
            assert!(loaded.replay.active_task.is_none());
            assert_eq!(
                loaded
                    .replay
                    .history
                    .iter()
                    .filter(|record| matches!(record, JournalRecord::TaskFinished { .. }))
                    .count(),
                1
            );
        }
    }
}
