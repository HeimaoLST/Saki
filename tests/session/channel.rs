use super::*;
use crate::{
    agent::{
        AgentEvent,
        events::{EventLimits, EventSink},
    },
    session::{
        SessionEvent,
        runtime::{Association, RuntimeError},
    },
};
use std::{
    future::Future,
    pin::Pin,
    task::{Context, Poll, Waker},
    time::Duration,
};
use tokio::{sync::watch, time::timeout};

fn large_event() -> SessionEvent {
    SessionEvent::Error {
        operation: "test",
        error: RuntimeError::new("test", "x".repeat(300 * 1024)),
    }
}

fn poll_once<F: Future>(future: Pin<&mut F>) -> Poll<F::Output> {
    let mut context = Context::from_waker(Waker::noop());
    future.poll(&mut context)
}

fn fill_bytes(sender: &SessionEventSender) -> usize {
    let mut queued = 0;
    while sender.try_send(large_event()).is_ok() {
        queued += 1;
        assert!(queued < 32, "byte budget must fill before message capacity");
    }
    assert!(queued > 0, "at least one event should fit");
    assert!(
        !sender.state().is_closed(),
        "try_send full must not close the output"
    );
    queued
}

#[tokio::test]
async fn byte_budget_fills_before_message_capacity_and_releases_on_drop() {
    let (sender, mut receiver) = channel(OutputState::default());
    let queued = fill_bytes(&sender);
    assert_eq!(sender.buffer_stats().messages, queued);
    assert_eq!(
        sender.metrics().snapshot().bytes,
        sender.buffer_stats().bytes
    );

    let mut pending = Box::pin(sender.send(large_event()));
    assert!(poll_once(pending.as_mut()).is_pending());

    let packet = timeout(Duration::from_secs(1), receiver.recv())
        .await
        .expect("receiver timed out")
        .expect("queued packet");
    drop(packet);

    let result = timeout(Duration::from_secs(1), pending)
        .await
        .expect("pending send did not resume");
    result.expect("send should resume after a permit is released");

    drop(receiver);
    assert_eq!(sender.buffer_stats().bytes, 0);
    assert_eq!(sender.buffer_stats().messages, 0);
}

#[tokio::test]
async fn closing_output_releases_waiting_send_without_a_lease() {
    let (sender, receiver) = channel(OutputState::default());
    fill_bytes(&sender);

    let mut pending = Box::pin(sender.send(large_event()));
    assert!(poll_once(pending.as_mut()).is_pending());
    sender.state().close();

    let result = timeout(Duration::from_secs(1), pending)
        .await
        .expect("closed send did not return");
    assert!(result.is_err());
    assert_eq!(sender.buffer_stats().waiting_producers, 0);

    drop(receiver);
    assert_eq!(sender.buffer_stats().bytes, 0);
    assert_eq!(sender.buffer_stats().messages, 0);
}

#[tokio::test]
async fn enqueue_timeout_releases_wait_and_marks_output_closed() {
    let (sender, receiver) = channel(OutputState::default());
    fill_bytes(&sender);

    let mut pending = Box::pin(sender.send(large_event()));
    // Pause before the first poll so the enqueue deadline is created on the
    // virtual clock rather than racing a real deadline.
    tokio::time::pause();
    assert!(poll_once(pending.as_mut()).is_pending());
    tokio::time::advance(Duration::from_millis(30_001)).await;
    tokio::time::resume();

    let result = timeout(Duration::from_secs(1), pending)
        .await
        .expect("timed-out send did not return");
    assert!(result.is_err());
    assert!(sender.state().is_closed());
    assert_eq!(sender.buffer_stats().waiting_producers, 0);

    drop(receiver);
    assert_eq!(sender.buffer_stats().bytes, 0);
    assert_eq!(sender.buffer_stats().messages, 0);
}

#[tokio::test]
async fn send_task_holds_upstream_permit_until_frontend_packet_is_dropped() {
    let (sender, mut receiver) = channel(OutputState::default());
    let (_cancel_tx, cancel_rx) = watch::channel(false);
    let (sink, mut upstream) =
        EventSink::bounded(EventLimits::default(), cancel_rx, OutputState::default());
    let upstream_metrics = sink.metrics();
    let producer_sink = sink.clone();
    let producer = tokio::spawn(async move {
        producer_sink
            .send(AgentEvent::ResponseText {
                item_id: "item".to_owned(),
                text: "payload".to_owned(),
            })
            .await;
    });

    let packet = timeout(Duration::from_secs(1), upstream.recv())
        .await
        .expect("upstream packet timed out")
        .expect("upstream packet");
    timeout(Duration::from_secs(1), producer)
        .await
        .expect("producer timed out")
        .expect("producer");
    assert!(upstream_metrics.snapshot().bytes > 0);

    let association = Association {
        session_id: "session".to_owned(),
        task_id: "task".to_owned(),
        message_id: "message".to_owned(),
    };
    sender
        .send_task(association.clone(), packet)
        .await
        .expect("send_task");

    let packet = timeout(Duration::from_secs(1), receiver.recv())
        .await
        .expect("terminal packet timed out")
        .expect("terminal packet");
    assert_eq!(packet.request_id, None);
    assert_eq!(
        packet
            .association
            .as_ref()
            .map(|value| value.session_id.as_str()),
        Some("session")
    );
    match &packet.event {
        SessionEvent::Agent { session_id, event } => {
            assert_eq!(session_id, "session");
            assert!(matches!(
                event,
                AgentEvent::ResponseText { item_id, text }
                    if item_id == "item" && text == "payload"
            ));
        }
        _ => panic!("expected a task agent event"),
    }
    assert!(
        upstream_metrics.snapshot().bytes > 0,
        "upstream permit must survive outside EventSink"
    );

    drop(packet);
    assert_eq!(upstream_metrics.snapshot().bytes, 0);
}
