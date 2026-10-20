//! External editor handoff for the composer (Ctrl+G).
//!
//! The configured program is parsed with `shlex` and spawned directly with the
//! temp file as its last argument. There is no shell, expansion, or guessed
//! fallback. The terminal suspension/restoration lives in `TerminalSession` so
//! this module only owns the file and child lifecycle.

use std::path::PathBuf;
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
    let path = temp_path();
    if let Err(error) = std::fs::write(&path, draft.as_bytes()) {
        return Err(format!("Could not create the editor file: {error}"));
    }
    let status = run(launch, &path);
    let contents = std::fs::read(&path).ok();
    let _ = std::fs::remove_file(&path);
    status?;
    let contents = contents.ok_or_else(|| "Could not read the editor file.".to_owned())?;
    String::from_utf8(contents)
        .map_err(|_| "External editor saved text that is not valid UTF-8.".to_owned())
}

fn temp_path() -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |duration| duration.as_nanos());
    std::env::temp_dir().join(format!("saki-editor-{}-{nanos}.txt", std::process::id()))
}

fn run(launch: &Launch, path: &std::path::Path) -> Result<(), String> {
    // In cooked mode Ctrl+C reaches the whole foreground group (Saki and the
    // editor). Catch it here so the parent survives; a caught action is reset
    // by exec in the child, so the editor still behaves normally.
    #[cfg(unix)]
    let _sigint = SigintGuard::install();
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

#[cfg(unix)]
struct SigintGuard {
    previous: libc::sighandler_t,
}

#[cfg(unix)]
impl SigintGuard {
    fn install() -> Self {
        extern "C" fn catch(_: libc::c_int) {}
        let previous =
            unsafe { libc::signal(libc::SIGINT, catch as *const () as libc::sighandler_t) };
        Self { previous }
    }
}

#[cfg(unix)]
impl Drop for SigintGuard {
    fn drop(&mut self) {
        unsafe {
            libc::signal(libc::SIGINT, self.previous);
        }
    }
}
