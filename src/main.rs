mod agent;
mod app;
mod client;
mod command;
mod frontend;
mod model;
mod protocol;
mod session;
pub mod tools;

// Single root inclusion of the shared test fixtures. Every module test refers to
// `crate::test_support` instead of re-loading `tests/support/mod.rs` by `#[path]`,
// which used to trigger `clippy::duplicate_mod` and per-copy dead_code.
#[cfg(test)]
#[path = "../tests/support/mod.rs"]
mod test_support;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    app::run().await
}
