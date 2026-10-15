//! Process-level regression for the application startup session policy.
//!
//! Plain startup must create a fresh session; stored history is restored only
//! through an explicit `SAKI_SESSION=<id>` (the `/resume` and picker paths are
//! covered by their own tests). The child test calls the real `app::run` on the
//! same test executable, so this exercises environment parsing, store creation
//! and front-end assembly without a second copy of the selection policy.

use std::{
    fs,
    net::TcpListener,
    path::{Path, PathBuf},
    process::{Output, Stdio},
    time::Duration,
};

use tokio::process::Command;

use crate::{
    agent::conversation::CompactionConfig,
    session::{SYSTEM_PROMPT, store::SessionStore},
};

const CHILD_TEST: &str = "frontend::console::tests::startup::startup_session_child";
const CHILD_TIMEOUT: Duration = Duration::from_secs(20);
const SEED_MARKER: &str = "seed-old-history-marker";

struct StartupWorkspace {
    root: PathBuf,
}

impl StartupWorkspace {
    fn new() -> Self {
        static NEXT: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let root = std::env::temp_dir().join(format!(
            "saki-startup-session-{}-{}",
            crate::agent::journal::new_id().unwrap(),
            NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
        fs::create_dir_all(root.join("workspace")).unwrap();
        Self { root }
    }

    fn workspace(&self) -> PathBuf {
        self.root.join("workspace")
    }

    fn store(&self) -> SessionStore {
        SessionStore::new(self.root.join("sessions")).unwrap()
    }
}

impl Drop for StartupWorkspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

/// Creates a supported session in the workspace and saves one user message, so
/// an implicit restore would be visible.
fn seed_supported_history(store: &SessionStore, workspace: &Path) -> String {
    let id = store
        .create(
            workspace,
            "startup-test-model",
            SYSTEM_PROMPT,
            CompactionConfig::new(262_144),
        )
        .unwrap();
    let mut loaded = store.open(&id).unwrap();
    loaded.conversation.add_user_message(SEED_MARKER);
    loaded.conversation.save_history().unwrap();
    id
}

/// Runs the real application as a child of this test executable with the
/// console front end, an empty stdin (immediate EOF) and an unreachable model
/// URL. `session = None` leaves `SAKI_SESSION` unset. A wedged child is killed
/// by `kill_on_drop` when the bounded wait expires.
async fn run_app(workspace: &StartupWorkspace, session: Option<&str>, base_url: &str) -> Output {
    let mut command = Command::new(std::env::current_exe().unwrap());
    command
        .kill_on_drop(true)
        .args(["--ignored", "--exact", "--nocapture", CHILD_TEST])
        .current_dir(workspace.workspace())
        .env("SAKI_UI", "console")
        .env("SAKI_SESSION_DIR", workspace.root.join("sessions"))
        .env("SAKI_CONTEXT_TOKENS", "262144")
        .env("SAKI_MODEL", "startup-test-model")
        .env("API_KEY", "dummy-api-key")
        .env("SAKI_BASE_URL", base_url)
        .env_remove("SAKI_SESSION")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if let Some(id) = session {
        command.env("SAKI_SESSION", id);
    }
    let child = command.spawn().expect("spawn startup child");
    match tokio::time::timeout(CHILD_TIMEOUT, child.wait_with_output()).await {
        Ok(Ok(output)) => output,
        Ok(Err(error)) => panic!("startup child I/O failed: {error}"),
        Err(_) => panic!("startup child exceeded {CHILD_TIMEOUT:?} and was killed"),
    }
}

fn combined(output: &Output) -> String {
    format!(
        "status={:?}\nstdout:\n{}\nstderr:\n{}",
        output.status,
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    )
}

fn would_block(listener: &TcpListener) -> bool {
    match listener.accept() {
        Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => true,
        Err(_) => false,
        Ok((_stream, _address)) => false,
    }
}

#[tokio::test]
async fn plain_startup_creates_fresh_sessions_and_explicit_ids_still_restore() {
    let workspace = StartupWorkspace::new();
    let store = workspace.store();
    let seeded = seed_supported_history(&store, &workspace.workspace());
    let seeded_history = fs::read(store.directory(&seeded).unwrap().join("history.jsonl")).unwrap();

    // Any connection to this listener would mean a model call was attempted.
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let base_url = format!("http://{}/v1", listener.local_addr().unwrap());

    // Two plain startups must each create a distinct fresh session and must
    // neither print nor store the old transcript.
    for attempt in 1..=2 {
        let output = run_app(&workspace, None, &base_url).await;
        assert!(
            output.status.success(),
            "plain startup {attempt} failed: {}",
            combined(&output)
        );
        assert!(
            !combined(&output).contains(SEED_MARKER),
            "plain startup {attempt} printed stored history: {}",
            combined(&output)
        );
    }
    let sessions = store.list().unwrap();
    assert_eq!(
        sessions.len(),
        3,
        "plain startups must each create a session"
    );
    let mut new_ids: Vec<String> = sessions
        .iter()
        .map(|session| session.metadata.id.clone())
        .filter(|id| *id != seeded)
        .collect();
    new_ids.sort();
    assert_eq!(new_ids.len(), 2);
    assert_ne!(new_ids[0], new_ids[1], "each startup gets its own session");

    for id in &new_ids {
        let loaded = store.open(id).unwrap();
        assert_eq!(
            loaded.replay.context.len(),
            1,
            "a fresh session starts with only the system message"
        );
        let journal =
            fs::read_to_string(store.directory(id).unwrap().join("history.jsonl")).unwrap();
        assert!(!journal.contains(SEED_MARKER));
        assert!(!journal.contains("task_started"), "startup must not send");
    }

    // SAKI_SESSION=new is the explicit form of the same fresh-session contract.
    let output = run_app(&workspace, Some("new"), &base_url).await;
    assert!(
        output.status.success(),
        "explicit new failed: {}",
        combined(&output)
    );
    assert!(
        !combined(&output).contains(SEED_MARKER),
        "explicit new printed stored history: {}",
        combined(&output)
    );
    let after_new = store.list().unwrap();
    assert_eq!(after_new.len(), 4, "explicit new must create one session");
    let explicit_new = after_new
        .iter()
        .map(|session| session.metadata.id.clone())
        .find(|id| *id != seeded && !new_ids.contains(id))
        .expect("explicit new must add a fresh session");
    assert_eq!(store.open(&explicit_new).unwrap().replay.context.len(), 1);

    // An explicit ID still opens the exact stored history without a new session.
    let output = run_app(&workspace, Some(&seeded), &base_url).await;
    assert!(
        output.status.success(),
        "explicit startup failed: {}",
        combined(&output)
    );
    assert!(
        combined(&output).contains(SEED_MARKER),
        "explicit startup did not restore the stored transcript: {}",
        combined(&output)
    );
    assert_eq!(store.list().unwrap().len(), 4);

    // A missing ID fails as before and must not silently create or fall back.
    let output = run_app(&workspace, Some("missing-session-id"), &base_url).await;
    assert!(
        !output.status.success(),
        "missing id was accepted: {}",
        combined(&output)
    );
    assert_eq!(store.list().unwrap().len(), 4);

    // A locked ID fails as before and must not silently create or fall back.
    let held = store.open(&seeded).unwrap();
    let output = run_app(&workspace, Some(&seeded), &base_url).await;
    assert!(
        !output.status.success(),
        "locked id was accepted: {}",
        combined(&output)
    );
    assert!(
        combined(&output).contains("already in use"),
        "unexpected locked-id error: {}",
        combined(&output)
    );
    assert_eq!(store.list().unwrap().len(), 4);
    drop(held);

    assert_eq!(
        fs::read(store.directory(&seeded).unwrap().join("history.jsonl")).unwrap(),
        seeded_history,
        "stored history changed during startup-only runs"
    );
    assert!(
        would_block(&listener),
        "a startup attempted a model connection"
    );
}

#[tokio::test]
#[ignore = "startup child: invoked by plain_startup_creates_fresh_sessions_and_explicit_ids_still_restore"]
async fn startup_session_child() {
    match crate::app::run().await {
        Ok(()) => println!("startup-result: ok"),
        Err(error) => {
            println!("startup-result: error: {error:#}");
            panic!("startup failed: {error:#}");
        }
    }
}
