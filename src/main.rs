mod agent;
mod app;
mod client;
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
    for arg in std::env::args().skip(1) {
        match arg.as_str() {
            "--stdio" => {}
            "--help" | "-h" => {
                println!(
                    "Saki Agent Runtime\nUsage: Saki [--stdio]\nReads v1 JSONL requests from stdin and writes responses/events to stdout.\nConfigure the service with SAKI_RUNTIME_CONFIG or provider environment variables."
                );
                return Ok(());
            }
            "--version" | "-V" => {
                println!("Saki {}", env!("CARGO_PKG_VERSION"));
                return Ok(());
            }
            _ => anyhow::bail!("unknown argument {arg:?}; use --help"),
        }
    }
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?;
    let result = runtime.block_on(app::run());
    // app::run has drained the service and released its locks. Tokio's pending
    // stdin read cannot be cancelled and must not hold the service open.
    runtime.shutdown_background();
    result
}
