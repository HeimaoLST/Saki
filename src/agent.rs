use std::io;

use crate::{
    client::{DeepseekClient, ModelRequest, OutputItem, OutputText, ReasoningText},
    conversation::Conversation,
    tool_registry::{ToolCall, ToolRegistry},
};

pub struct Agent {
    client: DeepseekClient,
    tool_registry: ToolRegistry,
    conversation: Conversation,
}

impl Agent {
    pub fn new(
        client: DeepseekClient,
        tool_registry: ToolRegistry,
        conversation: Conversation,
    ) -> Self {
        Self {
            client,
            tool_registry,
            conversation,
        }
    }

    pub async fn run(&mut self) -> anyhow::Result<()> {
        loop {
            let mut input = String::new();
            io::stdin()
                .read_line(&mut input)
                .expect("Failed to read lint");
            self.conversation.add_user_message(input);
            loop {
                let request = ModelRequest::new(
                    "deepseek-flash-v4".to_owned(),
                    &self.conversation,
                    self.tool_registry.list_definition(),
                );
                let respond = self.client.respond(request).await?;
                let output = respond.output;
                let mut pending_function_call = Vec::new();
                let mut assistant_text = String::new();
                for output_item in output {
                    match output_item {
                        OutputItem::Message { content, .. } => {
                            let text: String = content
                                .into_iter()
                                .map(|OutputText { text }| text)
                                .collect::<String>();

                            self.conversation.add_assistant_message(text.clone());
                            assistant_text = text;
                        }
                        OutputItem::Reasoning { content, .. } => {
                            let thinking_text = content
                                .into_iter()
                                .map(|ReasoningText { text, .. }| text)
                                .collect::<String>();
                            println!("Thinking...:\n\n{thinking_text}");
                        }
                        OutputItem::FunctionCall {
                            call_id,
                            name,
                            arguments,
                            ..
                        } => {
                            self.conversation.add_function_call(
                                call_id.clone(),
                                name.clone(),
                                arguments.clone(),
                            );
                            pending_function_call.push((call_id, name, arguments));
                        }
                        _ => {}
                    };
                }
                if pending_function_call.is_empty() {
                    println!("Agent:\n\n{assistant_text}");
                    break;
                }
                //execute the FunctionCall
                for (call_id, name, arguments) in pending_function_call {
                    let function_call_output = match serde_json::from_str(&arguments) {
                        Ok(arg) => {
                            let call = ToolCall::new(call_id.clone(), name, arg);
                            match self.tool_registry.execution_tool(call).await {
                                Ok(output) => output,
                                Err(error) => serde_json::json!({
                                    "error" : error.to_string()
                                }),
                            }
                        }
                        Err(error) => serde_json::json!({
                            "error" : format!("Invalid tool arguments: {error}")
                        }),
                    };

                    self.conversation
                        .add_function_call_output(call_id, function_call_output);
                }
            }
        }
    }
}
