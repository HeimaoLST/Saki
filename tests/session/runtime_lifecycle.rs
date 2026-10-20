use super::*;
use crate::agent::journal::{JournalRecord, TaskStatus};
use crate::client::config::RESPONSES_API;
use crate::test_support::{completed, read_request, sse};
use serde_json::{Value, json};
use tokio::{io::AsyncWriteExt, net::TcpListener, sync::oneshot, task::JoinHandle};

/// Serves one Responses SSE request: one text delta, then a gate the test opens
/// only after the machine event was observed, then the completed response.
async fn gated_sse() -> (ModelClient, oneshot::Sender<()>, JoinHandle<Value>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let (release_tx, release_rx) = oneshot::channel();
    let worker = tokio::spawn(async move {
        let (mut socket, _) = listener.accept().await.unwrap();
        let request = read_request(&mut socket).await;
        socket
            .write_all(
                b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\n",
            )
            .await
            .unwrap();
        let delta = sse(json!({
            "type": "response.output_text.delta",
            "item_id": "message",
            "delta": "Hel"
        }));
        socket.write_all(delta.as_bytes()).await.unwrap();
        socket.flush().await.unwrap();
        // The response must not finish until the test has seen the delta.
        let _ = release_rx.await;
        let finished = sse(json!({
            "type": "response.completed",
            "response": completed("Hello")
        }));
        socket.write_all(finished.as_bytes()).await.unwrap();
        socket.write_all(b"data: [DONE]\n\n").await.unwrap();
        socket.flush().await.unwrap();
        let _ = socket.shutdown().await;
        request
    });
    (
        ModelClient::for_test(format!("http://{address}/v1")),
        release_tx,
        worker,
    )
}

#[tokio::test]
async fn streaming_delta_precedes_completion_and_same_item_is_replaced_by_final_text() {
    let directory = Directory::new();
    let (client, release, server) = gated_sse().await;
    let mut driver = Driver::new(&directory, client).await;
    let session_id = driver.create().await;
    let accepted = driver
        .call(
            "task.send",
            json!({"session_id":session_id,"message":{"text":"stream"}}),
        )
        .await;
    let task = accepted["result"].clone();

    // `call` may have read the delta while waiting for the response; check both
    // the already-seen rows and the live stream.
    let mut delta = driver
        .seen
        .iter()
        .find(|value| value["event"]["type"] == "response_text_delta")
        .cloned();
    while delta.is_none() {
        let value = driver.next().await;
        if value["event"]["type"] == "response_text_delta" {
            delta = Some(value);
        }
    }
    let delta = delta.expect("the delta must reach the machine client before completion");
    assert_eq!(delta["task_id"], task["task_id"]);
    assert_eq!(delta["event"]["item_id"], "message");
    assert_eq!(delta["event"]["delta"], "Hel");

    // Only now may the server send response.completed.
    release.send(()).unwrap();
    let finished = driver.finish(&task).await;
    assert_eq!(finished["event"]["status"], "completed");
    let seen = driver.shutdown().await;
    let events: Vec<_> = seen
        .iter()
        .filter(|value| value["type"] == "event" && value["task_id"] == task["task_id"])
        .collect();

    assert_eq!(
        events
            .iter()
            .filter(|value| value["event"]["type"] == "task_finished")
            .count(),
        1,
        "exactly one terminal event"
    );
    assert!(events.iter().any(|value| {
        value["event"]["type"] == "response_text_delta"
            && value["event"]["item_id"] == "message"
            && value["event"]["delta"] == "Hel"
    }));
    let final_text = events
        .iter()
        .find(|value| value["event"]["type"] == "response_text")
        .expect("final text must be emitted");
    assert_eq!(final_text["event"]["item_id"], "message");
    assert_eq!(
        final_text["event"]["text"], "Hello",
        "the final text replaces the same item rather than appending a second item"
    );
    assert_eq!(
        events
            .iter()
            .filter(|value| {
                value["event"]["type"] == "response_text" && value["event"]["item_id"] == "message"
            })
            .count(),
        1
    );

    let request = server.await.unwrap();
    assert_eq!(request["stream"], true);
}

#[tokio::test]
async fn stdout_disconnect_drains_accepted_task_without_writing_cancel() {
    let directory = Directory::new();
    let (client, arrived, release, server) = paused_model().await;
    let mut driver = Driver::new(&directory, client).await;
    let session_id = driver.create().await;
    let accepted = driver
        .call(
            "task.send",
            json!({"session_id":session_id,"message":{"text":"finish"}}),
        )
        .await;
    let task = accepted["result"].clone();
    arrived.await.unwrap();

    // Keep stdin open; only the output observer disappears. The runtime must
    // stop accepting new work, wait for the accepted task, then return the
    // writer error instead of treating it as a cancellation.
    let Driver {
        input,
        output,
        worker,
        ..
    } = driver;
    drop(output);
    release.send(()).unwrap();
    let run_result = worker.await.unwrap();
    assert!(
        run_result.is_err(),
        "a broken output must be returned by runtime.run"
    );
    let error = run_result.unwrap_err();
    assert!(
        error
            .chain()
            .any(|cause| cause.downcast_ref::<std::io::Error>().is_some()),
        "runtime.run should surface the output I/O error, got: {error:#}"
    );
    drop(input);
    server.await.unwrap();

    let store = SessionStore::new(directory.0.join("sessions")).unwrap();
    let loaded = store.open_for_api(&session_id, RESPONSES_API).unwrap();
    assert_eq!(loaded.replay.status, TaskStatus::Completed);
    assert!(loaded.replay.active_task.is_none());
    assert_eq!(
        loaded
            .replay
            .history
            .iter()
            .filter(|record| matches!(
                record,
                JournalRecord::TaskFinished {
                    status: TaskStatus::Completed,
                    ..
                }
            ))
            .count(),
        1
    );
    assert!(
        !loaded.replay.history.iter().any(|record| matches!(
            record,
            JournalRecord::TaskFinished {
                status: TaskStatus::Interrupted,
                ..
            }
        )),
        "the output disconnect must not write a cancellation"
    );
    assert!(loaded.replay.history.iter().any(|record| matches!(
        record,
        JournalRecord::TaskFinished { task_id, .. } if task_id == task["task_id"].as_str().unwrap()
    )));
}
