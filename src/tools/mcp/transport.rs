use std::{
    collections::HashMap,
    future::Future,
    io,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
};

use crate::protocol::ByteCounter;
use rmcp::{
    RoleClient,
    model::{
        ClientJsonRpcMessage, ClientNotification, ClientRequest, GetExtensions, GetMeta,
        JsonRpcMessage, RequestId, ServerJsonRpcMessage, ServerNotification,
    },
    transport::Transport,
};
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

pub(super) const FRAME_MAX_BYTES: usize = 4 * 1024 * 1024;
pub(super) const INBOUND_LIMIT: usize = 8;

pub(super) struct CatalogChanges {
    revision: AtomicU64,
    subscription: Mutex<Option<RequestId>>,
    legacy: bool,
}

impl CatalogChanges {
    pub(super) fn new(legacy: bool) -> Self {
        Self {
            revision: AtomicU64::new(0),
            subscription: Mutex::new(None),
            legacy,
        }
    }

    pub(super) fn revision(&self) -> u64 {
        self.revision.load(Ordering::SeqCst)
    }

    pub(super) fn invalidate(&self) {
        self.revision.fetch_add(1, Ordering::SeqCst);
    }

    pub(super) fn end_subscription(&self, id: &RequestId) {
        let mut current = self.subscription.lock().unwrap();
        if current.as_ref() == Some(id) {
            *current = None;
            self.invalidate();
        }
    }

    pub(super) fn has_subscription(&self) -> bool {
        self.subscription.lock().unwrap().is_some()
    }

    pub(super) fn clear_subscription(&self, id: &RequestId) {
        let mut current = self.subscription.lock().unwrap();
        if current.as_ref() == Some(id) {
            *current = None;
        }
    }

    fn set_subscription(&self, id: RequestId) {
        *self.subscription.lock().unwrap() = Some(id);
    }

    fn clear_current(&self) {
        *self.subscription.lock().unwrap() = None;
    }

    fn observe_tool_list_changed(&self, subscription: Option<RequestId>) {
        let current = self.subscription.lock().unwrap();
        let changed = if self.legacy {
            subscription.is_none()
        } else {
            subscription
                .as_ref()
                .is_some_and(|id| current.as_ref() == Some(id))
        };
        if changed {
            self.invalidate();
        }
    }
}

pub(super) struct BoundedTransport<T> {
    inner: T,
    pending: Option<ServerJsonRpcMessage>,
    inbound: Arc<Semaphore>,
    response_permits: Arc<Mutex<HashMap<RequestId, OwnedSemaphorePermit>>>,
    catalog_changes: Option<Arc<CatalogChanges>>,
}

impl<T> BoundedTransport<T> {
    pub(super) fn new(inner: T) -> Self {
        Self {
            inner,
            pending: None,
            inbound: Arc::new(Semaphore::new(INBOUND_LIMIT)),
            response_permits: Arc::new(Mutex::new(HashMap::new())),
            catalog_changes: None,
        }
    }

    pub(super) fn with_catalog_changes(mut self, catalog_changes: Arc<CatalogChanges>) -> Self {
        self.catalog_changes = Some(catalog_changes);
        self
    }

    fn stop(&mut self) {
        self.pending = None;
        self.response_permits.lock().unwrap().clear();
        if let Some(catalog) = &self.catalog_changes {
            catalog.clear_current();
        }
        self.inbound.close();
    }
}

impl<T> Transport<RoleClient> for BoundedTransport<T>
where
    T: Transport<RoleClient, Error = io::Error>,
{
    type Error = io::Error;

    fn send(
        &mut self,
        item: ClientJsonRpcMessage,
    ) -> impl Future<Output = Result<(), Self::Error>> + Send + 'static {
        let response_id = match &item {
            JsonRpcMessage::Response(response) => Some(response.id.clone()),
            JsonRpcMessage::Error(error) => error.id.clone(),
            _ => None,
        };
        let mut counter = ByteCounter::new(FRAME_MAX_BYTES);
        let prepared = match serde_json::to_writer(&mut counter, &item) {
            Ok(()) => {
                if let Some(catalog) = &self.catalog_changes {
                    observe_outbound(catalog, &item);
                }
                let permit =
                    response_id.and_then(|id| self.response_permits.lock().unwrap().remove(&id));
                Ok((self.inner.send(item), permit))
            }
            Err(error) => Err(io::Error::new(io::ErrorKind::InvalidData, error)),
        };

        async move {
            match prepared {
                Ok((send, permit)) => {
                    let result = send.await;
                    drop(permit);
                    result
                }
                Err(error) => Err(error),
            }
        }
    }

    async fn receive(&mut self) -> Option<ServerJsonRpcMessage> {
        let message = if let Some(message) = self.pending.take() {
            message
        } else {
            let Some(message) = self.inner.receive().await else {
                self.stop();
                return None;
            };
            message
        };

        if !matches!(
            &message,
            JsonRpcMessage::Request(_) | JsonRpcMessage::Notification(_)
        ) {
            if let Some(catalog) = &self.catalog_changes {
                observe_inbound(catalog, &message);
            }
            return Some(message);
        }

        self.pending = Some(message);
        let permit = match self.inbound.clone().acquire_owned().await {
            Ok(permit) => permit,
            Err(_) => {
                self.stop();
                return None;
            }
        };
        let mut message = self.pending.take().expect("pending frame");

        if let Some(catalog) = &self.catalog_changes {
            observe_inbound(catalog, &message);
        }

        match &mut message {
            JsonRpcMessage::Request(request) => {
                let id = request.id.clone();
                let mut permits = self.response_permits.lock().unwrap();
                if permits.contains_key(&id) {
                    drop(permits);
                    self.stop();
                    return None;
                }
                permits.insert(id, permit);
            }
            JsonRpcMessage::Notification(notification) => {
                notification
                    .notification
                    .extensions_mut()
                    .insert(NotificationPermit {
                        _permit: Arc::new(permit),
                    });
            }
            _ => unreachable!("permit is only acquired for requests and notifications"),
        }

        Some(message)
    }

    async fn close(&mut self) -> Result<(), Self::Error> {
        self.stop();
        self.inner.close().await
    }
}

#[derive(Clone)]
struct NotificationPermit {
    _permit: Arc<OwnedSemaphorePermit>,
}

fn observe_outbound(catalog: &CatalogChanges, item: &ClientJsonRpcMessage) {
    match item {
        JsonRpcMessage::Request(request) => {
            if matches!(
                &request.request,
                ClientRequest::SubscriptionsListenRequest(_)
            ) {
                catalog.set_subscription(request.id.clone());
            }
        }
        JsonRpcMessage::Notification(notification) => {
            if let ClientNotification::CancelledNotification(cancelled) = &notification.notification
                && let Some(id) = &cancelled.params.request_id
            {
                catalog.clear_subscription(id);
            }
        }
        _ => {}
    }
}

fn observe_inbound(catalog: &CatalogChanges, message: &ServerJsonRpcMessage) {
    match message {
        JsonRpcMessage::Notification(notification)
            if matches!(
                &notification.notification,
                ServerNotification::ToolListChangedNotification(_)
            ) =>
        {
            catalog
                .observe_tool_list_changed(notification.notification.get_meta().subscription_id())
        }
        JsonRpcMessage::Response(response) => catalog.end_subscription(&response.id),
        JsonRpcMessage::Error(error) => {
            if let Some(id) = &error.id {
                catalog.end_subscription(id);
            }
        }
        _ => {}
    }
}

#[cfg(test)]
#[path = "../../../tests/tools/mcp_transport.rs"]
mod tests;
