use std::{
    collections::{BTreeSet, HashSet},
    path::Path,
    process::Stdio,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
};

use anyhow::{Context, Result, bail};
use rmcp::{
    ClientCacheConfig, ClientLifecycleMode, ClientServiceExt, RoleClient,
    model::{
        CallToolRequestParams, CallToolResponse, CallToolResult, ClientConfig, ErrorCode,
        PaginatedRequestParams, ProtocolVersion, SubscriptionFilter, Tool,
    },
    service::{ClientInitializeError, RunningService},
    transport::async_rw::AsyncRwTransport,
};
use tokio::{
    process::{Child, Command},
    sync::OwnedSemaphorePermit,
    task::JoinHandle,
    time::{Instant, timeout, timeout_at},
};

use super::{
    config::{PreparedSource, ProtocolPolicy},
    transport::{BoundedTransport, CatalogChanges, FRAME_MAX_BYTES},
};
use crate::{protocol::ByteCounter, tools::bash::GroupGuard};

pub(super) struct SourceProcess {
    pub(super) config: Arc<PreparedSource>,
    pub(super) generation: u64,
    peer: Option<LivePeer>,
    closing: Option<JoinHandle<(LivePeer, Result<()>)>>,
    catalog: Arc<CatalogChanges>,
    subscribed: Arc<AtomicBool>,
    closed: bool,
    legacy: bool,
    call_uncertain: bool,
}

impl SourceProcess {
    pub(super) async fn start(
        config: Arc<PreparedSource>,
        cwd: &Path,
        generation: u64,
        permit: OwnedSemaphorePermit,
    ) -> Result<(Self, Vec<Tool>)> {
        let deadline = Instant::now() + config.startup_timeout;
        let mut legacy = config.protocol == ProtocolPolicy::Legacy;
        loop {
            let mut child = Command::new(&config.command)
                .args(&config.args)
                .current_dir(cwd)
                .env_clear()
                .envs(&config.environment)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::inherit())
                .process_group(0)
                .kill_on_drop(true)
                .spawn()
                .with_context(|| format!("start MCP source {}", config.id))?;
            let mut group = GroupGuard::new(child.id().context("MCP source has no process id")?);
            let catalog = Arc::new(CatalogChanges::new(legacy));
            let transport = BoundedTransport::new(
                AsyncRwTransport::new(
                    child.stdout.take().context("MCP stdout missing")?,
                    child.stdin.take().context("MCP stdin missing")?,
                )
                .with_max_line_length(FRAME_MAX_BYTES),
            )
            .with_catalog_changes(catalog.clone());
            let connected = timeout_at(
                deadline,
                ClientConfig::default()
                    .with_protocol_version(ProtocolVersion::V_2025_11_25)
                    .serve_with_lifecycle(
                        transport,
                        if legacy {
                            ClientLifecycleMode::Initialize
                        } else {
                            ClientLifecycleMode::Discover {
                                preferred_versions: vec![ProtocolVersion::V_2026_07_28],
                            }
                        },
                    ),
            )
            .await;
            let service = match connected {
                Ok(Ok(service)) => service,
                error => {
                    group.terminate()?;
                    child.wait().await?;
                    if !legacy
                        && config.protocol == ProtocolPolicy::Auto
                        && matches!(&error, Ok(Err(error)) if allows_legacy(error))
                    {
                        legacy = true;
                        continue;
                    }
                    return match error {
                        Ok(Err(error)) => Err(error.into()),
                        Err(error) => Err(error.into()),
                        Ok(Ok(_)) => unreachable!(),
                    };
                }
            };
            let mut source = Self {
                config,
                generation,
                peer: Some(LivePeer {
                    child,
                    group,
                    service,
                    listener: None,
                    permit: Some(permit),
                }),
                closing: None,
                catalog,
                subscribed: Arc::new(AtomicBool::new(false)),
                closed: false,
                legacy,
                call_uncertain: false,
            };
            let prepared = async {
                let info = source
                    .service()
                    .peer_info()
                    .context("MCP peer information missing")?;
                let expected = if legacy {
                    ProtocolVersion::V_2025_11_25
                } else {
                    ProtocolVersion::V_2026_07_28
                };
                if info.protocol_version != expected || info.capabilities.tools.is_none() {
                    bail!("MCP source negotiated an unsupported protocol or lacks tools");
                }
                source
                    .service()
                    .set_response_cache_config(ClientCacheConfig::disabled())
                    .await;
                source.subscribe(deadline).await;
                source.discover_until(deadline).await
            }
            .await;
            match prepared {
                Ok(tools) => return Ok((source, tools)),
                Err(error) => {
                    source.close().await?;
                    return Err(error);
                }
            }
        }
    }

    async fn subscribe(&mut self, deadline: Instant) {
        if self.legacy
            || !self.service().peer_info().is_some_and(|info| {
                info.capabilities
                    .tools
                    .as_ref()
                    .is_some_and(|tools| tools.list_changed == Some(true))
            })
        {
            return;
        }
        if let Some(listener) = self
            .peer
            .as_mut()
            .expect("live peer owns subscription")
            .listener
            .take()
        {
            listener.abort();
            let _ = listener.await;
        }
        let filter = SubscriptionFilter::builder().tools_list_changed().build();
        let acknowledgment_deadline =
            deadline.min(Instant::now() + self.config.startup_timeout / 4);
        let subscription = timeout_at(
            acknowledgment_deadline,
            self.service()
                .listen_with_capacity(filter, std::num::NonZeroUsize::new(2).unwrap()),
        )
        .await;
        let Ok(Ok(mut subscription)) = subscription else {
            return;
        };
        if subscription.acknowledged().tools_list_changed != Some(true) {
            let _ = timeout_at(acknowledgment_deadline, subscription.cancel()).await;
            return;
        }
        self.subscribed.store(true, Ordering::Release);
        let catalog = self.catalog.clone();
        let subscribed = self.subscribed.clone();
        let id = subscription.id().clone();
        self.peer
            .as_mut()
            .expect("live peer owns subscription")
            .listener = Some(tokio::spawn(async move {
            while let Ok(Some(_)) = subscription.next().await {}
            catalog.end_subscription(&id);
            subscribed.store(false, Ordering::Release);
        }));
    }

    pub(super) fn is_closed(&self) -> bool {
        self.closed
            || self.call_uncertain
            || self.service().is_closed()
            || self.service().is_transport_closed()
    }

    pub(super) async fn discover(&mut self) -> Result<Vec<Tool>> {
        let deadline = Instant::now() + self.config.startup_timeout;
        if !self.subscribed.load(Ordering::Acquire) || !self.catalog.has_subscription() {
            self.subscribe(deadline).await;
        }
        self.discover_until(deadline).await
    }

    async fn discover_until(&self, deadline: Instant) -> Result<Vec<Tool>> {
        for _ in 0..2 {
            let revision = self.catalog.revision();
            let mut tools = Vec::new();
            let mut cursors = HashSet::new();
            let mut names = BTreeSet::new();
            let mut cursor = None;
            let mut bytes = ByteCounter::new(FRAME_MAX_BYTES);
            for page in 0..64 {
                let result = timeout_at(
                    deadline,
                    self.service()
                        .list_tools(Some(PaginatedRequestParams::default().with_cursor(cursor))),
                )
                .await??;
                for tool in result.tools {
                    serde_json::to_writer(&mut bytes, &tool)?;
                    if tools.len() == 1024 || !names.insert(tool.name.to_string()) {
                        bail!("MCP tool catalog has too many tools or duplicate names");
                    }
                    tools.push(tool);
                }
                cursor = result.next_cursor;
                match &cursor {
                    None => break,
                    Some(next) if page == 63 || !cursors.insert(next.clone()) => {
                        bail!("MCP tool catalog pagination is cyclic or exceeds its limit");
                    }
                    _ => {}
                }
            }
            if self.catalog.revision() == revision {
                return Ok(tools);
            }
        }
        Err(CatalogUnstable.into())
    }

    pub(super) fn protocol_version(&self) -> String {
        self.service()
            .peer_info()
            .expect("startup validated peer info")
            .protocol_version
            .to_string()
    }
    pub(super) fn monitoring(&self) -> &'static str {
        if self.is_closed() {
            "unavailable"
        } else if self.legacy {
            if self.service().peer_info().is_some_and(|info| {
                info.capabilities
                    .tools
                    .as_ref()
                    .is_some_and(|tools| tools.list_changed == Some(true))
            }) {
                "legacy"
            } else {
                "polling"
            }
        } else if self.subscribed.load(Ordering::Acquire) && self.catalog.has_subscription() {
            "subscribed"
        } else {
            "polling"
        }
    }

    pub(super) fn needs_cleanup(&self) -> bool {
        self.call_uncertain
    }

    pub(super) async fn call(
        &mut self,
        name: &str,
        arguments: serde_json::Value,
    ) -> Result<CallToolResult> {
        if self.is_closed() {
            bail!("MCP source is unavailable; business calls are never replayed");
        }
        let arguments = arguments
            .as_object()
            .context("MCP tool arguments must be an object")?
            .clone();
        serde_json::to_writer(&mut ByteCounter::new(FRAME_MAX_BYTES), &arguments)?;
        // A dropped call future leaves this flag set for the owning worker's awaited cleanup.
        self.call_uncertain = true;
        let attempt = timeout(
            self.config.call_timeout,
            self.service().call_tool_once(
                CallToolRequestParams::new(name.to_owned()).with_arguments(arguments),
            ),
        )
        .await;
        let result = match attempt {
            Ok(Ok(CallToolResponse::Complete(result))) => {
                self.call_uncertain = false;
                return Ok(result);
            }
            Ok(Ok(CallToolResponse::InputRequired(_))) => anyhow::anyhow!(
                "MCP input-required interactions are unsupported; call was not replayed"
            ),
            Ok(Ok(CallToolResponse::Task(_))) => {
                anyhow::anyhow!("MCP tasks are unsupported; call was not replayed")
            }
            Ok(Ok(_)) => anyhow::anyhow!("unsupported MCP tool response; call was not replayed"),
            Ok(Err(error)) => error.into(),
            Err(error) => anyhow::Error::new(error).context("MCP tool call timed out"),
        };
        self.close().await.context("MCP failed-call cleanup")?;
        Err(result)
    }

    fn service(&self) -> &RunningService<RoleClient, ClientConfig> {
        &self
            .peer
            .as_ref()
            .expect("open source owns the peer")
            .service
    }

    pub(super) async fn close(&mut self) -> Result<()> {
        self.closed = true;
        if self.closing.is_none() {
            let mut peer = self
                .peer
                .take()
                .context("MCP cleanup completion is unavailable")?;
            if peer.permit.is_none() {
                self.peer = Some(peer);
                return Ok(());
            }
            let grace = self.config.cancel_grace;
            self.closing = Some(tokio::spawn(async move {
                let result = peer.close(grace).await;
                (peer, result)
            }));
        }
        let outcome = self
            .closing
            .as_mut()
            .expect("cleanup job is retained")
            .await;
        self.closing.take();
        let (peer, result) = outcome?;
        self.peer = Some(peer);
        if result.is_ok() {
            self.call_uncertain = false;
        }
        result
    }
}

struct LivePeer {
    child: Child,
    group: GroupGuard,
    service: RunningService<RoleClient, ClientConfig>,
    listener: Option<JoinHandle<()>>,
    permit: Option<OwnedSemaphorePermit>,
}

impl LivePeer {
    async fn close(&mut self, grace: std::time::Duration) -> Result<()> {
        if let Some(listener) = self.listener.take() {
            listener.abort();
            let _ = listener.await;
        }
        let service = &mut self.service;
        let child = &mut self.child;
        let closing = async {
            tokio::try_join!(
                async { service.close().await.map_err(anyhow::Error::from) },
                async { child.wait().await.map_err(anyhow::Error::from) }
            )?;
            Ok::<_, anyhow::Error>(())
        };
        tokio::pin!(closing);
        match timeout_at(Instant::now() + grace, &mut closing).await {
            Ok(result) => result?,
            Err(_) => {
                self.group.terminate()?;
                closing.await?;
            }
        }
        self.group.terminate()?;
        self.permit.take();
        Ok(())
    }
}

fn allows_legacy(error: &ClientInitializeError) -> bool {
    match error {
        ClientInitializeError::JsonRpcError(error) => error.code == ErrorCode::METHOD_NOT_FOUND,
        ClientInitializeError::NoCompatibleProtocolVersion {
            server_supported, ..
        } => server_supported.contains(&ProtocolVersion::V_2025_11_25),
        _ => false,
    }
}

#[cfg(test)]
#[path = "../../../tests/tools/mcp_client.rs"]
mod tests;

#[derive(Debug, thiserror::Error)]
#[error("catalog_unstable: MCP tool catalog changed during both discovery attempts")]
pub(super) struct CatalogUnstable;
