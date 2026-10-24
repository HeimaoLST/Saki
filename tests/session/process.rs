use std::{fs, path::PathBuf, process::Stdio, time::Duration};

use serde_json::json;
use tokio::{
    io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader},
    net::TcpListener,
    process::{Child, ChildStdin, ChildStdout, Command},
};

use crate::{
    agent::journal::{JournalRecord, TaskStatus},
    session::store::SessionStore,
    test_support::{requests, scripted_server},
};

const CHILD_TEST: &str = "process_tests::host_entry_child";
const DEADLINE: Duration = Duration::from_secs(5);

struct Workspace(PathBuf);

impl Workspace {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "saki-host-process-{}",
            crate::agent::journal::new_id().unwrap()
        ));
        fs::create_dir(&root).unwrap();
        Self(root)
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.0.join("sessions")).unwrap()
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn spawn_host(
    workspace: &Workspace,
    base_url: &str,
) -> (Child, ChildStdin, BufReader<ChildStdout>) {
    let mut child = Command::new(std::env::current_exe().unwrap())
        .args(["--ignored", "--exact", "--nocapture", CHILD_TEST])
        .env_clear()
        .env("PATH", std::env::var_os("PATH").unwrap_or_default())
        .env("HOME", &workspace.0)
        .env("TMPDIR", &workspace.0)
        .env("SAKI_UI", "console")
        .env("SAKI_SESSION_DIR", workspace.0.join("sessions"))
        .env("SAKI_CONTEXT_TOKENS", "262144")
        .env("SAKI_MODEL", "process-fixture")
        .env("SAKI_API", "openai-responses")
        .env("SAKI_BASE_URL", base_url)
        .env("API_KEY", "offline-fixture")
        .current_dir(&workspace.0)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .unwrap();
    let stdin = child.stdin.take().unwrap();
    let output = BufReader::new(child.stdout.take().unwrap());
    (child, stdin, output)
}

async fn output_until(output: &mut BufReader<ChildStdout>, needle: &str) -> String {
    tokio::time::timeout(DEADLINE, async {
        let mut captured = String::new();
        loop {
            let mut line = String::new();
            assert_ne!(output.read_line(&mut line).await.unwrap(), 0, "{captured}");
            captured.push_str(&line);
            if captured.contains(needle) {
                return captured;
            }
        }
    })
    .await
    .expect("console output did not reach the expected barrier")
}

fn interrupt(child: &Child) {
    assert_eq!(
        unsafe { libc::kill(child.id().unwrap() as i32, libc::SIGINT) },
        0
    );
}

async fn exited(child: &mut Child) {
    match tokio::time::timeout(DEADLINE, child.wait()).await {
        Ok(status) => assert!(status.unwrap().success()),
        Err(_) => {
            child.kill().await.unwrap();
            let mut error = String::new();
            child
                .stderr
                .take()
                .unwrap()
                .read_to_string(&mut error)
                .await
                .unwrap();
            panic!("console failed to exit with stdin still open: {error}");
        }
    }
}

#[test]
#[ignore = "process fixture; invoked by the parent host lifecycle tests"]
fn host_entry_child() {
    crate::main().unwrap();
}

#[tokio::test]
async fn idle_sigint_exits_with_open_stdin_after_releasing_the_session() {
    let workspace = Workspace::new();
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let (mut child, stdin, mut output) = spawn_host(&workspace, &base);
    output_until(&mut output, "Session:").await;
    interrupt(&child);
    exited(&mut child).await;
    drop(stdin);

    let store = workspace.store();
    let sessions = store.list().unwrap();
    assert_eq!(sessions.len(), 1);
    let loaded = store.open(&sessions[0].metadata.id).unwrap();
    assert!(loaded.replay.active_task.is_none());
    assert!(!loaded.replay.history.iter().any(|record| matches!(
        record,
        JournalRecord::TaskStarted { .. } | JournalRecord::TaskFinished { .. }
    )));
    assert_eq!(
        listener.accept().unwrap_err().kind(),
        std::io::ErrorKind::WouldBlock
    );
}

#[tokio::test]
async fn active_sigint_reaps_the_tool_persists_one_terminal_and_keeps_input_usable() {
    let workspace = Workspace::new();
    let control = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let script = format!(
        "import os,socket;s=socket.create_connection((\"127.0.0.1\",{}));s.sendall((str(os.getpid())+\"\\n\").encode());s.recv(1)",
        control.local_addr().unwrap().port()
    );
    let python = crate::tools::mcp::test_support::python_command();
    let command = format!("exec '{}' -c '{script}'", python.display());
    let response = json!({
        "id":"tool-response", "status":"completed", "output":[{
            "type":"function_call", "status":"completed", "id":"tool-item",
            "call_id":"blocked-tool", "name":"bash",
            "arguments":serde_json::to_string(&json!({"command":command})).unwrap()
        }]
    });
    let (base, server) = scripted_server(vec![(
        200,
        "application/json",
        serde_json::to_vec(&response).unwrap(),
    )])
    .await;
    let (mut child, mut stdin, mut output) = spawn_host(&workspace, &base);
    output_until(&mut output, "Session:").await;
    stdin
        .write_all(b"run the controlled blocking tool\n")
        .await
        .unwrap();
    let (socket, _) = tokio::time::timeout(DEADLINE, control.accept())
        .await
        .unwrap()
        .unwrap();
    let mut socket = BufReader::new(socket);
    let mut pid = String::new();
    tokio::time::timeout(DEADLINE, socket.read_line(&mut pid))
        .await
        .unwrap()
        .unwrap();
    let pid: i32 = pid.trim().parse().unwrap();
    assert_eq!(unsafe { libc::kill(pid, 0) }, 0);

    interrupt(&child);
    let cancelled = output_until(&mut output, "Task interrupted.").await;
    assert_eq!(cancelled.matches("Task interrupted.").count(), 1);
    assert_eq!(
        unsafe { libc::kill(pid, 0) },
        -1,
        "tool outlived its terminal"
    );
    stdin.write_all(b"/help\n").await.unwrap();
    output_until(&mut output, "Local commands:").await;
    interrupt(&child);
    exited(&mut child).await;
    drop(stdin);
    drop(socket);

    let store = workspace.store();
    let sessions = store.list().unwrap();
    assert_eq!(sessions.len(), 1);
    let loaded = store.open(&sessions[0].metadata.id).unwrap();
    let terminals: Vec<_> = loaded
        .replay
        .history
        .iter()
        .filter_map(|record| match record {
            JournalRecord::TaskFinished { status, .. } => Some(status),
            _ => None,
        })
        .collect();
    assert_eq!(terminals, vec![&TaskStatus::Interrupted]);
    assert!(loaded.replay.active_task.is_none());
    assert_eq!(requests(server).await.len(), 1);
}
