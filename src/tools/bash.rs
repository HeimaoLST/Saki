use std::{
    process::Stdio,
    sync::Arc,
    time::{Duration, Instant},
};

use serde::Deserialize;
use serde_json::{Value, json};
use tokio::{io::AsyncReadExt, process::Command};

use super::tool_registry::{Tool, ToolError};
use super::{ToolContext, output::OutputAccumulator, parse_args};
use crate::protocol::{ToolDefinition, ToolType};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct BashArgs {
    command: String,
    workdir: Option<String>,
    #[serde(default = "default_timeout")]
    timeout_ms: u64,
}

fn default_timeout() -> u64 {
    120_000
}

/// Kills the process group when the execution future is dropped (for example
/// when the turn is cancelled). `kill_on_drop` alone only signals the shell.
#[cfg(unix)]
struct GroupGuard {
    pid: u32,
    armed: bool,
}

#[cfg(unix)]
impl Drop for GroupGuard {
    fn drop(&mut self) {
        if self.armed {
            let _ = kill_group(self.pid);
        }
    }
}

/// One synchronous process-group kill used by the timeout, normal/error
/// cleanup and the drop guard. A vanished group is benign; other errors are
/// reported on the normal path and ignored in Drop.
#[cfg(unix)]
fn kill_group(pid: u32) -> Result<(), ToolError> {
    let result = unsafe { libc::kill(-(pid as i32), libc::SIGKILL) };
    if result == -1 {
        let error = std::io::Error::last_os_error();
        if error.raw_os_error() == Some(libc::ESRCH) {
            return Ok(());
        }
        return Err(error.into());
    }
    Ok(())
}

#[cfg(unix)]
async fn bash(args: Value, context: &ToolContext) -> Result<Value, ToolError> {
    let args: BashArgs = parse_args(args)?;
    if args.timeout_ms == 0 {
        return Err(ToolError::InvalidArgs(
            "timeout_ms must be positive".to_owned(),
        ));
    }
    let cwd = args
        .workdir
        .as_deref()
        .map(|path| context.resolve_path(path))
        .unwrap_or_else(|| context.cwd.clone());
    let mut command = Command::new("bash");
    command
        .arg("-c")
        .arg(&args.command)
        .current_dir(cwd)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .process_group(0)
        .kill_on_drop(true);
    let mut child = command.spawn()?;
    let pid = child.id().unwrap();
    let mut group_guard = GroupGuard { pid, armed: true };
    let mut stdout = child.stdout.take().unwrap();
    let mut stderr = child.stderr.take().unwrap();
    let mut stdout_buffer = [0; 8192];
    let mut stderr_buffer = [0; 8192];
    let mut stdout_open = true;
    let mut stderr_open = true;
    let mut exit_status = None;
    let mut timed_out = false;
    let started = Instant::now();
    let deadline = tokio::time::sleep(Duration::from_millis(args.timeout_ms));
    tokio::pin!(deadline);
    let mut output = OutputAccumulator::new();
    let collected: Result<(), ToolError> = async {
        while exit_status.is_none() || stdout_open || stderr_open {
            tokio::select! {
                status = child.wait(), if exit_status.is_none() => {
                    exit_status = Some(status?);
                    // Background descendants can keep inherited pipes open after the shell exits.
                    kill_group(pid)?;
                }
                chunk = stdout.read(&mut stdout_buffer), if stdout_open => {
                    let size = chunk?;
                    stdout_open = size != 0;
                    if size != 0 { output.append(&stdout_buffer[..size], &context.output_dir).await?; }
                }
                chunk = stderr.read(&mut stderr_buffer), if stderr_open => {
                    let size = chunk?;
                    stderr_open = size != 0;
                    if size != 0 { output.append(&stderr_buffer[..size], &context.output_dir).await?; }
                }
                _ = &mut deadline, if !timed_out => {
                    timed_out = true;
                    kill_group(pid)?;
                }
            }
        }
        output.finish().await?;
        Ok(())
    }.await;
    if let Err(error) = collected {
        kill_group(pid)?;
        child.wait().await?;
        return Err(error);
    }
    let mut result = json!({"output":output.preview(), "exit_code":exit_status.unwrap().code(),
        "timed_out":timed_out, "truncated":output.path.is_some(), "wall_time_ms":started.elapsed().as_millis()});
    if let Some(path) = output.path {
        result["output_path"] = json!(path);
    }
    group_guard.armed = false;
    Ok(result)
}

#[cfg(not(unix))]
async fn bash(_: Value, _: &ToolContext) -> Result<Value, ToolError> {
    Err(ToolError::ExecutionFailed(
        "bash currently requires a Unix execution environment".to_owned(),
    ))
}

pub fn init(context: Arc<ToolContext>) -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "bash".to_owned(),
            description: "Run a non-interactive bash command and wait for completion. workdir defaults to the fixed working directory. timeout_ms defaults to 120000; timed-out commands and their process group are terminated. No managed background processes or stdin sessions. Returns combined stdout/stderr, exit_code, and timed_out. Preview is limited to the last 2000 lines / 50 KiB; larger output is saved at output_path for read or rg. Use for searches, tests, and batch scripts.".to_owned(),
            parameters: json!({"type":"object", "properties":{
                "command":{"type":"string"}, "workdir":{"type":"string"},
                "timeout_ms":{"type":"integer","minimum":1}}, "required":["command"], "additionalProperties":false}),
        },
        handler: Arc::new(move |args| { let context = context.clone(); Box::pin(async move { bash(args, &context).await }) }),
    }
}
