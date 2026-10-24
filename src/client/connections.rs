//! Static host-owned connection directory.
//!
//! Credentials are read only from the host-provided environment callback and
//! live inside each `ModelClient`. A connection identity intentionally contains
//! only the normalized protocol and endpoint so key rotation does not change
//! persisted identity.

use std::collections::BTreeMap;

use anyhow::{Context, Result, ensure};
use reqwest::Url;
use serde::{Deserialize, Serialize};

use super::{ClientConfig, ModelClient};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConnectionIdentity {
    pub connection_id: String,
    pub api: String,
    pub endpoint: String,
}

#[derive(Clone)]
pub struct Connection {
    identity: ConnectionIdentity,
    default_model: String,
    client: ModelClient,
}

impl Connection {
    pub fn identity(&self) -> &ConnectionIdentity {
        &self.identity
    }

    pub fn default_model(&self) -> &str {
        &self.default_model
    }

    pub fn client(&self) -> &ModelClient {
        &self.client
    }

    pub fn new(connection_id: String, config: ClientConfig, default_model: String) -> Result<Self> {
        ensure!(
            !connection_id.trim().is_empty(),
            "connection id must not be empty"
        );
        ensure!(
            !default_model.trim().is_empty(),
            "default model must not be empty"
        );
        let endpoint = normalize_endpoint(config.base_url())?;
        let identity = ConnectionIdentity {
            connection_id,
            api: config.api().as_str().to_owned(),
            endpoint,
        };
        let client = ModelClient::new(&config)?;
        Ok(Self {
            identity,
            default_model,
            client,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ConnectionView {
    pub id: String,
    pub api: String,
    pub endpoint: String,
    pub default_model: String,
}

#[derive(Clone)]
pub struct ConnectionRegistry {
    default_id: String,
    connections: BTreeMap<String, Connection>,
}

impl ConnectionRegistry {
    pub fn new(default_id: String, connections: Vec<Connection>) -> Result<Self> {
        ensure!(
            !connections.is_empty(),
            "at least one connection is required"
        );
        let mut directory = BTreeMap::new();
        for connection in connections {
            let id = connection.identity.connection_id.clone();
            ensure!(
                directory.insert(id.clone(), connection).is_none(),
                "duplicate connection id: {id}"
            );
        }
        ensure!(
            directory.contains_key(&default_id),
            "default_connection_id does not match any connection"
        );
        Ok(Self {
            default_id,
            connections: directory,
        })
    }

    pub fn get(&self, connection_id: &str) -> Option<&Connection> {
        self.connections.get(connection_id)
    }

    pub fn default(&self) -> &Connection {
        &self.connections[&self.default_id]
    }

    pub fn len(&self) -> usize {
        self.connections.len()
    }

    pub fn views(&self) -> Vec<ConnectionView> {
        self.connections
            .values()
            .map(|connection| ConnectionView {
                id: connection.identity.connection_id.clone(),
                api: connection.identity.api.clone(),
                endpoint: connection.identity.endpoint.clone(),
                default_model: connection.default_model.clone(),
            })
            .collect()
    }
}

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConnectionDefinition {
    pub id: String,
    pub api: String,
    pub base_url: String,
    pub api_key_env: String,
    pub default_model: String,
}

fn normalize_endpoint(raw: &str) -> Result<String> {
    let mut url = Url::parse(raw).context("connection base_url must be an absolute HTTP(S) URL")?;
    ensure!(
        matches!(url.scheme(), "http" | "https"),
        "connection base_url must use http or https"
    );
    ensure!(
        url.host_str().is_some(),
        "connection base_url must be an absolute URL with a host"
    );
    ensure!(
        url.username().is_empty() && url.password().is_none(),
        "connection base_url must not contain credentials"
    );
    ensure!(
        url.query().is_none() && url.fragment().is_none(),
        "connection base_url must not contain a query or fragment"
    );
    let path = url.path().trim_end_matches('/').to_owned();
    url.set_path(&path);
    Ok(url.to_string())
}

#[cfg(test)]
#[path = "../../tests/client/connections.rs"]
mod tests;
