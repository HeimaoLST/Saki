mod agent;
mod app;
mod client;
mod command;
mod frontend;
mod model;
mod protocol;
mod session;
pub mod tools;

#[cfg(all(test, unix))]
#[path = "../tests/session/process.rs"]
mod process_tests;

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
    // app::run has drained the service and released its locks. Tokio's pending
    // stdin read cannot be cancelled and must not hold either host open.
    runtime.shutdown_background();
    result
}
