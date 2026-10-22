use std::path::PathBuf;

use serde::{Deserialize, Serialize, de::Deserializer};

use super::{runtime::RuntimeError, store::SessionMetadata};
use crate::{
    agent::conversation::{CompactionConfig, ValidatedCompactionConfig},
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
    pub model: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub catalog_id: Option<String>,
    #[serde(default, deserialize_with = "non_null")]
    pub compaction: Option<CompactionConfig>,
    #[serde(default, deserialize_with = "non_null")]
    pub working_directory: Option<PathBuf>,
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
    pub model: String,
    pub api: String,
    pub compaction: CompactionConfig,
    pub capacity_source: CapacitySource,
    pub working_directory: PathBuf,
    pub system_prompt: String,
    pub tool_selection: Vec<&'static str>,
    pub connection_binding: ConnectionBinding,
    pub connection_identity: Option<()>,
    pub execution_connection_id: &'static str,
}

#[derive(Debug, Clone, Serialize)]
pub struct ConnectionBinding {
    pub state: &'static str,
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
                model: metadata.model.clone(),
                api: metadata.api.clone(),
                compaction: metadata.compaction,
                capacity_source: metadata.capacity_source.clone(),
                working_directory: metadata.working_directory.clone(),
                system_prompt,
                tool_selection: vec!["read", "bash", "edit", "write"],
                connection_binding: ConnectionBinding { state: "unknown" },
                connection_identity: None,
                execution_connection_id: "default",
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

pub fn prepare_model_config(
    metadata: &SessionMetadata,
    items: &[InputItem],
    patch: ConfigPatch,
    catalog: Option<&ModelCatalog>,
    host_model: &str,
    host_compaction: CompactionConfig,
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
    let model = patch.model.unwrap_or_else(|| metadata.model.clone());
    if model.trim().is_empty() {
        return Err(RuntimeError::new(
            "invalid_request",
            "model must not be empty",
        ));
    }
    let (config, source) = if let Some(compaction) = patch.compaction {
        (compaction, CapacitySource::Explicit)
    } else if patch.catalog_id.is_none() && model != metadata.model && model == host_model {
        (host_compaction, CapacitySource::Stored)
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
        let mut config = metadata.compaction;
        config.context_budget_tokens = limits.context;
        config.max_input_tokens = limits.input;
        if let Some(output) = limits.output {
            config.reserve_tokens = config.reserve_tokens.min(output);
        }
        config.max_summary_tokens = config.max_summary_tokens.min(config.reserve_tokens);
        (config, CapacitySource::Catalog { catalog_id: id })
    } else {
        (metadata.compaction, metadata.capacity_source.clone())
    };
    let validated = config
        .validate()
        .map_err(|error| RuntimeError::new("invalid_model_limits", error.to_string()))?;
    if model != metadata.model
        && items.iter().any(|item| {
            matches!(
                item,
                InputItem::ProviderItem { .. }
                    | InputItem::Reasoning {
                        provider_data: Some(_),
                        ..
                    }
            )
        })
    {
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
