mod agent;
mod app;
mod client;
mod command;
mod frontend;
mod model;
mod protocol;
mod session;
pub mod tools;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    app::run().await
}
