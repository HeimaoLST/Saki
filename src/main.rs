use std::{collections::HashMap, fmt::format};
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let base_url = "http://34.56.72.108:46343/v1";
    let api_key = "sk-82631db0ae2644ef3acae77b19a8a9bafe9ab25efc2a480b";
    let mut headers = reqwest::header::HeaderMap::new();
    headers.append("Content-Type", "application/json".parse().unwrap());
    headers.append(
        "Authorization",
        format!("Bearer {}", api_key).parse().unwrap(),
    );
    let mut data = HashMap::new();
    data.insert("model", "gpt-5.6-sol");
    data.insert("input", "hello, introduce yourself");
    let client = reqwest::Client::new();

    let send = client
        .post(format!("{base_url}/responses"))
        .headers(headers)
        .json(&data)
        .send()
        .await;
    let response = send?.error_for_status()?;

    let response_text = response.json::<serde_json::Value>().await?;
    println!("Response: {response_text}");
    Ok(())
}
