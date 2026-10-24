use super::*;
use rmcp::{
    RoleClient,
    model::{
        CancelledNotification, CancelledNotificationParam, ClientJsonRpcMessage,
        ClientNotification, ClientRequest, ClientResult, CustomNotification, ErrorData,
        JsonRpcMessage, RequestId, ServerJsonRpcMessage, SubscriptionFilter,
        SubscriptionsListenRequest, SubscriptionsListenRequestParams,
    },
    transport::{Transport, async_rw::AsyncRwTransport},
};
use serde_json::{Value, json};
use std::{
    future::Future,
    io,
    pin::Pin,
    sync::Arc,
    task::{Context, Waker},
    time::Duration,
};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader, DuplexStream, ReadHalf, WriteHalf},
    time::timeout,
};

type TestTransport =
    BoundedTransport<AsyncRwTransport<RoleClient, ReadHalf<DuplexStream>, WriteHalf<DuplexStream>>>;

#[tokio::test]
async fn subscription_end_invalidates_before_delivery_and_old_ids_cannot_end_replacement() {
    let (mut transport, _reader, mut writer, changes) = observed_pair(false);
    transport.send(listen_request(11)).await.unwrap();
    assert!(changes.has_subscription());
    write_frame(
        &mut writer,
        json!({"jsonrpc":"2.0","id":11,"error":{"code":-32603,"message":"subscription ended"}}),
    )
    .await;
    assert!(matches!(
        receive(&mut transport).await,
        Some(JsonRpcMessage::Error(_))
    ));
    assert!(!changes.has_subscription());
    assert_eq!(changes.revision(), 1);
    transport.send(listen_request(12)).await.unwrap();
    write_frame(
        &mut writer,
        json!({"jsonrpc":"2.0","id":11,"error":{"code":-32603,"message":"late old end"}}),
    )
    .await;
    receive(&mut transport).await.unwrap();
    assert!(changes.has_subscription());
    write_frame(&mut writer, notification_with_subscription(Some(11))).await;
    receive(&mut transport).await.unwrap();
    assert_eq!(changes.revision(), 1);
    write_frame(&mut writer, notification_with_subscription(Some(12))).await;
    receive(&mut transport).await.unwrap();
    assert_eq!(changes.revision(), 2);
}

fn pair() -> (
    TestTransport,
    BufReader<ReadHalf<DuplexStream>>,
    WriteHalf<DuplexStream>,
) {
    let (client, server) = tokio::io::duplex(1024 * 1024);
    let (client_read, client_write) = tokio::io::split(client);
    let (server_read, server_write) = tokio::io::split(server);
    (
        BoundedTransport::new(AsyncRwTransport::<RoleClient, _, _>::new(
            client_read,
            client_write,
        )),
        BufReader::new(server_read),
        server_write,
    )
}

fn observed_pair(
    legacy: bool,
) -> (
    TestTransport,
    BufReader<ReadHalf<DuplexStream>>,
    WriteHalf<DuplexStream>,
    Arc<CatalogChanges>,
) {
    let (transport, reader, writer) = pair();
    let changes = Arc::new(CatalogChanges::new(legacy));
    (
        transport.with_catalog_changes(changes.clone()),
        reader,
        writer,
        changes,
    )
}

fn listen_request(id: i64) -> ClientJsonRpcMessage {
    ClientJsonRpcMessage::request(
        ClientRequest::SubscriptionsListenRequest(SubscriptionsListenRequest::new(
            SubscriptionsListenRequestParams::new(
                SubscriptionFilter::builder().tools_list_changed().build(),
            ),
        )),
        RequestId::Number(id),
    )
}

fn cancel(id: i64) -> ClientJsonRpcMessage {
    ClientJsonRpcMessage::notification(ClientNotification::CancelledNotification(
        CancelledNotification::new(CancelledNotificationParam::new(
            Some(RequestId::Number(id)),
            None,
        )),
    ))
}

fn notification_with_subscription(id: Option<i64>) -> Value {
    match id {
        Some(id) => json!({
            "jsonrpc": "2.0",
            "method": "notifications/tools/list_changed",
            "params": {
                "_meta": {"io.modelcontextprotocol/subscriptionId": id}
            }
        }),
        None => notification(),
    }
}

fn tools_response(id: i64) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "result": {"tools": []}})
}

async fn write_frame(writer: &mut WriteHalf<DuplexStream>, value: Value) {
    let mut bytes = serde_json::to_vec(&value).unwrap();
    bytes.push(b'\n');
    writer.write_all(&bytes).await.unwrap();
    writer.flush().await.unwrap();
}

fn request(id: i64) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "method": "ping"})
}

fn notification() -> Value {
    json!({"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
}

async fn receive(transport: &mut TestTransport) -> Option<ServerJsonRpcMessage> {
    timeout(Duration::from_secs(2), transport.receive())
        .await
        .expect("receive timed out")
}

fn assert_pending<F: Future>(future: Pin<&mut F>) {
    let mut context = Context::from_waker(Waker::noop());
    assert!(future.poll(&mut context).is_pending());
}

fn request_id(message: ServerJsonRpcMessage) -> RequestId {
    message.into_request().expect("expected a request").1
}

async fn send_response(transport: &mut TestTransport, id: i64) {
    transport
        .send(ClientJsonRpcMessage::response(
            ClientResult::empty(()),
            RequestId::Number(id),
        ))
        .await
        .unwrap();
}

#[tokio::test]
async fn modern_catalog_changes_require_current_subscription_before_response() {
    let (mut transport, _reader, mut writer, changes) = observed_pair(false);
    transport.send(listen_request(7)).await.unwrap();

    write_frame(&mut writer, notification_with_subscription(Some(6))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 0);

    write_frame(&mut writer, notification()).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 0);

    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    write_frame(&mut writer, tools_response(99)).await;
    let changed = receive(&mut transport).await.expect("tool notification");
    assert!(matches!(&changed, JsonRpcMessage::Notification(_)));
    assert_eq!(changes.revision(), 1);
    let response = receive(&mut transport).await.expect("tools response");
    assert!(matches!(&response, JsonRpcMessage::Response(_)));
    assert_eq!(changes.revision(), 1);

    changes.clear_subscription(&RequestId::Number(6));
    transport.send(listen_request(8)).await.unwrap();
    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 1);

    write_frame(&mut writer, notification_with_subscription(Some(8))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 2);

    changes.clear_subscription(&RequestId::Number(7));
    write_frame(&mut writer, notification_with_subscription(Some(8))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 3);

    changes.clear_subscription(&RequestId::Number(8));
    write_frame(&mut writer, notification_with_subscription(Some(8))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 3);
}

#[tokio::test]
async fn close_clears_current_catalog_subscription() {
    let (mut first, _reader, _writer, changes) = observed_pair(false);
    first.send(listen_request(7)).await.unwrap();
    first.close().await.unwrap();

    let (mut second, _reader, mut writer) = pair();
    second = second.with_catalog_changes(changes.clone());
    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    assert!(receive(&mut second).await.is_some());
    assert_eq!(changes.revision(), 0);
}

#[tokio::test]
async fn outbound_cancellation_clears_only_matching_subscription() {
    let (mut transport, _reader, mut writer, changes) = observed_pair(false);
    transport.send(listen_request(7)).await.unwrap();

    transport.send(cancel(6)).await.unwrap();
    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 1);

    transport.send(cancel(7)).await.unwrap();
    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 1);
}

#[tokio::test]
async fn legacy_catalog_changes_ignore_scoped_notifications() {
    let (mut transport, _reader, mut writer, changes) = observed_pair(true);

    write_frame(&mut writer, notification()).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 1);

    write_frame(&mut writer, notification_with_subscription(Some(7))).await;
    assert!(receive(&mut transport).await.is_some());
    assert_eq!(changes.revision(), 1);
}

#[tokio::test]
async fn cancelled_receive_counts_pending_notification_once() {
    let (mut transport, _reader, mut writer, changes) = observed_pair(true);
    for _ in 0..=INBOUND_LIMIT {
        write_frame(&mut writer, notification()).await;
    }

    let mut held = Vec::new();
    for _ in 0..INBOUND_LIMIT {
        held.push(receive(&mut transport).await.expect("notification"));
    }
    assert_eq!(changes.revision(), INBOUND_LIMIT as u64);

    let mut ninth = Box::pin(transport.receive());
    assert_pending(ninth.as_mut());
    drop(ninth);
    assert_eq!(changes.revision(), INBOUND_LIMIT as u64);

    drop(held.remove(0));
    let ninth = receive(&mut transport).await.expect("ninth notification");
    assert!(matches!(ninth, JsonRpcMessage::Notification(_)));
    assert_eq!(changes.revision(), (INBOUND_LIMIT + 1) as u64);
}

#[tokio::test]
async fn ninth_request_waits_and_response_releases_one_permit() {
    let (mut transport, _reader, mut writer) = pair();
    for id in 1..=9 {
        write_frame(&mut writer, request(id)).await;
    }
    for _ in 0..INBOUND_LIMIT {
        assert!(receive(&mut transport).await.is_some());
    }

    let mut ninth = Box::pin(transport.receive());
    assert_pending(ninth.as_mut());
    drop(ninth);

    send_response(&mut transport, 1).await;
    let ninth = receive(&mut transport).await.expect("ninth request");
    assert_eq!(request_id(ninth), RequestId::Number(9));
}

#[tokio::test]
async fn error_releases_a_request_permit() {
    let (mut transport, _reader, mut writer) = pair();
    for id in 1..=9 {
        write_frame(&mut writer, request(id)).await;
    }
    for _ in 0..INBOUND_LIMIT {
        assert!(receive(&mut transport).await.is_some());
    }

    let mut ninth = Box::pin(transport.receive());
    assert_pending(ninth.as_mut());
    drop(ninth);

    transport
        .send(ClientJsonRpcMessage::error(
            ErrorData::internal_error("fixture error", None),
            Some(RequestId::Number(1)),
        ))
        .await
        .unwrap();
    let ninth = receive(&mut transport).await.expect("ninth request");
    assert_eq!(request_id(ninth), RequestId::Number(9));
}

#[tokio::test]
async fn notification_permits_are_held_until_message_is_dropped() {
    let (mut transport, _reader, mut writer) = pair();
    for _ in 0..=INBOUND_LIMIT {
        write_frame(&mut writer, notification()).await;
    }
    let mut held = Vec::new();
    for _ in 0..INBOUND_LIMIT {
        let message = receive(&mut transport).await.expect("notification");
        assert!(matches!(&message, JsonRpcMessage::Notification(_)));
        held.push(message);
    }

    let mut ninth = Box::pin(transport.receive());
    assert_pending(ninth.as_mut());
    drop(ninth);

    drop(held.remove(0));
    let ninth = receive(&mut transport).await.expect("ninth notification");
    assert!(matches!(ninth, JsonRpcMessage::Notification(_)));
}

#[tokio::test]
async fn oversized_outgoing_frame_is_not_written() {
    let (mut transport, mut reader, _writer) = pair();
    let huge = "x".repeat(FRAME_MAX_BYTES + 1);
    let error = transport
        .send(ClientJsonRpcMessage::notification(
            ClientNotification::CustomNotification(CustomNotification::new(huge, None)),
        ))
        .await
        .unwrap_err();
    assert_eq!(error.kind(), io::ErrorKind::InvalidData);

    let mut line = String::new();
    assert!(
        timeout(Duration::from_millis(100), reader.read_line(&mut line))
            .await
            .is_err(),
        "oversized frame must not write bytes"
    );

    transport
        .send(ClientJsonRpcMessage::notification(
            ClientNotification::CustomNotification(CustomNotification::new("small-method", None)),
        ))
        .await
        .unwrap();
    let read = timeout(Duration::from_secs(2), reader.read_line(&mut line))
        .await
        .expect("small frame timed out")
        .expect("small frame write");
    assert!(read > 0);
    assert!(line.contains("small-method"));
}

#[tokio::test]
async fn close_releases_pending_request_permits() {
    let (mut transport, _reader, mut writer) = pair();
    for id in 1..=INBOUND_LIMIT as i64 {
        write_frame(&mut writer, request(id)).await;
    }
    for _ in 0..INBOUND_LIMIT {
        assert!(receive(&mut transport).await.is_some());
    }
    assert_eq!(
        transport.response_permits.lock().unwrap().len(),
        INBOUND_LIMIT
    );

    transport.close().await.unwrap();

    assert!(transport.response_permits.lock().unwrap().is_empty());
    assert_eq!(transport.inbound.available_permits(), INBOUND_LIMIT);
}

#[tokio::test]
async fn duplicate_inbound_request_id_ends_transport() {
    let (mut transport, _reader, mut writer) = pair();
    write_frame(&mut writer, request(1)).await;
    assert!(receive(&mut transport).await.is_some());

    write_frame(&mut writer, request(1)).await;
    assert!(receive(&mut transport).await.is_none());
    assert!(transport.response_permits.lock().unwrap().is_empty());
}
