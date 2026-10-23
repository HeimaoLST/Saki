use std::{collections::HashMap, fs, path::Path, time::Duration};

use anyhow::{Result, ensure};
use serde::{Deserialize, Serialize};

const CATALOG_URL: &str = "https://models.dev/catalog.json";
pub const DEFAULT_CONTEXT_TOKENS: usize = 256 * 1024;
const SYNC_INTERVAL: Duration = Duration::from_secs(24 * 60 * 60);

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct ModelLimits {
    pub context: usize,
    pub input: Option<usize>,
    pub output: Option<u32>,
}

#[derive(Clone, Debug, Serialize)]
pub struct CatalogEntry {
    pub id: String,
    pub name: Option<String>,
    pub limits: ModelLimits,
}

#[derive(Deserialize)]
struct Model {
    name: Option<String>,
    limit: ModelLimits,
}

#[derive(Deserialize)]
struct Provider {
    models: HashMap<String, Model>,
}

#[derive(Deserialize)]
pub struct ModelCatalog {
    #[serde(default)]
    providers: HashMap<String, Provider>,
    #[serde(default)]
    models: HashMap<String, Model>,
}

impl ModelCatalog {
    /// Refreshes the public catalog at startup when its local cache is stale.
    /// A failed refresh keeps a usable prior snapshot; no snapshot means failure.
    pub async fn load(cache_path: &Path) -> Result<Self> {
        Self::load_from(cache_path, CATALOG_URL, SYNC_INTERVAL).await
    }

    async fn load_from(cache_path: &Path, url: &str, max_age: Duration) -> Result<Self> {
        let cached = match fs::read(cache_path) {
            Ok(bytes) => Self::from_snapshot(&bytes).ok(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => None,
            Err(error) => return Err(error.into()),
        };
        let cached = match cached {
            Some(catalog)
                if fs::metadata(cache_path)?
                    .modified()?
                    .elapsed()
                    .unwrap_or_default()
                    < max_age =>
            {
                return Ok(catalog);
            }
            cached => cached,
        };

        // Public metadata requests must not inherit the inference bearer token.
        let client = reqwest::Client::builder()
            .user_agent("Saki/0.1")
            .timeout(Duration::from_secs(20))
            .build()?;
        let refreshed = async {
            let bytes = client
                .get(url)
                .send()
                .await?
                .error_for_status()?
                .bytes()
                .await?;
            let catalog = Self::from_snapshot(&bytes)?;
            if let Some(parent) = cache_path
                .parent()
                .filter(|parent| !parent.as_os_str().is_empty())
            {
                fs::create_dir_all(parent)?;
            }
            let temporary = cache_path.with_extension(format!("{}.tmp", std::process::id()));
            fs::write(&temporary, &bytes)?;
            fs::rename(&temporary, cache_path)?;
            Ok::<_, anyhow::Error>(catalog)
        }
        .await;
        match refreshed {
            Ok(catalog) => Ok(catalog),
            Err(error) => match cached {
                Some(catalog) => {
                    eprintln!("Model catalog refresh failed; using cached snapshot: {error}");
                    Ok(catalog)
                }
                None => Err(error.context("models.dev unavailable and no usable cache")),
            },
        }
    }

    fn parse(bytes: &[u8]) -> Result<Self> {
        let catalog: Self = serde_json::from_slice(bytes)?;
        ensure!(
            !catalog.providers.is_empty() || !catalog.models.is_empty(),
            "model catalog is empty"
        );
        Ok(catalog)
    }

    /// Build a catalog from an already-loaded snapshot. This never calls
    /// `load()` and never performs network I/O.
    pub fn from_snapshot(bytes: &[u8]) -> Result<Self> {
        Self::parse(bytes)
    }

    /// Stable, deduplicated model entries from both providers and root models.
    /// Provider entries win over a same-ID root entry, matching `limits()`.
    pub fn entries(&self) -> Vec<CatalogEntry> {
        let mut entries: HashMap<String, CatalogEntry> = HashMap::new();
        for (provider_id, provider) in &self.providers {
            for (model_id, model) in &provider.models {
                if model.limit.context == 0 {
                    continue;
                }
                let id = format!("{provider_id}/{model_id}");
                entries.insert(
                    id.clone(),
                    CatalogEntry {
                        id,
                        name: model.name.clone(),
                        limits: model.limit,
                    },
                );
            }
        }
        for (id, model) in &self.models {
            if model.limit.context == 0 {
                continue;
            }
            entries.entry(id.clone()).or_insert_with(|| CatalogEntry {
                id: id.clone(),
                name: model.name.clone(),
                limits: model.limit,
            });
        }
        let mut entries: Vec<_> = entries.into_values().collect();
        entries.sort_by(|left, right| left.id.cmp(&right.id));
        entries
    }

    /// Prefer exact provider/model or canonical IDs, then a unique model name.
    pub fn limits(&self, requested: &str) -> Option<ModelLimits> {
        if let Some((provider, model_id)) = requested.split_once('/') {
            if let Some(model) = self
                .providers
                .get(provider)
                .and_then(|provider| provider.models.get(model_id))
                && model.limit.context > 0
            {
                return Some(model.limit);
            }
            if let Some(model) = self.models.get(requested)
                && model.limit.context > 0
            {
                return Some(model.limit);
            }
            if let Some(model) = self.models.get(model_id)
                && model.limit.context > 0
            {
                return Some(model.limit);
            }
        }
        if let Some(model) = self.models.get(requested)
            && model.limit.context > 0
        {
            return Some(model.limit);
        }
        let name = requested.rsplit('/').next().unwrap_or(requested);
        let mut matches = self.models.iter().filter(|(id, model)| {
            model.limit.context > 0
                && (id.rsplit('/').next() == Some(name) || model.name.as_deref() == Some(name))
        });
        let (_, model) = matches.next()?;
        if matches.next().is_some() {
            return None;
        }
        Some(model.limit)
    }
}

#[cfg(test)]
#[path = "../../tests/models/mod.rs"]
mod tests;
