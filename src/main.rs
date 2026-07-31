use serde::{Deserialize, Serialize};
use serde_json::Value;

use std::{io, println};
#[derive(Serialize, Deserialize)]
struct Message {
    role: String,
    content: String,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut msgs: Vec<Message> = Vec::new();
    loop {
        let mut user_prompt = String::new();
        println!("User: ");

        io::stdin()
            .read_line(&mut user_prompt)
            .expect("Failed to read line");
        let (think, assistant_msg) = chat(&user_prompt, &mut msgs).await?;
        println!("\n\nThink: {think}");
        println!("Agent: {assistant_msg}\n\n");
    }
    //Ok(())
}

async fn chat(
    prompt: &String,
    msgs: &mut Vec<Message>,
) -> Result<(String, String), Box<dyn std::error::Error>> {
    let base_url = "https://api.deepseek.com";
    let api_key = "sk-49d015b1870a4e70989484fc15c65711";
    let mut maxtry = 4;
    let mut headers = reqwest::header::HeaderMap::new();
    headers.append("Content-Type", "application/json".parse().unwrap());
    headers.append(
        "Authorization",
        format!("Bearer {}", api_key).parse().unwrap(),
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
    while maxtry > 0 {
        maxtry -= 1;
        let send = client
            .post(format!("{base_url}/responses"))
            .headers(headers.clone())
            .json(&data)
            .send()
            .await;

        let response = send?.error_for_status();
        match response {
            Ok(res) => {
                let response_json = res.json::<serde_json::Value>().await?;
                let output = response_json.get("output").unwrap();
                let think = output
                    .get(0)
                    .and_then(|item| item.get("content"))
                    .and_then(|content| content.get(0))
                    .and_then(|item| item.get("text"))
                    .and_then(Value::as_str)
                    .ok_or_else(|| io::Error::other("thinking text not found"))?
                    .to_owned();

                let assistant_msg = output
                    .get(1)
                    .and_then(|item| item.get("content"))
                    .and_then(|content| content.get(0))
                    .and_then(|item| item.get("text"))
                    .and_then(Value::as_str)
                    .ok_or_else(|| io::Error::other("assistant text not found"))?
                    .to_owned();

                msgs.push(Message {
                    role: "assistant".to_string(),
                    content: assistant_msg.to_string(),
                });

                return Ok((think, assistant_msg));
            }
            Err(err) => {
                last_err = err.to_string();
            }
        }
    }

    Err(io::Error::other(format!("All retry failed, last err message: {last_err}")).into())
}
