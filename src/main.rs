mod agent;
mod client;
mod conversation;
mod tool_registry;
use serde::{Deserialize, Serialize};
use serde_json::Value;

use std::{
    format,
    io::{self, Error},
    println,
    str::FromStr,
    sync::Arc,
};

use crate::tool_registry::{Tool, ToolError, ToolRegistry};
#[derive(Serialize, Deserialize)]
struct Message {
    role: String,
    content: String,
}

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
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut msgs: Vec<Message> = Vec::new();
    let caculator: Tool = Tool {
        name: "caculator".to_owned(),
        description: String::from_str("用来进行两个数字之间的四则运算和算术")?,
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
        handler: Arc::new(|args| Box::pin(caculator(args))),
    };
    let mut tool_registry = ToolRegistry::new();
    tool_registry.registry(caculator);
    let tool_definition = tool_registry.list_definition();
    let system_prompt = format!(
        r#"
    你是一个可以调用工具的Agent,你的名字叫做Saki
    可用工具：
    {}
    如果需要使用工具，输出：
    {{
        "type" : "tool_call",
        "name" : "...",
        "arguments" : {{}}
    }}
        "#,
        serde_json::to_string_pretty(&tool_definition)?
    );
    //init the system_prompt

    msgs.push(Message {
        role: "system".to_owned(),
        content: system_prompt,
    });
    loop {
        let mut user_prompt = String::new();
        println!("User: ");

        io::stdin()
            .read_line(&mut user_prompt)
            .expect("Failed to read line");
        let (think, assistant_msg) = chat(&user_prompt, &mut msgs).await?;
        println!("\n\nThink: {think}\n\n");
        println!("Agent: {assistant_msg}\n\n");
    }
    //Ok(())
}
fn extract_output_text(output: &Value, index: usize, label: &str) -> Result<String, Error> {
    output
        .get(index)
        .and_then(|item| item.get("content"))
        .and_then(|content| content.get(0))
        .and_then(|item| item.get("text"))
        .and_then(Value::as_str)
        .map(str::to_owned)
        .ok_or_else(|| io::Error::other(format!("{label} text not found")))
}
async fn chat(
    prompt: &String,
    msgs: &mut Vec<Message>,
) -> Result<(String, String), Box<dyn std::error::Error>> {
    const BASE_URL: &str = "https://api.deepseek.com";
    const MAX_RETRIES: u32 = 4;
    let mut headers = reqwest::header::HeaderMap::new();
    headers.append("Content-Type", "application/json".parse().unwrap());
    headers.append(
        "Authorization",
        format!("Bearer {}", API_KEY).parse().unwrap(),
    );
    msgs.push(Message {
        role: "user".to_string(),
        content: prompt.clone(),
    });
    let data = serde_json::json!({
        "model":"deepseek-v4-flash",
        "input": msgs
    });

    let client = reqwest::Client::new();
    let mut last_err = String::from("error");

    for _ in 0..MAX_RETRIES {
        let result = client
            .post(format!("{BASE_URL}/responses"))
            .headers(headers.clone())
            .json(&data)
            .send()
            .await
            .and_then(|res| res.error_for_status());

        let res = match result {
            Ok(res) => res,
            Err(err) => {
                last_err = err.to_string();
                continue;
            }
        };

        let response_json = res.json::<serde_json::Value>().await?;
        let output = response_json.get("output").unwrap();
        let think = extract_output_text(output, 0, "thinking")?;
        let assistant_msg = extract_output_text(output, 1, "assistant")?;
        msgs.push(Message {
            role: "assistant".to_string(),
            content: assistant_msg.to_string(),
        });

        return Ok((think, assistant_msg));
    }

    Err(io::Error::other(format!("All retry failed, last err message: {last_err}")).into())
}
