use super::*;
use crate::{
    agent::{Agent, AgentEvent, conversation::Conversation, message::UserMessage, run_agent},
    test_support::{requests, scripted_http},
    tools::{
        ToolContext,
        mcp::test_support::python_command,
        tool_registry::{ToolError, ToolRegistry},
    },
};
use serde_json::{Value, json};
use std::{future::Future, path::PathBuf, pin::Pin, sync::Arc, time::Duration};
use tokio::{
    io::{AsyncBufReadExt, BufReader},
    net::{TcpListener, TcpStream},
    sync::{mpsc, oneshot, watch},
};

type BashExecution = Pin<Box<dyn Future<Output = Result<Value, ToolError>> + Send>>;
const WIRE_TIMEOUT: Duration = Duration::from_secs(5);

struct ContextDir {
    root: PathBuf,
    context: Arc<ToolContext>,
}

impl ContextDir {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "saki-bash-cleanup-{}",
            crate::agent::journal::new_id().unwrap()
        ));
        std::fs::create_dir_all(root.join("output")).unwrap();
        let context = Arc::new(ToolContext {
            cwd: root.clone(),
            output_dir: root.join("output"),
        });
        Self { root, context }
    }
}

impl Drop for ContextDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.root);
    }
}

struct ReapGate {
    resume: Option<oneshot::Sender<()>>,
    pid: i32,
}

impl ReapGate {
    fn release(&mut self) {
        if let Some(resume) = self.resume.take() {
            let _ = resume.send(());
        }
    }
}

impl Drop for ReapGate {
    fn drop(&mut self) {
        self.release();
        // Test cleanup: the Reaper owns the normal path, but a failed assertion
        // must not leave the harness process behind.
        unsafe {
            libc::kill(-(self.pid), libc::SIGKILL);
        }
    }
}

fn blocking_command(port: u16) -> String {
    let python = python_command();
    let script = format!(
        "import os,socket;s=socket.create_connection((\"127.0.0.1\",{port}));s.sendall((str(os.getpid())+\"\\n\").encode());s.recv(1)"
    );
    format!("exec '{}' -c '{}'", python.display(), script)
}

async fn start_blocked_bash(
    registry: &ToolRegistry,
    listener: &TcpListener,
) -> (i32, BashExecution, BufReader<TcpStream>) {
    let tool = registry.get("bash").expect("bash tool registered");
    let mut execution: BashExecution = (tool.handler)(json!({
        "command": blocking_command(listener.local_addr().unwrap().port()),
        "timeout_ms": 120_000
    }));
    let (socket, _) = tokio::select! {
        result = listener.accept() => result.expect("accept bash handshake"),
        result = &mut execution => panic!("bash finished before its handshake: {result:?}"),
    };
    let mut reader = BufReader::new(socket);
    let mut line = String::new();
    tokio::time::timeout(WIRE_TIMEOUT, reader.read_line(&mut line))
        .await
        .expect("PID handshake timed out")
        .expect("read PID handshake");
    let pid = line.trim().parse().expect("numeric PID");
    (pid, execution, reader)
}

async fn wait_for_cleanup_start(started: oneshot::Receiver<()>) {
    // The caller keeps the release sender in a ReapGate so a panic releases
    // the reaper instead of leaving a child behind.
    tokio::time::timeout(WIRE_TIMEOUT, started)
        .await
        .expect("reaper did not start")
        .expect("reaper start signal was dropped");
}

fn wait_for_exit_without_reaping(pid: i32) {
    let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
    loop {
        let result = unsafe {
            libc::waitid(
                libc::P_PID,
                pid as libc::id_t,
                &mut info,
                libc::WEXITED | libc::WNOWAIT,
            )
        };
        if result == 0 {
            return;
        }
        let error = std::io::Error::last_os_error();
        assert_eq!(
            error.raw_os_error(),
            Some(libc::EINTR),
            "waitid failed for {pid}: {error}"
        );
    }
}

#[tokio::test]
async fn dropped_bash_future_is_reaped_before_task_cleanup_completes() {
    let directory = ContextDir::new();
    let reaper = Arc::new(Reaper::default());
    let mut registry = ToolRegistry::new();
    register_with_reaper(&mut registry, directory.context.clone(), reaper.clone());
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();

    let (pid, execution, _handshake) = start_blocked_bash(&registry, &listener).await;
    let (started, resume) = reaper.hold_next_reap();
    let mut gate = ReapGate {
        resume: Some(resume),
        pid,
    };

    drop(execution);
    wait_for_cleanup_start(started).await;
    assert_eq!(
        unsafe { libc::kill(pid, 0) },
        0,
        "bash child must still exist while the reaper is gated"
    );

    let mut cleanup = Box::pin(registry.finish_task());
    assert!(
        futures_util::poll!(cleanup.as_mut()).is_pending(),
        "task cleanup completed before the dropped bash child was reaped"
    );

    gate.release();
    tokio::time::timeout(WIRE_TIMEOUT, cleanup)
        .await
        .expect("task cleanup did not resume after release")
        .expect("bash cleanup failed");
    assert_eq!(
        unsafe { libc::kill(pid, 0) },
        -1,
        "bash child remained after cleanup"
    );
}

#[tokio::test]
async fn earlier_cleanup_error_does_not_skip_bash_reap() {
    let directory = ContextDir::new();
    let reaper = Arc::new(Reaper::default());
    let mut registry = ToolRegistry::new();
    registry.add_task_cleanup(|| {
        Box::pin(async {
            Err::<(), ToolError>(ToolError::ExecutionFailed(
                "intentional cleanup failure".to_owned(),
            ))
        })
    });
    register_with_reaper(&mut registry, directory.context.clone(), reaper.clone());
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();

    let (pid, execution, _handshake) = start_blocked_bash(&registry, &listener).await;
    let (started, resume) = reaper.hold_next_reap();
    let mut gate = ReapGate {
        resume: Some(resume),
        pid,
    };

    drop(execution);
    wait_for_cleanup_start(started).await;

    let mut cleanup = Box::pin(registry.finish_task());
    assert!(
        futures_util::poll!(cleanup.as_mut()).is_pending(),
        "an earlier cleanup error short-circuited the bash reaper"
    );

    gate.release();
    let error = tokio::time::timeout(WIRE_TIMEOUT, cleanup)
        .await
        .expect("task cleanup did not resume after release")
        .expect_err("the intentional cleanup failure must be retained");
    assert!(
        error.to_string().contains("intentional cleanup failure"),
        "original cleanup error was lost: {error}"
    );
    assert_eq!(
        unsafe { libc::kill(pid, 0) },
        -1,
        "bash child remained after a prior cleanup error"
    );
}

// The cancellation branch must re-enter task cleanup on this same runtime
// before the reaper gate is released. current_thread makes a missing cleanup
// barrier deterministic: the agent can finish and enqueue its terminal before
// the test observes the gate. No yield/sleep is used.
//
// Exercise a live child and an exited-but-unreaped child. WNOWAIT forces
// the latter state without letting the agent poll its wait before cancellation.
#[tokio::test(flavor = "current_thread")]
async fn agent_cancellation_waits_for_bash_reap_before_one_interrupted_terminal() {
    for child_exited_before_cancel in [false, true] {
        let directory = ContextDir::new();
        let reaper = Arc::new(Reaper::default());
        let mut registry = ToolRegistry::new();
        register_with_reaper(&mut registry, directory.context.clone(), reaper.clone());
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let command = blocking_command(listener.local_addr().unwrap().port());
        let response = json!({
            "id":"bash-response",
            "status":"completed",
            "output":[{
                "type":"function_call",
                "status":"completed",
                "id":"bash-item",
                "call_id":"bash-call",
                "name":"bash",
                "arguments":serde_json::to_string(&json!({"command":command})).unwrap()
            }]
        });
        let (client, server) = scripted_http(vec![(200, response)]).await;
        let (event_tx, mut events) = mpsc::unbounded_channel();
        let (cancel_tx, cancel_rx) = watch::channel(false);
        let (input_tx, input_rx) = mpsc::unbounded_channel();
        let agent = Agent::new(client, registry, Conversation::new("system".to_owned()))
            .with_model("test".to_owned())
            .with_events(event_tx)
            .with_cancel(cancel_rx);
        let mut task = tokio::spawn(run_agent(agent, input_rx));
        input_tx
            .send(UserMessage::text("run the blocked bash"))
            .unwrap();
        drop(input_tx);

        let (socket, _) = tokio::select! {
            result = listener.accept() => result.expect("accept bash handshake"),
            result = &mut task => panic!("agent finished before its bash handshake: {result:?}"),
        };
        let mut reader = BufReader::new(socket);
        let mut line = String::new();
        tokio::time::timeout(WIRE_TIMEOUT, reader.read_line(&mut line))
            .await
            .expect("PID handshake timed out")
            .expect("read PID handshake");
        let pid: i32 = line.trim().parse().expect("numeric PID");

        let (started, resume) = reaper.hold_next_reap();
        let mut gate = ReapGate {
            resume: Some(resume),
            pid,
        };
        if child_exited_before_cancel {
            // Keep Tokio's Child unpolled while making its OS exit observable.
            unsafe {
                assert_eq!(libc::kill(pid, libc::SIGKILL), 0, "kill blocked bash child");
            }
            wait_for_exit_without_reaping(pid);
        }
        cancel_tx.send(true).unwrap();
        wait_for_cleanup_start(started).await;

        while let Ok(event) = events.try_recv() {
            assert!(
                !matches!(
                    event,
                    AgentEvent::TurnCompleted
                        | AgentEvent::TurnInterrupted
                        | AgentEvent::TurnFailed { .. }
                ),
                "terminal event escaped before bash reap: {event:?}"
            );
        }
        // On this runtime the agent has already yielded to the gated reaper.
        // Its join handle must remain pending until cleanup is released.
        assert!(
            futures_util::poll!(&mut task).is_pending(),
            "agent returned before the bash reaper was released"
        );

        gate.release();
        tokio::time::timeout(WIRE_TIMEOUT, &mut task)
            .await
            .expect("agent did not finish after reap release")
            .expect("agent task panicked");
        assert_eq!(
            unsafe { libc::kill(pid, 0) },
            -1,
            "bash child remained after agent termination"
        );

        let mut terminals = Vec::new();
        loop {
            match tokio::time::timeout(WIRE_TIMEOUT, events.recv()).await {
                Ok(Some(event)) => {
                    if matches!(
                        event,
                        AgentEvent::TurnCompleted
                            | AgentEvent::TurnInterrupted
                            | AgentEvent::TurnFailed { .. }
                    ) {
                        terminals.push(event);
                    }
                }
                Ok(None) => break,
                Err(_) => panic!("agent event channel did not close"),
            }
        }
        assert_eq!(terminals, vec![AgentEvent::TurnInterrupted]);
        assert_eq!(requests(server).await.len(), 1);
    }
}
