mod agent;
mod client;
mod conversation;
pub mod tools;

use crate::agent::Agent;
use crate::{
    client::DeepseekClient,
    conversation::Conversation,
    tools::{caculator, tool_registry::ToolRegistry},
};
#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let mut tool_registry = ToolRegistry::new();
    let client = DeepseekClient::new();
    let system_prompt = r#"
    你是一个可以调用工具的Agent,你的名字叫做Saki
        "#
    .to_string();
    let conversation = Conversation::new(system_prompt);
    tool_registry.registry(caculator::init());
    let mut agent = Agent::new(client, tool_registry, conversation);
    agent.run().await?;
    Ok(())
}
