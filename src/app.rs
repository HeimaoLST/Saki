use std::io::IsTerminal;

use tokio::sync::mpsc;

use crate::{
    agent::conversation::CompactionConfig,
    client::{ClientConfig, ModelApi, ModelClient},
    frontend::{console, tui},
    model::catalog,
    session::{SYSTEM_PROMPT, SessionManager, store::SessionStore},
};

/// Application assembly: existing model, tools, conversation and session setup,
/// then the agent worker connected to the current terminal front end.
///
/// This module wires channels and lifecycle only; it contains no model/tool loop
/// and no terminal drawing.
pub async fn run() -> anyhow::Result<()> {
    // Assembly boundary: the protocol, key and base URL are read once here and
    // injected explicitly; nothing below reads them again.
    let client_config = ClientConfig::from_env()?;
    let client = ModelClient::new(&client_config)?;
    let model = std::env::var("SAKI_MODEL").unwrap_or_else(|_| "deepseek-v4.1-flash".to_owned());
    let mut loaded_catalog = None;
    let mut config = match std::env::var("SAKI_CONTEXT_TOKENS") {
        Ok(value) => CompactionConfig::new(value.parse()?),
        Err(_) => {
            let model_id = std::env::var("SAKI_MODEL_ID").unwrap_or_else(|_| model.clone());
            let limits =
                match catalog::ModelCatalog::load(std::path::Path::new(".saki/cache/models.json"))
                    .await
                {
                    Ok(catalog) => {
                        let limits = catalog
                            .limits(&model_id)
                            .ok_or_else(|| anyhow::anyhow!("no unique model match for {model_id}"));
                        loaded_catalog = Some(catalog);
                        limits
                    }
                    Err(error) => Err(error),
                };
            let mut config = CompactionConfig::new(catalog::DEFAULT_CONTEXT_TOKENS);
            match limits {
                Ok(limits) => {
                    config.context_budget_tokens = limits.context;
                    config.max_input_tokens = limits.input;
                    if let Some(output) = limits.output {
                        config.reserve_tokens = config.reserve_tokens.min(output);
                    }
                    config.max_summary_tokens =
                        config.max_summary_tokens.min(config.reserve_tokens);
                }
                Err(error) => eprintln!("Using default 256K context capacity: {error}"),
            }
            config
        }
    };
    if let Ok(value) = std::env::var("SAKI_OUTPUT_TOKENS") {
        config.reserve_tokens = value.parse()?;
    }
    if let Ok(value) = std::env::var("SAKI_COMPACT_KEEP_TOKENS") {
        config.keep_recent_tokens = value.parse()?;
    }
    if let Ok(value) = std::env::var("SAKI_COMPACT_SUMMARY_TOKENS") {
        config.max_summary_tokens = value.parse()?;
    }
    crate::agent::conversation::Conversation::new(SYSTEM_PROMPT.into())
        .with_compaction_config(config)?;
    let cwd = std::env::current_dir()?.canonicalize()?;
    let root = std::env::var_os("SAKI_SESSION_DIR")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|| cwd.join(".saki/sessions"));
    let store = SessionStore::new(root)?;
    if std::env::args().skip(1).any(|arg| arg == "--stdio") {
        #[cfg(unix)]
        let output = crate::session::stdio::NativeStdout::new()?;
        #[cfg(not(unix))]
        let output = tokio::io::stdout();
        return crate::session::runtime::Runtime::new(store, client, cwd, model, config)
            .with_catalog(loaded_catalog)
            .run(tokio::io::BufReader::new(tokio::io::stdin()), output)
            .await;
    }
    let session_id = match std::env::var("SAKI_SESSION").ok().filter(|id| id != "new") {
        // Explicit `new` (or no value at all) starts a fresh session; plain
        // startup never resumes stored history. An explicit ID is opened below
        // and fails loudly when it is missing or locked.
        Some(id) => id,
        None => match client_config.api() {
            // The default API keeps the original creation path.
            ModelApi::Responses => store.create(&cwd, &model, SYSTEM_PROMPT, config)?,
            api => store.create_with_api(&cwd, &model, SYSTEM_PROMPT, api.as_str(), config)?,
        },
    };
    let loaded = store.open_for_api(&session_id, client_config.api().as_str())?;
    let manager = SessionManager::new(store, client, loaded, cwd, model, config)?;

    let (events_tx, events_rx) = mpsc::unbounded_channel();
    let (input_tx, input_rx) = mpsc::unbounded_channel();
    let worker = tokio::spawn(manager.run(input_rx, events_tx));

    // One choice point for the terminal front end. Both front ends connect to
    // the worker through the same channels. The front end result is kept so the
    // worker is always awaited before returning.
    let frontend = match select_frontend() {
        Ok(frontend) => frontend,
        Err(error) => {
            drop(input_tx);
            drop(events_rx);
            let _ = worker.await;
            return Err(error);
        }
    };
    let frontend_result = match frontend {
        Frontend::Console => console::run(input_tx, events_rx).await,
        Frontend::Tui => tui::run(input_tx, events_rx).await,
    };
    let worker_result = worker.await;
    worker_result?;
    frontend_result?;
    Ok(())
}

/// Terminal front ends. This is the only startup selection; never branch on the
/// UI anywhere else.
enum Frontend {
    Console,
    Tui,
}

fn select_frontend() -> anyhow::Result<Frontend> {
    let stdin_tty = std::io::stdin().is_terminal();
    let stdout_tty = std::io::stdout().is_terminal();
    match std::env::var("SAKI_UI").as_deref() {
        Ok("console") => Ok(Frontend::Console),
        Ok("tui") => {
            if stdin_tty && stdout_tty {
                Ok(Frontend::Tui)
            } else {
                // Never take over a pipe or redirected output.
                anyhow::bail!("SAKI_UI=tui requires both stdin and stdout to be terminals")
            }
        }
        Ok(other) => {
            eprintln!("unknown SAKI_UI={other:?}; using console");
            Ok(Frontend::Console)
        }
        // Interactive terminals default to the TUI; pipes and scripts stay on
        // the line-based console adapter.
        Err(_) => {
            if stdin_tty && stdout_tty {
                Ok(Frontend::Tui)
            } else {
                Ok(Frontend::Console)
            }
        }
    }
}
