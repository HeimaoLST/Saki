#![cfg(unix)]
//! TTY-only lifecycle fixture. The child test enters a real `TerminalSession`
//! on a PTY and then panics; the parent drains the PTY concurrently and
//! verifies the PTY is back in cooked mode. Run explicitly:
//! `cargo test --bin Saki frontend::tui::tests::pty::pty_panic_restores_terminal -- --ignored --exact`

use std::os::fd::FromRawFd;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[test]
#[ignore = "TTY-only lifecycle fixture: run with --ignored --exact frontend::tui::tests::pty::pty_panic_restores_terminal"]
fn pty_panic_restores_terminal() {
    unsafe {
        let (mut master, mut slave): (libc::c_int, libc::c_int) = (-1, -1);
        assert_eq!(
            libc::openpty(
                &mut master,
                &mut slave,
                std::ptr::null_mut(),
                std::ptr::null_mut(),
                std::ptr::null_mut(),
            ),
            0,
            "openpty"
        );
        for fd in [master, slave] {
            libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
        }
        let mut size: libc::winsize = std::mem::zeroed();
        size.ws_row = 24;
        size.ws_col = 80;
        libc::ioctl(slave, libc::TIOCSWINSZ, &size);

        // Drain the master while the child runs; otherwise a panic backtrace
        // can fill the PTY buffer and block the child before it exits.
        let reader = std::thread::spawn(move || {
            let mut master = std::fs::File::from_raw_fd(master);
            let mut output = Vec::new();
            let _ = std::io::Read::read_to_end(&mut master, &mut output);
            output
        });

        let child_in = libc::dup(slave);
        let child_out = libc::dup(slave);
        let child_err = libc::dup(slave);
        let mut child = Command::new(std::env::current_exe().unwrap())
            .args([
                "--ignored",
                "--exact",
                "--test-threads=1",
                "--nocapture",
                "frontend::tui::tests::pty::pty_panic_child",
            ])
            .env_remove("SAKI_THEME")
            .stdin(Stdio::from_raw_fd(child_in))
            .stdout(Stdio::from_raw_fd(child_out))
            .stderr(Stdio::from_raw_fd(child_err))
            .spawn()
            .expect("spawn PTY child");

        // Bounded wait so a wedged child cannot hang the fixture forever.
        let deadline = Instant::now() + Duration::from_secs(30);
        let status = loop {
            match child.try_wait().expect("try_wait") {
                Some(status) => break status,
                None if Instant::now() >= deadline => {
                    let _ = child.kill();
                    let _ = child.wait();
                    libc::close(slave);
                    let _ = reader.join();
                    panic!("PTY child did not exit within the bounded wait");
                }
                None => std::thread::sleep(Duration::from_millis(25)),
            }
        };
        assert!(!status.success(), "the child test panics by design");

        let mut termios: libc::termios = std::mem::zeroed();
        assert_eq!(libc::tcgetattr(slave, &mut termios), 0, "tcgetattr");
        assert_ne!(
            termios.c_lflag & libc::ICANON,
            0,
            "ICANON must be restored after panic"
        );
        assert_ne!(
            termios.c_lflag & libc::ECHO,
            0,
            "ECHO must be restored after panic"
        );
        // Closing the last slave descriptor lets the drain thread reach EOF.
        libc::close(slave);
        let output = reader.join().expect("PTY drain thread");
        let text = String::from_utf8_lossy(&output);
        assert!(
            text.contains("pty lifecycle panic"),
            "child panic output captured: {text}"
        );
    }
}

#[test]
#[ignore = "TTY-only lifecycle child invoked by pty_panic_restores_terminal"]
fn pty_panic_child() {
    let _session = crate::frontend::tui::terminal::TerminalSession::enter(
        crate::frontend::tui::theme::ThemeSetting::Light,
    )
    .expect("enter terminal session on the PTY");
    panic!("pty lifecycle panic");
}
