use super::*;
use std::os::unix::process::CommandExt;
use std::process::{Child, Command as StdCommand, Stdio};

struct ChildCleanup {
    child: Option<Child>,
    pgid: u32,
}

impl ChildCleanup {
    fn new(child: Child) -> Self {
        let pgid = child.id();
        Self {
            child: Some(child),
            pgid,
        }
    }

    fn pid(&self) -> u32 {
        self.pgid
    }

    fn take(&mut self) -> Child {
        self.child.take().expect("child was already reaped")
    }
}

impl Drop for ChildCleanup {
    fn drop(&mut self) {
        if let Some(mut child) = self.child.take() {
            unsafe {
                libc::kill(-(self.pgid as i32), libc::SIGKILL);
            }
            let _ = child.wait();
        }
    }
}

#[test]
fn macos_zombie_only_group_eperm_counts_as_cleaned() {
    let child = StdCommand::new("/bin/sh")
        .arg("-c")
        .arg("exit 0")
        .process_group(0)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn shell in its own process group");
    let mut cleanup = ChildCleanup::new(child);
    let pid = cleanup.pid();

    // WNOWAIT keeps a zombie in the group until the cleanup fixture reaps it.
    let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
    loop {
        let result = unsafe {
            libc::waitid(
                libc::P_PID,
                pid as libc::id_t,
                &mut info,
                libc::WEXITED | libc::WNOWAIT,
            )
        };
        if result == 0 {
            break;
        }
        let error = std::io::Error::last_os_error();
        assert_eq!(
            error.raw_os_error(),
            Some(libc::EINTR),
            "waitid failed for {pid}: {error}"
        );
    }

    // Record the platform's raw behavior for the log, without asserting a
    // particular kernel errno forever: the production path below is the core
    // contract. A future macOS may legitimately return success here.
    let raw = unsafe { libc::kill(-(pid as i32), libc::SIGKILL) };
    let raw_errno = if raw == -1 {
        std::io::Error::last_os_error().raw_os_error()
    } else {
        None
    };
    eprintln!("zombie-only group pid={pid}: raw kill result={raw} errno={raw_errno:?}");

    assert!(
        process_group_exited(pid),
        "a group that contains only the exited leader must count as exited"
    );

    let mut group = GroupGuard { pid, armed: true };
    group
        .terminate()
        .expect("terminating an exited group must be benign");
    assert!(!group.armed, "a benign termination must disarm the guard");
    group
        .terminate()
        .expect("a disarmed guard must be terminable again");

    let status = cleanup.take().wait().expect("reap the exited shell");
    assert!(
        status.success(),
        "the shell should have exited successfully"
    );
}

#[test]
fn macos_live_process_group_is_not_reported_as_exited() {
    let mut child = StdCommand::new("/bin/sh")
        .arg("-c")
        .arg("read -r line")
        .process_group(0)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn a shell blocked on stdin");
    assert!(
        child.try_wait().expect("probe shell").is_none(),
        "shell must still be running before the liveness check"
    );
    let stdin = child.stdin.take().expect("piped stdin");
    let mut cleanup = ChildCleanup::new(child);
    let pid = cleanup.pid();

    assert!(
        !process_group_exited(pid),
        "a live process group must not be reported as exited"
    );

    let mut group = GroupGuard { pid, armed: true };
    group.terminate().expect("kill the live process group");
    assert!(!group.armed, "the successful kill must disarm the guard");
    group
        .terminate()
        .expect("a disarmed guard must be terminable again");

    let status = cleanup.take().wait().expect("reap the killed shell");
    assert!(
        !status.success(),
        "the killed shell must not report success"
    );
    drop(stdin);
}
