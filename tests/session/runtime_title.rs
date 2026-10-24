use super::*;
use crate::session::store;
use tokio::net::TcpStream;

async fn respond(socket: &mut TcpStream, text: &str) {
    let body = serde_json::to_vec(&completed(text)).unwrap();
    socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",body.len()).as_bytes()).await.unwrap();
    socket.write_all(&body).await.unwrap();
}

async fn gated_title(
    additional_user: bool,
) -> (
    ModelClient,
    oneshot::Receiver<()>,
    oneshot::Sender<()>,
    JoinHandle<Vec<Value>>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let client = ModelClient::for_test(format!("http://{}/v1", listener.local_addr().unwrap()));
    let (arrived_tx, arrived) = oneshot::channel();
    let (release, release_rx) = oneshot::channel();
    let server = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let main = read_request(&mut socket).await;
        respond(&mut socket, "first visible reply").await;
        let (mut socket, _) = listener.accept().await.unwrap();
        let title = read_request(&mut socket).await;
        arrived_tx.send(()).unwrap();
        let pending = tokio::spawn(async move {
            release_rx.await.unwrap();
            respond(&mut socket, "Generated title").await;
        });
        let mut requests = vec![main, title];
        if additional_user {
            let (mut socket, _) = listener.accept().await.unwrap();
            requests.push(read_request(&mut socket).await);
            respond(&mut socket, "second user reply").await;
        }
        pending.await.unwrap();
        requests
    });
    (client, arrived, release, server)
}

async fn send(handle: &RuntimeHandle, id: &str, text: &str) -> TaskFinished {
    let task = accepted(
        handle
            .call(RuntimeCommand::Send {
                session_id: id.into(),
                message: UserMessage::text(text),
            })
            .await
            .unwrap(),
    );
    tokio::time::timeout(Duration::from_secs(5), finish(task.events))
        .await
        .unwrap()
}

#[tokio::test]
async fn service_title_job_is_optional_persisted_once_and_has_no_task_history() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![
        (200, completed("main result")),
        (200, completed("Generated title")),
        (200, completed("later response")),
    ])
    .await;
    let (handle, actor) = runtime(client, &workspace)
        .with_automatic_titles(true)
        .start();
    let mut updates = handle.subscribe_metadata();
    let id = create_session(&handle).await;
    assert_eq!(
        send(&handle, &id, "first user input").await.status,
        "completed"
    );
    let update = tokio::time::timeout(Duration::from_secs(5), updates.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(update.session_id, id);
    let state = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(state.metadata.as_ref().unwrap().title, "Generated title");
    assert_eq!(
        state.metadata.as_ref().unwrap().title_source.as_deref(),
        Some("auto")
    );
    assert_eq!(
        send(&handle, &id, "second user input").await.status,
        "completed"
    );
    shutdown_actor(handle, actor).await;
    let requests = crate::test_support::requests(server).await;
    assert_eq!(requests.len(), 3);
    assert_eq!(requests[1]["max_output_tokens"], 1024);
    assert!(
        requests[1].get("tools").is_none(),
        "title job must not expose business tools"
    );
    let title_input = requests[1]["input"].to_string();
    assert!(title_input.contains("first user input"));
    assert!(title_input.contains("main result"));
    let records = workspace.store().open(&id).unwrap().replay.history;
    assert_eq!(
        records
            .iter()
            .filter(|record| matches!(record, JournalRecord::TaskStarted { .. }))
            .count(),
        2
    );
    assert!(
        !serde_json::to_string(&records)
            .unwrap()
            .contains("Generated title")
    );
}

#[tokio::test]
async fn manual_rename_wins_while_title_job_runs_and_shutdown_waits_for_cleanup() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = gated_title(false).await;
    let (handle, actor) = runtime(client, &workspace)
        .with_automatic_titles(true)
        .start();
    let mut updates = handle.subscribe_metadata();
    let id = create_session(&handle).await;
    assert_eq!(
        send(&handle, &id, "original user input").await.status,
        "completed"
    );
    tokio::time::timeout(Duration::from_secs(5), arrived)
        .await
        .unwrap()
        .unwrap();
    let original = fs::read(journal_file(&workspace, &id)).unwrap();
    handle
        .call(RuntimeCommand::Rename {
            session_id: id.clone(),
            title: "My manual title".into(),
        })
        .await
        .unwrap();
    handle.call(RuntimeCommand::Shutdown).await.unwrap();
    release.send(()).unwrap();
    tokio::time::timeout(Duration::from_secs(5), actor)
        .await
        .unwrap()
        .unwrap()
        .unwrap();
    let metadata = workspace.store().metadata(&id).unwrap();
    assert_eq!(metadata.title, "My manual title");
    assert_eq!(metadata.title_source.as_deref(), Some("manual"));
    assert!(matches!(
        updates.try_recv(),
        Err(broadcast::error::TryRecvError::Empty)
    ));
    assert_eq!(fs::read(journal_file(&workspace, &id)).unwrap(), original);
    let requests = crate::test_support::requests(server).await;
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[1]["model"], "test");
}

#[tokio::test]
async fn occupied_auxiliary_slot_does_not_consume_user_capacity_or_queue_title_jobs() {
    let workspace = Workspace::new();
    let (client, arrived, release, server) = gated_title(true).await;
    let (handle, actor) = runtime(client, &workspace)
        .with_automatic_titles(true)
        .start();
    let mut updates = handle.subscribe_metadata();
    let a = create_session(&handle).await;
    let b = create_session(&handle).await;
    assert_eq!(send(&handle, &a, "A first").await.status, "completed");
    tokio::time::timeout(Duration::from_secs(5), arrived)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(
        send(&handle, &b, "B while A title is held").await.status,
        "completed"
    );
    let capabilities = match handle.call(RuntimeCommand::Capabilities).await.unwrap() {
        RuntimeReply::Capabilities(value) => value,
        _ => panic!("expected capabilities"),
    };
    assert_eq!(capabilities.max_active_tasks, 1);
    assert_eq!(capabilities.max_auxiliary_jobs, 1);
    assert!(capabilities.automatic_titles);
    assert_eq!(
        workspace
            .store()
            .metadata(&b)
            .unwrap()
            .title_source
            .as_deref(),
        Some("pending")
    );
    release.send(()).unwrap();
    let update = tokio::time::timeout(Duration::from_secs(5), updates.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(update.session_id, a);
    shutdown_actor(handle, actor).await;
    assert_eq!(
        workspace.store().metadata(&b).unwrap().title,
        super::super::super::store::DEFAULT_TITLE
    );
    assert_eq!(
        crate::test_support::requests(server).await.len(),
        3,
        "busy title should not enter a hidden queue"
    );
}

#[tokio::test]
async fn failed_title_is_not_retried_after_restart_and_next_user_turn_still_runs() {
    let workspace = Workspace::new();
    let (client, server) = scripted_http(vec![
        (200, completed("main result")),
        // A finite, parseable empty response is the observed title failure.
        (200, completed("")),
        (200, completed("continued after title failure")),
    ])
    .await;

    let (handle, actor) = runtime(client.clone(), &workspace)
        .with_automatic_titles(true)
        .start();
    let id = create_session(&handle).await;
    assert_eq!(
        send(&handle, &id, "first user input").await.status,
        "completed"
    );
    shutdown_actor(handle, actor).await;

    let metadata = workspace.store().metadata(&id).unwrap();
    assert_eq!(metadata.title, store::DEFAULT_TITLE);
    assert_eq!(metadata.title_source.as_deref(), Some("auto"));

    let (handle, actor) = runtime(client, &workspace)
        .with_automatic_titles(true)
        .start();
    let loaded = session(
        handle
            .call(RuntimeCommand::Load {
                session_id: id.clone(),
            })
            .await
            .unwrap(),
    );
    assert_eq!(
        loaded.metadata.as_ref().unwrap().title_source.as_deref(),
        Some("auto")
    );
    assert_eq!(
        loaded.metadata.as_ref().unwrap().title,
        store::DEFAULT_TITLE
    );
    assert_eq!(
        send(&handle, &id, "continue after failure").await.status,
        "completed"
    );
    shutdown_actor(handle, actor).await;

    let requests = crate::test_support::requests(server).await;
    assert_eq!(requests.len(), 3, "title failure must not be retried");
    assert_eq!(requests[1]["max_output_tokens"], 1024);
    assert!(
        requests[1].get("tools").is_none(),
        "title request must not expose tools"
    );
    assert!(
        requests[1]["input"]
            .to_string()
            .contains("first user input")
    );
    assert!(
        requests[2]["input"]
            .to_string()
            .contains("continue after failure")
    );

    let history = workspace.store().open(&id).unwrap().replay.history;
    assert_eq!(
        history
            .iter()
            .filter(|record| matches!(record, JournalRecord::TaskStarted { .. }))
            .count(),
        2,
        "only the two user tasks may start; title work is not a task"
    );
    assert!(
        !serde_json::to_string(&history)
            .unwrap()
            .contains("Generated title")
    );
}
