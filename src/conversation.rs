use serde::Serialize;
use serde_json::Value;

use crate::client::{
    InputItem::{self},
    InputMessageContent, MessageRole,
};

#[derive(Serialize)]
#[serde(transparent)]
pub struct Conversation {
    //system_prompt: String,
    items: Vec<InputItem>,
}

impl Conversation {
    pub fn new(system_prompt: String) -> Self {
        let system_input = InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text(system_prompt),
        };
        let items: Vec<InputItem> = vec![system_input];

        Self { items }
    }
    pub fn add_user_message(&mut self, user_prompt: String) {
        let user_input = InputItem::Message {
            role: MessageRole::User,
            content: InputMessageContent::Text(user_prompt),
        };
        self.items.push(user_input)
    }
    pub fn add_assistant_message(&mut self, assistant_message: String) {
        let assistant_message = InputItem::Message {
            role: MessageRole::Assistant,
            content: InputMessageContent::Text(assistant_message),
        };
        self.items.push(assistant_message);
    }
    pub fn add_function_call(&mut self, call_id: String, name: String, arguments: String) {
        let function_call = InputItem::FunctionCall {
            call_id,
            name,
            arguments,
        };
        self.items.push(function_call);
    }
    pub fn add_function_call_output(&mut self, call_id: String, output: Value) {
        let function_call_output = InputItem::FunctionCallOutput { call_id, output };
        self.items.push(function_call_output);
    }
}
