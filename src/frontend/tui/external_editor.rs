//! External editor handoff for the composer (Ctrl+G).
//!
//! The configured program is parsed with `shlex` and spawned directly with the
//! temp file as its last argument. There is no shell, expansion, or guessed
//! fallback. The terminal suspension/restoration lives in `TerminalSession` so
//! this module only owns the file and child lifecycle.

use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{SystemTime, UNIX_EPOCH};

/// A parsed `VISUAL`/`EDITOR` command. Arguments keep their shlex boundaries,
/// so quoted executable paths and arguments work.
pub struct Launch {
    pub(crate) program: PathBuf,
    pub(crate) args: Vec<String>,
}

/// Resolves the first nonempty `VISUAL`, then `EDITOR`. Missing configuration
/// and malformed quoting are errors: no `vi` fallback is invented.
pub fn resolve(visual: Option<&str>, editor: Option<&str>) -> Result<Launch, String> {
    let command = [visual, editor]
        .into_iter()
        .flatten()
        .map(str::trim)
        .find(|command| !command.is_empty())
        .ok_or_else(|| "Set VISUAL or EDITOR to use the external editor.".to_owned())?;
    let mut parts =
        shlex::split(command).ok_or_else(|| "External editor command is malformed.".to_owned())?;
    if parts.is_empty() {
        return Err("External editor command is empty.".to_owned());
    }
    let program = PathBuf::from(parts.remove(0));
    Ok(Launch {
        program,
        args: parts,
    })
}

pub fn from_env() -> Result<Launch, String> {
    let visual = std::env::var("VISUAL").ok();
    let editor = std::env::var("EDITOR").ok();
    resolve(visual.as_deref(), editor.as_deref())
}

/// Writes the draft, runs the editor, and returns the new UTF-8 text. Any
/// failure leaves the caller's draft untouched; the temp file is always removed.
pub fn edit(launch: &Launch, draft: &str) -> Result<String, String> {
    let file = TempFile::create(draft)?;
    run(launch, file.path())?;
    let contents = std::fs::read(file.path())
        .map_err(|error| format!("Could not read the editor file: {error}"))?;
    String::from_utf8(contents)
        .map_err(|_| "External editor saved text that is not valid UTF-8.".to_owned())
}

/// Owns the temp file; every return/error path removes it.
struct TempFile {
    path: PathBuf,
}

impl TempFile {
    fn create(draft: &str) -> Result<Self, String> {
        let path = temp_path();
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&path)
            .map_err(|error| format!("Could not create the editor file: {error}"))?;
        if let Err(error) = file.write_all(draft.as_bytes()) {
            let _ = std::fs::remove_file(&path);
            return Err(format!("Could not write the editor file: {error}"));
        }
        Ok(Self { path })
    }

    fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for TempFile {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.path);
    }
}

fn temp_path() -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |duration| duration.as_nanos());
    std::env::temp_dir().join(format!("saki-editor-{}-{nanos}.txt", std::process::id()))
}

fn run(launch: &Launch, path: &Path) -> Result<(), String> {
    let status = Command::new(&launch.program)
        .args(&launch.args)
        .arg(path)
        .status()
        .map_err(|error| format!("Could not start the external editor: {error}"))?;
    if status.success() {
        Ok(())
    } else {
        Err("External editor did not exit successfully.".to_owned())
    }
}

/// Process-global SIGINT guard for the UI invocation only. In cooked mode a
/// terminal Ctrl+C reaches the whole foreground group (Saki and the editor);
/// catching it here keeps the parent alive while exec resets the child to the
/// default action. The exact prior sigaction (handler, flags, mask) is restored.
#[cfg(unix)]
pub struct SigintGuard {
    previous: libc::sigaction,
}

#[cfg(unix)]
impl SigintGuard {
    pub fn install() -> Result<Self, String> {
        extern "C" fn catch(_: libc::c_int) {}
        unsafe {
            let mut action: libc::sigaction = std::mem::zeroed();
            action.sa_sigaction = catch as *const () as libc::sighandler_t;
            action.sa_flags = libc::SA_RESTART;
            libc::sigemptyset(&mut action.sa_mask);
            let mut previous: libc::sigaction = std::mem::zeroed();
            if libc::sigaction(libc::SIGINT, &action, &mut previous) != 0 {
                return Err(format!(
                    "Could not install the SIGINT guard: {}",
                    std::io::Error::last_os_error()
                ));
            }
            Ok(Self { previous })
        }
    }
}

#[cfg(unix)]
impl Drop for SigintGuard {
    fn drop(&mut self) {
        unsafe {
            libc::sigaction(libc::SIGINT, &self.previous, std::ptr::null_mut());
        }
    }
}
