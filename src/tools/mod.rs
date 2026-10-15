#[macro_export]
macro_rules! registry_tool {
    ($registry:expr,[ $ ( $module:ident),* $(,)?]) => {
        $(
            pub mod $module;
            $registry.registry($module::tool());
        )*
    };
}
pub mod bash;
pub mod caculator;
pub mod edit;
mod output;
pub mod read;
pub mod tool_registry;
pub mod write;

use serde::de::DeserializeOwned;
use serde_json::Value;
use std::{path::PathBuf, sync::Arc};
use tool_registry::{ToolError, ToolRegistry};

pub struct ToolContext {
    pub cwd: PathBuf,
    pub output_dir: PathBuf,
}

impl ToolContext {
    pub fn resolve_path(&self, path: &str) -> PathBuf {
        self.cwd.join(path)
    }
}

pub fn register_builtins(registry: &mut ToolRegistry, context: ToolContext) {
    let context = Arc::new(context);
    registry.registry(read::init(context.clone()));
    registry.registry(bash::init(context.clone()));
    registry.registry(edit::init(context.clone()));
    registry.registry(write::init(context));
}

fn parse_args<T: DeserializeOwned>(args: Value) -> Result<T, ToolError> {
    serde_json::from_value(args).map_err(|error| ToolError::InvalidArgs(error.to_string()))
}

#[cfg(test)]
#[path = "../../tests/tools/mod.rs"]
mod tests;
