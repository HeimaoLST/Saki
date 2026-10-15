use std::str::FromStr;
use std::sync::Arc;

use serde_json::Value;

use crate::protocol::{ToolDefinition, ToolType};
use crate::tools::tool_registry::{Tool, ToolError};

async fn caculator(value: Value) -> Result<Value, ToolError> {
    let a = value["a"]
        .as_f64()
        .ok_or_else(|| ToolError::InvalidArgs("a".to_owned()))?;
    let b = value["b"]
        .as_f64()
        .ok_or_else(|| ToolError::InvalidArgs("b".to_owned()))?;
    let op = value["op"]
        .as_str()
        .ok_or_else(|| ToolError::InvalidArgs("op".to_owned()))?;

    let result = match op {
        "+" => a + b,
        "-" => a - b,
        "*" => a * b,
        "/" => a / b,
        _ => return Err(ToolError::InvalidArgs("op".to_owned())),
    };
    Ok(serde_json::json!({
        "result" : result
    }))
}
pub fn init() -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "caculator".to_owned(),
            description: String::from_str("Perform basic arithmetic on two numbers.").unwrap(),
            parameters: serde_json::json!({
                "type": "object",
                "properties" : {
                    "a" : {
                        "type" : "number"
                    },
                    "b" : {
                        "type" : "number"
                    },
                    "op" : {
                        "type" : "string",
                        "enum" : ["+","-","*","/"]
                    }
                },
                "required" : ["a","b","op"]
            }),
        },
        handler: Arc::new(|args| Box::pin(caculator(args))),
    }
}
