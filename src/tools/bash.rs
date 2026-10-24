#[cfg(unix)]
use std::os::unix::process::CommandExt;
use std::{
    process::Stdio,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use serde::Deserialize;
use serde_json::{Value, json};
use tokio::{
    io::AsyncReadExt,
    process::{Child, Command},
    task::JoinHandle,
};

use super::tool_registry::{Tool, ToolError, ToolRegistry};
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

// Dropping an executing handler cannot await Child::wait. Transfer ownership
// to the existing task cleanup boundary before publishing its terminal.
#[derive(Default)]
struct Reaper {
    pending: Mutex<Vec<JoinHandle<Result<(), ToolError>>>>,
    #[cfg(all(test, unix))]
    gate: Mutex<Option<ReapGate>>,
}

#[cfg(all(test, unix))]
struct ReapGate {
    started: tokio::sync::oneshot::Sender<()>,
    resume: tokio::sync::oneshot::Receiver<()>,
}

impl Reaper {
    #[cfg(unix)]
    fn queue(&self, mut child: Child) {
        let Ok(runtime) = tokio::runtime::Handle::try_current() else {
            // A standalone handler dropped after runtime shutdown retains
            // kill_on_drop cleanup; a live task always has its runtime.
            return;
        };
        let pid = child.id();
        #[cfg(all(test, unix))]
        let gate = self.gate.lock().unwrap().take();
        let pending =
            runtime.spawn(async move {
                #[cfg(all(test, unix))]
                if let Some(gate) = gate {
                    let _ = gate.started.send(());
                    let _ = gate.resume.await;
                }
                child.wait().await.map(|_| ()).map_err(|error| {
                    ToolError::ExecutionFailed(format!("reap bash {pid:?}: {error}"))
                })
            });
        self.pending.lock().unwrap().push(pending);
    }

    async fn finish(&self) -> Result<(), ToolError> {
        let pending = std::mem::take(&mut *self.pending.lock().unwrap());
        let mut failure = None;
        for job in pending {
            let result = match job.await {
                Ok(result) => result,
                Err(error) => Err(ToolError::ExecutionFailed(format!(
                    "bash reaper failed: {error}"
                ))),
            };
            if let Err(error) = result {
                failure.get_or_insert(error);
            }
        }
        failure.map_or(Ok(()), Err)
    }

    #[cfg(all(test, unix))]
    fn hold_next_reap(
        &self,
    ) -> (
        tokio::sync::oneshot::Receiver<()>,
        tokio::sync::oneshot::Sender<()>,
    ) {
        let (started, observed) = tokio::sync::oneshot::channel();
        let (release, resume) = tokio::sync::oneshot::channel();
        *self.gate.lock().unwrap() = Some(ReapGate { started, resume });
        (observed, release)
    }
}

#[cfg(unix)]
struct ReapingChild {
    child: Option<Child>,
    group: GroupGuard,
    reaper: Arc<Reaper>,
}

#[cfg(unix)]
impl Drop for ReapingChild {
    fn drop(&mut self) {
        let _ = self.group.terminate();
        if let Some(mut child) = self.child.take() {
            if matches!(child.try_wait(), Ok(Some(_))) {
                return;
            }
            // Preserve kill_on_drop's direct-child fallback if group signalling fails.
            let _ = child.start_kill();
            self.reaper.queue(child);
        }
    }
}

/// Owns the process group's cleanup. The timeout, the normal exit, the error
/// path and `Drop` all terminate through [`GroupGuard::terminate`], which is a
/// no-op once the group has been signalled. A successful signal or a benign
/// ESRCH or confirmed exited group disarms it; a genuine first error leaves it armed for
/// the drop cleanup. `kill_on_drop` alone only signals the shell.
#[cfg(unix)]
pub(super) struct GroupGuard {
    pid: u32,
    armed: bool,
}

#[cfg(unix)]
impl GroupGuard {
    pub(super) fn new(pid: u32) -> Self {
        Self { pid, armed: true }
    }

    pub(super) fn terminate(&mut self) -> Result<(), ToolError> {
        if !self.armed {
            return Ok(());
        }
        // kill_group treats a vanished or confirmed exited group as already done.
        kill_group(self.pid)?;
        self.armed = false;
        Ok(())
    }
}

#[cfg(unix)]
impl Drop for GroupGuard {
    fn drop(&mut self) {
        if self.armed {
            let _ = self.terminate();
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
        // Darwin also reports EPERM when its group iterator finds only zombies.
        #[cfg(target_os = "macos")]
        if error.raw_os_error() == Some(libc::EPERM) && process_group_exited(pid) {
            return Ok(());
        }
        return Err(ToolError::ExecutionFailed(format!(
            "kill process group {pid}: {error}"
        )));
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn process_group_members(pid: u32) -> Option<Vec<libc::pid_t>> {
    let capacity = unsafe { libc::proc_listpgrppids(pid as i32, std::ptr::null_mut(), 0) };
    if capacity <= 0 {
        return None;
    }
    let mut members = vec![0 as libc::pid_t; capacity as usize];
    let Ok(buffer_size) = i32::try_from(std::mem::size_of_val(members.as_slice())) else {
        return None;
    };
    let (count, error) = unsafe {
        // libproc maps query errors to zero, the same result as an empty group.
        *libc::__error() = 0;
        let count = libc::proc_listpgrppids(pid as i32, members.as_mut_ptr().cast(), buffer_size);
        (count, *libc::__error())
    };
    if count < 0 || count as usize >= members.len() || (count == 0 && error != 0) {
        return None;
    }
    members.truncate(count as usize);
    Some(members)
}

#[cfg(target_os = "macos")]
fn process_group_exited(pid: u32) -> bool {
    let Some(members) = process_group_members(pid) else {
        return false;
    };
    for &member in &members {
        let mut info = std::mem::MaybeUninit::<libc::proc_bsdinfo>::uninit();
        let size = unsafe {
            libc::proc_pidinfo(
                member,
                libc::PROC_PIDTBSDINFO,
                0,
                info.as_mut_ptr().cast(),
                std::mem::size_of::<libc::proc_bsdinfo>() as i32,
            )
        };
        if size == 0 && std::io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH) {
            continue;
        }
        if size != std::mem::size_of::<libc::proc_bsdinfo>() as i32 {
            return false;
        }
        let info = unsafe { info.assume_init() };
        if info.pbi_status != libc::SZOMB {
            return false;
        }
    }
    // A member may fork before exiting. Do not accept an unexamined new PID.
    process_group_members(pid)
        .is_some_and(|current| current.iter().all(|member| members.contains(member)))
}

#[cfg(unix)]
async fn bash(args: Value, context: &ToolContext, reaper: Arc<Reaper>) -> Result<Value, ToolError> {
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
        .process_group(0)
        .kill_on_drop(true);
    // Merge stderr into the stdout pipe inside the child so the kernel keeps
    // the write order of both streams. Reading two pipes concurrently appends
    // chunks in completion order, which can put late stderr reads after the
    // final stdout bytes and evict them from the bounded preview tail.
    // SAFETY: `dup2` only runs in the child between fork and exec and operates
    // on the stdio descriptors that std/tokio already configured.
    unsafe {
        command.as_std_mut().pre_exec(|| {
            if libc::dup2(libc::STDOUT_FILENO, libc::STDERR_FILENO) == -1 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let child = command
        .spawn()
        .map_err(|error| ToolError::ExecutionFailed(format!("spawn bash: {error}")))?;
    let pid = child.id().unwrap();
    let mut owned = ReapingChild {
        child: Some(child),
        group: GroupGuard::new(pid),
        reaper,
    };
    let ReapingChild { child, group, .. } = &mut owned;
    let child = child.as_mut().unwrap();
    let mut stdout = child.stdout.take().unwrap();
    let mut stdout_buffer = [0; 8192];
    let mut stdout_open = true;
    let mut exit_status = None;
    let mut timed_out = false;
    let started = Instant::now();
    let deadline = tokio::time::sleep(Duration::from_millis(args.timeout_ms));
    tokio::pin!(deadline);
    let mut output = OutputAccumulator::new();
    let collected: Result<(), ToolError> = async {
        while exit_status.is_none() || stdout_open {
            tokio::select! {
                status = child.wait(), if exit_status.is_none() => {
                    exit_status = Some(status.map_err(|error| ToolError::ExecutionFailed(format!("wait for bash {pid}: {error}")))?);
                    // Background descendants can keep inherited pipes open after the shell exits.
                    group.terminate()?;
                }
                chunk = stdout.read(&mut stdout_buffer), if stdout_open => {
                    let size = chunk?;
                    stdout_open = size != 0;
                    if size != 0 { output.append(&stdout_buffer[..size], &context.output_dir).await?; }
                }
                _ = &mut deadline, if !timed_out => {
                    timed_out = true;
                    group.terminate()?;
                }
            }
        }
        output.finish().await?;
        Ok(())
    }.await;
    if let Err(error) = collected {
        group.terminate()?;
        child
            .wait()
            .await
            .map_err(|error| ToolError::ExecutionFailed(format!("wait for bash {pid}: {error}")))?;
        return Err(error);
    }
    let mut result = json!({"output":output.preview(), "exit_code":exit_status.unwrap().code(),
        "timed_out":timed_out, "truncated":output.path.is_some(), "wall_time_ms":started.elapsed().as_millis()});
    if let Some(path) = output.path {
        result["output_path"] = json!(path);
    }
    Ok(result)
}

#[cfg(not(unix))]
async fn bash(_: Value, _: &ToolContext, _: Arc<Reaper>) -> Result<Value, ToolError> {
    Err(ToolError::ExecutionFailed(
        "bash currently requires a Unix execution environment".to_owned(),
    ))
}

pub fn init(context: Arc<ToolContext>) -> Tool {
    init_with_reaper(context, Arc::new(Reaper::default()))
}

pub(super) fn register_builtin(registry: &mut ToolRegistry, context: Arc<ToolContext>) {
    register_with_reaper(registry, context, Arc::new(Reaper::default()));
}

fn register_with_reaper(
    registry: &mut ToolRegistry,
    context: Arc<ToolContext>,
    reaper: Arc<Reaper>,
) {
    registry.registry(init_with_reaper(context, reaper.clone()));
    registry.add_task_cleanup(move || {
        let reaper = reaper.clone();
        Box::pin(async move { reaper.finish().await })
    });
}

fn init_with_reaper(context: Arc<ToolContext>, reaper: Arc<Reaper>) -> Tool {
    Tool {
        definition: ToolDefinition {
            tool_type: ToolType::Function,
            name: "bash".to_owned(),
            description: "Run a non-interactive bash command and wait for completion. workdir defaults to the fixed working directory. timeout_ms defaults to 120000; timed-out commands and their process group are terminated. No managed background processes or stdin sessions. Returns stdout/stderr merged in write order, exit_code, and timed_out. Preview is limited to the last 2000 lines / 50 KiB; larger output is saved at output_path for read or rg. Use for searches, tests, and batch scripts.".to_owned(),
            parameters: json!({"type":"object", "properties":{
                "command":{"type":"string"}, "workdir":{"type":"string"},
                "timeout_ms":{"type":"integer","minimum":1}}, "required":["command"], "additionalProperties":false}),
        },
        handler: Arc::new(move |args| { let context = context.clone(); let reaper = reaper.clone(); Box::pin(async move { bash(args, &context, reaper).await }) }),
    }
}

#[cfg(all(test, target_os = "macos"))]
#[path = "../../tests/tools/bash.rs"]
mod tests;

#[cfg(all(test, unix))]
#[path = "../../tests/tools/bash_cleanup.rs"]
mod cleanup_tests;
