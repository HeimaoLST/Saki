use std::{path::PathBuf, sync::Arc};

pub use crate::protocol::ToolSelection;
use serde::{Deserialize, Serialize, de::Deserializer};

use super::{runtime::RuntimeError, store::SessionMetadata};
use crate::{
    agent::conversation::{CompactionConfig, ValidatedCompactionConfig},
    client::{
        ModelClient,
        connections::{ConnectionIdentity, ConnectionRegistry},
    },
    model::catalog::ModelCatalog,
    protocol::{InputContentPart, InputItem, InputMessageContent, MessageRole},
};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum CapacitySource {
    #[default]
    Stored,
    Explicit,
    Catalog {
        catalog_id: String,
    },
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigPatch {
    #[serde(default, deserialize_with = "non_null")]
    pub connection_id: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub system_prompt: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub tool_selection: Option<ToolSelection>,
    #[serde(default, deserialize_with = "non_null")]
    pub model: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub catalog_id: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub compaction: Option<CompactionConfig>,
    #[serde(default, deserialize_with = "non_null")]
    pub working_directory: Option<PathBuf>,
}

impl ConfigPatch {
    pub(crate) fn is_only_binding(&self) -> bool {
        self.connection_id.is_some()
            && self.model.is_none()
            && self.catalog_id.is_none()
            && self.compaction.is_none()
            && self.working_directory.is_none()
            && self.system_prompt.is_none()
            && self.tool_selection.is_none()
    }
}

fn non_null<'de, D: Deserializer<'de>, T: Deserialize<'de>>(
    deserializer: D,
) -> Result<Option<T>, D::Error> {
    T::deserialize(deserializer).map(Some)
}

#[derive(Debug, Clone, Serialize)]
pub struct CatalogModel {
    pub catalog_id: String,
    pub name: Option<String>,
    pub limits: crate::model::catalog::ModelLimits,
}
#[derive(Debug, Clone, Serialize)]
pub struct ConfiguredModel {
    pub model: String,
    pub compaction: CompactionConfig,
}

#[derive(Debug, Clone, Serialize)]
pub struct EffectiveConfig {
    pub connection_id: Option<String>,
    pub model: String,
    pub api: String,
    pub compaction: CompactionConfig,
    pub capacity_source: CapacitySource,
    pub working_directory: PathBuf,
    pub system_prompt: String,
    pub tool_selection: ToolSelection,
    pub connection_binding: ConnectionBinding,
    pub connection_identity: Option<ConnectionIdentity>,
    pub execution_connection_id: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ConnectionBinding {
    pub state: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub origin: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub bound_at_revision: Option<u64>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ConfigView {
    pub session_id: String,
    pub effective_config: EffectiveConfig,
    pub config_revision: u64,
    pub storage_schema: u32,
    pub immutable_fields: [&'static str; 1],
}

impl ConfigView {
    pub fn from_metadata(metadata: &SessionMetadata, system_prompt: String) -> Self {
        Self {
            session_id: metadata.id.clone(),
            effective_config: EffectiveConfig {
                connection_id: metadata
                    .connection_identity
                    .as_ref()
                    .map(|identity| identity.connection_id.clone()),
                model: metadata.model.to_owned(),
                api: metadata.api.clone(),
                compaction: metadata.compaction,
                capacity_source: metadata.capacity_source.clone(),
                working_directory: metadata.working_directory.clone(),
                system_prompt: metadata.system_prompt.clone().unwrap_or(system_prompt),
                tool_selection: metadata
                    .tool_selection
                    .clone()
                    .unwrap_or_else(default_tool_selection),
                connection_binding: ConnectionBinding {
                    origin: metadata.binding_origin.clone(),
                    bound_at_revision: metadata.bound_at_revision,
                    state: if metadata.connection_identity.is_some() {
                        "bound"
                    } else {
                        "unknown"
                    },
                },
                connection_identity: metadata.connection_identity.clone(),
                execution_connection_id: None,
            },
            config_revision: metadata.config_revision,
            storage_schema: metadata.schema_version,
            immutable_fields: ["working_directory"],
        }
    }
}

pub struct PreparedModelConfig {
    pub model: String,
    pub compaction: ValidatedCompactionConfig,
    pub capacity_source: CapacitySource,
    pub changed: bool,
}

pub struct ModelConfigInput<'a> {
    pub model: &'a str,
    pub api: &'a str,
    pub compaction: CompactionConfig,
    pub capacity_source: &'a CapacitySource,
}
impl<'a> From<&'a SessionMetadata> for ModelConfigInput<'a> {
    fn from(metadata: &'a SessionMetadata) -> Self {
        Self {
            model: &metadata.model,
            api: &metadata.api,
            compaction: metadata.compaction,
            capacity_source: &metadata.capacity_source,
        }
    }
}

pub fn capacity_from_limits(
    mut config: CompactionConfig,
    limits: crate::model::catalog::ModelLimits,
) -> CompactionConfig {
    config.context_budget_tokens = limits.context;
    config.max_input_tokens = limits.input;
    if let Some(output) = limits.output {
        config.reserve_tokens = config.reserve_tokens.min(output);
    }
    config.max_summary_tokens = config.max_summary_tokens.min(config.reserve_tokens);
    config
}

pub fn builtin_names() -> &'static [&'static str] {
    &["bash", "edit", "read", "write"]
}

pub fn default_tool_selection() -> ToolSelection {
    ToolSelection {
        builtin: builtin_names()
            .iter()
            .map(|name| (*name).to_owned())
            .collect(),
        external: Vec::new(),
    }
}

pub fn validate_tool_selection(selection: &ToolSelection) -> Result<ToolSelection, RuntimeError> {
    if !selection.external.is_empty() {
        return Err(RuntimeError::new(
            "unsupported_feature",
            "external tools are not supported until issue #107",
        ));
    }
    let available = builtin_names();
    if selection
        .builtin
        .iter()
        .any(|name| !available.contains(&name.as_str()))
    {
        return Err(RuntimeError::new(
            "unknown_tool",
            "tool selection contains an unavailable builtin tool",
        ));
    }
    let mut builtin = selection.builtin.clone();
    builtin.sort();
    builtin.dedup();
    Ok(ToolSelection {
        builtin,
        external: Vec::new(),
    })
}

pub fn has_native_continuation(items: &[InputItem]) -> bool {
    items.iter().any(|item| {
        matches!(
            item,
            InputItem::ProviderItem { .. }
                | InputItem::Reasoning {
                    provider_data: Some(_),
                    ..
                }
        )
    })
}

pub fn prepare_model_config(
    metadata: ModelConfigInput<'_>,
    items: &[InputItem],
    patch: ConfigPatch,
    catalog: Option<&ModelCatalog>,
    host_model: &str,
    host_capacity: (CompactionConfig, CapacitySource),
) -> Result<PreparedModelConfig, RuntimeError> {
    if patch.working_directory.is_some() {
        return Err(RuntimeError::new(
            "immutable_field",
            "working_directory requires a new session",
        ));
    }
    if patch.catalog_id.is_some() && patch.compaction.is_some() {
        return Err(RuntimeError::new(
            "invalid_request",
            "choose catalog_id or explicit compaction, not both",
        ));
    }
    let model = patch.model.unwrap_or_else(|| metadata.model.to_owned());
    if model.trim().is_empty() {
        return Err(RuntimeError::new(
            "invalid_request",
            "model must not be empty",
        ));
    }
    let (config, source) = if let Some(compaction) = patch.compaction {
        (compaction, CapacitySource::Explicit)
    } else if patch.catalog_id.is_none() && model != metadata.model && model == host_model {
        host_capacity
    } else if patch.catalog_id.is_some() || model != metadata.model {
        let id = patch.catalog_id.unwrap_or_else(|| model.clone());
        let limits = catalog
            .and_then(|catalog| catalog.limits(&id))
            .ok_or_else(|| {
                RuntimeError::new(
                    "model_limits_required",
                    "no unique loaded model capacity; supply explicit compaction",
                )
            })?;
        let config = capacity_from_limits(metadata.compaction, limits);
        (config, CapacitySource::Catalog { catalog_id: id })
    } else {
        (metadata.compaction, metadata.capacity_source.clone())
    };
    let validated = config
        .validate()
        .map_err(|error| RuntimeError::new("invalid_model_limits", error.to_string()))?;
    if model != metadata.model && has_native_continuation(items) {
        return Err(RuntimeError::new(
            "continuation_not_supported",
            "native continuation cannot be transferred to another model",
        ));
    }
    if metadata.api == crate::client::config::CHAT_COMPLETIONS_API && items.iter().any(|item| matches!(item,
        InputItem::Message { content: InputMessageContent::Parts(parts), .. } if parts.iter().any(|part| matches!(part, InputContentPart::Image { .. }))
    )) {
        return Err(RuntimeError::new("config_incompatible", "this API cannot consume the stored image history"));
    }
    let changed = model != metadata.model || config != metadata.compaction;
    Ok(PreparedModelConfig {
        model,
        compaction: validated,
        capacity_source: source,
        changed,
    })
}

pub fn system_prompt(items: &[InputItem]) -> String {
    match items.first() {
        Some(InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text(text),
            ..
        }) => text.clone(),
        _ => String::new(),
    }
}

#[derive(Clone, Copy)]
pub(crate) struct UpgradeOptions {
    pub(crate) allow_schema_upgrade: bool,
    pub(crate) acknowledge_unknown_origin: bool,
}

pub(crate) struct HostConfiguration {
    pub(crate) client: ModelClient,
    pub(crate) connections: Option<Arc<ConnectionRegistry>>,
    pub(crate) catalog: Option<Arc<ModelCatalog>>,
    pub(crate) model: String,
    pub(crate) compaction: CompactionConfig,
}

impl HostConfiguration {
    pub(crate) fn model_capacity(&self, model: &str) -> (CompactionConfig, self::CapacitySource) {
        if model != self.model
            && let Some(limits) = self
                .catalog
                .as_ref()
                .and_then(|catalog| catalog.limits(model))
        {
            (
                self::capacity_from_limits(self.compaction, limits),
                self::CapacitySource::Catalog {
                    catalog_id: model.into(),
                },
            )
        } else {
            (self.compaction, self::CapacitySource::Stored)
        }
    }
}

pub(crate) struct PreparedConfiguration {
    pub(crate) metadata: SessionMetadata,
    pub(crate) selection: PreparedModelConfig,
    pub(crate) client: ModelClient,
    pub(crate) prompt: String,
    pub(crate) changed: bool,
}

pub(crate) fn select_connection(
    host: &HostConfiguration,
    id: Option<&str>,
) -> Result<
    (
        ModelClient,
        Option<crate::client::connections::ConnectionIdentity>,
        String,
    ),
    RuntimeError,
> {
    if let Some(directory) = &host.connections {
        let connection = match id {
            Some(id) => directory.get(id).ok_or_else(|| {
                RuntimeError::new("unknown_connection", "connection is not configured")
            })?,
            None => directory.default(),
        };
        Ok((
            connection.client().clone(),
            Some(connection.identity().clone()),
            connection.default_model().to_owned(),
        ))
    } else if id.is_none() || id == Some("default") {
        Ok((host.client.clone(), None, host.model.clone()))
    } else {
        Err(RuntimeError::new(
            "unknown_connection",
            "connection is not configured",
        ))
    }
}

pub(crate) fn prepare_configuration(
    host: &HostConfiguration,
    metadata: &SessionMetadata,
    items: &[crate::protocol::InputItem],
    current_prompt: &str,
    patch: &ConfigPatch,
    upgrade: UpgradeOptions,
) -> Result<PreparedConfiguration, RuntimeError> {
    let prompt = patch
        .system_prompt
        .clone()
        .unwrap_or_else(|| current_prompt.into());
    let old_tools = metadata
        .tool_selection
        .clone()
        .unwrap_or_else(self::default_tool_selection);
    let tools = self::validate_tool_selection(patch.tool_selection.as_ref().unwrap_or(&old_tools))?;
    let new_connection = patch.connection_id.as_deref().or_else(|| {
        metadata
            .connection_identity
            .as_ref()
            .map(|identity| identity.connection_id.as_str())
    });
    let (client, identity, default_model) = if new_connection.is_some() {
        select_connection(host, new_connection)?
    } else {
        (
            resolve_connection(metadata, &host.client, host.connections.as_deref())?,
            None,
            host.model.clone(),
        )
    };
    if client.api().as_str() != metadata.api {
        return Err(RuntimeError::new(
            "api_mismatch",
            "cross-API configuration requires a new session",
        ));
    }
    let enhanced_change =
        identity != metadata.connection_identity || prompt != current_prompt || tools != old_tools;
    let mut next = metadata.clone();
    if metadata.schema_version == 1 && enhanced_change {
        if !upgrade.allow_schema_upgrade {
            return Err(RuntimeError::new(
                "schema_upgrade_required",
                "explicit allow_schema_upgrade is required",
            ));
        }
        if !upgrade.acknowledge_unknown_origin {
            return Err(RuntimeError::new(
                "acknowledgement_required",
                "explicit acknowledgement of unknown legacy origin is required",
            ));
        }
        if patch.connection_id.is_none() || identity.is_none() {
            return Err(RuntimeError::new(
                "connection_binding_required",
                "explicitly select the connection for legacy binding",
            ));
        }
        next.schema_version = 2;
        next.binding_origin = Some("explicit_legacy_binding".into());
        next.bound_at_revision = Some(
            metadata
                .config_revision
                .checked_add(1)
                .ok_or_else(|| RuntimeError::conflict(metadata.config_revision))?,
        );
    }
    if enhanced_change && self::has_native_continuation(items) {
        return Err(RuntimeError::new(
            "continuation_not_supported",
            "native continuation cannot be reinterpreted by connection, prompt or tool changes",
        ));
    }
    let selection = self::prepare_model_config(
        metadata.into(),
        items,
        patch.clone(),
        host.catalog.as_deref(),
        &default_model,
        host.model_capacity(&default_model),
    )?;
    next.model = selection.model.clone();
    next.compaction = selection.compaction.get();
    next.capacity_source = selection.capacity_source.clone();
    if next.schema_version == 2 {
        next.connection_identity = identity;
        next.system_prompt = Some(prompt.clone());
        next.tool_selection = Some(tools);
    }
    Ok(PreparedConfiguration {
        changed: selection.changed || enhanced_change,
        metadata: next,
        selection,
        client,
        prompt,
    })
}

pub(crate) fn resolve_connection(
    metadata: &SessionMetadata,
    default: &ModelClient,
    connections: Option<&ConnectionRegistry>,
) -> Result<ModelClient, RuntimeError> {
    if let Some(identity) = &metadata.connection_identity {
        let connection = connections
            .and_then(|directory| directory.get(&identity.connection_id))
            .filter(|connection| connection.identity() == identity)
            .ok_or_else(|| {
                RuntimeError::new(
                    "connection_mismatch",
                    "the bound connection is missing or its endpoint/API has changed",
                )
            })?;
        return Ok(connection.client().clone());
    }
    if connections.is_some_and(|directory| directory.len() > 1) {
        return Err(RuntimeError::new(
            "connection_binding_required",
            "legacy session origin is unknown; explicitly bind a connection before execution",
        ));
    }
    let client = connections.map_or(default, |directory| directory.default().client());
    if client.api().as_str() != metadata.api {
        return Err(RuntimeError::new(
            "api_mismatch",
            "session and configured connection use different APIs",
        ));
    }
    Ok(client.clone())
}
