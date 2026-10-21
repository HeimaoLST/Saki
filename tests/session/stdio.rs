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
    blocked: Arc<Notify>,
    stall: Arc<AtomicBool>,
}
impl AsyncWrite for ObservedWriter {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        bytes: &[u8],
    ) -> Poll<io::Result<usize>> {
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
    let (client, server) = scripted_stream(vec![body]).await;
    let (mut input, runtime_input) = tokio::io::duplex(16384);
    let (runtime_output, output) = tokio::io::duplex(64);
    let blocked = Arc::new(Notify::new());
    let stall = Arc::new(AtomicBool::new(false));
    let worker = tokio::spawn(run_with_options(
        workspace.runtime(client),
        BufReader::new(runtime_input),
        ObservedWriter {
            inner: runtime_output,
            blocked: blocked.clone(),
            stall: stall.clone(),
        },
        StdioOptions {
            write_timeout: Duration::from_millis(250),
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
    stall.store(true, Ordering::SeqCst);
    blocked.notified().await;
    command(
        &mut input,
        "cancel",
        "task.cancel",
        json!({"session_id":id,"task_id":task}),
    )
    .await;
    drop(input);
    let error = tokio::time::timeout(Duration::from_secs(5), worker)
        .await
        .unwrap()
        .unwrap()
        .unwrap_err();
    assert!(error.to_string().contains("timed out"), "{error:#}");
    drop(output);
    server.await.unwrap();
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
