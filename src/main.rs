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

fn main() -> anyhow::Result<()> {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?;
    let result = runtime.block_on(app::run());
    if std::env::args().skip(1).any(|arg| arg == "--stdio") {
        // The machine host has drained accepted work and released its locks.
        // Tokio's blocking stdin read cannot be cancelled when stdout is lost.
        runtime.shutdown_background();
    }
    result
}
