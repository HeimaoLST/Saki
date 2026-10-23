use super::*;
use std::{future::Future, pin::pin, task::Poll};

async fn assert_pending<F: Future>(mut future: std::pin::Pin<&mut F>) {
    std::future::poll_fn(|cx| {
        assert!(future.as_mut().poll(cx).is_pending());
        Poll::Ready(())
    })
    .await;
}

#[tokio::test]
async fn byte_permit_follows_dequeued_packet_and_cancel_releases_blocked_producer() {
    let event = || AgentEvent::TaskStarted {
        task_id: "task".into(),
        message_id: Some("message".into()),
    };
    let size = serde_json::to_vec(&event()).unwrap().len();
    let output = OutputState::default();
    let (cancel, cancellation) = watch::channel(false);
    let (sink, mut receiver) = EventSink::bounded(
        EventLimits {
            messages: 4,
            bytes: size,
            max_event_bytes: size,
        },
        cancellation,
        output.clone(),
    );
    sink.send(event()).await;
    let packet = receiver.recv().await.unwrap();
    let stats = sink.metrics().snapshot();
    assert_eq!(stats.bytes, size);
    assert_eq!(stats.messages, 1);
    assert_eq!(stats.peak_bytes, size);
    assert_eq!(stats.peak_messages, 1);
    // The queue is empty, but the adapter still owns this event's byte budget.
    let mut second = pin!(sink.send(event()));
    assert_pending(second.as_mut()).await;
    assert_eq!(sink.metrics().snapshot().waiting_producers, 1);
    cancel.send_replace(true);
    second.await;
    assert_eq!(sink.metrics().snapshot().waiting_producers, 0);
    assert!(sink.history_required());
    assert!(!output.is_closed());
    assert!(receiver.try_recv().is_err());
    drop(packet);
    assert_eq!(sink.metrics().snapshot().bytes, 0);
}

#[tokio::test]
async fn full_message_queue_stalls_producer_until_output_is_closed() {
    let output = OutputState::default();
    let (_, cancellation) = watch::channel(false);
    let (sink, mut receiver) = EventSink::bounded(
        EventLimits {
            messages: 1,
            bytes: 1024,
            max_event_bytes: 1024,
        },
        cancellation,
        output.clone(),
    );
    sink.send(AgentEvent::TurnCompleted).await;
    let mut second = pin!(sink.send(AgentEvent::TurnCompleted));
    assert_pending(second.as_mut()).await;
    assert_eq!(sink.metrics().snapshot().waiting_producers, 1);
    output.close();
    second.await;
    assert_eq!(sink.metrics().snapshot().waiting_producers, 0);
    assert!(receiver.recv().await.is_some());
    assert!(receiver.try_recv().is_err());
}

#[tokio::test]
async fn oversized_event_closes_output_without_retaining_an_event() {
    let output = OutputState::default();
    let (_, cancellation) = watch::channel(false);
    let (sink, mut receiver) = EventSink::bounded(
        EventLimits {
            messages: 1,
            bytes: 16,
            max_event_bytes: 16,
        },
        cancellation,
        output.clone(),
    );
    sink.send(AgentEvent::TurnCompleted).await;
    assert!(output.is_closed());
    assert!(receiver.try_recv().is_err());
}

#[tokio::test]
async fn cancelling_the_enclosing_future_marks_the_projection_incomplete() {
    let output = OutputState::default();
    let (_, cancellation) = watch::channel(false);
    let (sink, _receiver) = EventSink::bounded(
        EventLimits {
            messages: 1,
            bytes: 1024,
            max_event_bytes: 1024,
        },
        cancellation,
        output,
    );
    sink.send(AgentEvent::TurnCompleted).await;
    let mut send = Box::pin(sink.send(AgentEvent::TurnCompleted));
    assert_pending(send.as_mut()).await;
    assert_eq!(sink.metrics().snapshot().waiting_producers, 1);
    drop(send);
    assert_eq!(sink.metrics().snapshot().waiting_producers, 0);
    assert!(sink.history_required());
}
