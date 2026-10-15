use super::*;
use serde_json::{Value, json};
use std::{
    path::PathBuf,
    sync::atomic::{AtomicUsize, Ordering},
};
use tool_registry::ToolCall;

struct Workspace {
    path: PathBuf,
    registry: ToolRegistry,
}

impl Workspace {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "saki-tools-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&path).unwrap();
        let mut registry = ToolRegistry::new();
        register_builtins(
            &mut registry,
            ToolContext {
                cwd: path.clone(),
                output_dir: path.join("output"),
            },
        );
        Self { path, registry }
    }

    async fn call(&self, name: &str, args: Value) -> Result<Value, ToolError> {
        self.registry
            .execution_tool(ToolCall::new("test".to_owned(), name.to_owned(), args))
            .await
    }
}

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.path);
    }
}

#[tokio::test]
async fn read_pages_multibyte_crlf_text_without_losing_original_content() {
    let workspace = Workspace::new();
    let original = (0..25)
        .map(|i| format!("{i}:{}\r\n", "你".repeat(1000)))
        .collect::<String>();
    std::fs::write(workspace.path.join("text"), &original).unwrap();
    let mut content = String::new();
    let mut offset = 1;
    loop {
        let page = workspace
            .call("read", json!({"path":"text","offset":offset,"limit":10000}))
            .await
            .unwrap();
        let text = page["content"].as_str().unwrap();
        assert!(text.len() <= output::MAX_OUTPUT_BYTES);
        assert!(text.lines().count() <= output::MAX_OUTPUT_LINES);
        content.push_str(text);
        match page["next_offset"].as_u64() {
            Some(next) => {
                assert!(next > offset);
                offset = next;
            }
            None => break,
        }
    }
    assert_eq!(content, original);
}

#[tokio::test]
async fn read_handles_empty_eof_and_oversized_lines_without_fake_continuation() {
    let workspace = Workspace::new();
    std::fs::write(workspace.path.join("empty"), "").unwrap();
    std::fs::write(
        workspace.path.join("long"),
        "x".repeat(output::MAX_OUTPUT_BYTES + 1),
    )
    .unwrap();
    for offset in [1, 2, 100] {
        let result = workspace
            .call("read", json!({"path":"empty","offset":offset}))
            .await
            .unwrap();
        assert_eq!(result["content"], "");
        assert_eq!(result["truncated"], false);
        assert!(result["next_offset"].is_null());
    }
    let long = workspace
        .call("read", json!({"path":"long"}))
        .await
        .unwrap();
    assert_eq!(long["truncated"], true);
    assert!(long["next_offset"].is_null());
    assert!(long["note"].as_str().unwrap().contains("bash"));
    assert!(
        workspace
            .call("read", json!({"path":"empty","offset":0}))
            .await
            .is_err()
    );
}

#[tokio::test]
async fn edit_validates_all_replacements_before_mutation() {
    let workspace = Workspace::new();
    let original = "alpha beta\nrepeat repeat\naaa\n";
    let path = workspace.path.join("source");
    std::fs::write(&path, original).unwrap();
    let cases = [
        json!([{"oldText":"alpha","newText":"changed"},{"oldText":"missing","newText":"x"}]),
        json!([{"oldText":"repeat","newText":"x"}]),
        json!([{"oldText":"alpha beta","newText":"x"},{"oldText":"beta","newText":"y"}]),
        json!([{"oldText":"aa","newText":"x"}]),
        json!([{"oldText":"","newText":"x"}]),
    ];
    for edits in cases {
        assert!(
            workspace
                .call("edit", json!({"path":"source","edits":edits}))
                .await
                .is_err()
        );
        assert_eq!(std::fs::read_to_string(&path).unwrap(), original);
    }
}

#[tokio::test]
async fn edit_matches_original_and_preserves_bom_unicode_and_untouched_mixed_endings() {
    let workspace = Workspace::new();
    let original = "\u{feff}标题\r\nfirst=1\r\nkeep=原样\nlast=2\n";
    let path = workspace.path.join("source");
    std::fs::write(&path, original).unwrap();
    let result = workspace
        .call(
            "edit",
            json!({"path":"source","edits":[
                {"oldText":"last=2\n","newText":"last=first=1\n"},
                {"oldText":"first=1\n","newText":"first=9\nextra=值\n"}
            ]}),
        )
        .await
        .unwrap();
    assert_eq!(result["applied"], 2);
    assert_eq!(
        std::fs::read_to_string(path).unwrap(),
        "\u{feff}标题\r\nfirst=9\r\nextra=值\r\nkeep=原样\nlast=first=1\n"
    );
}

#[cfg(unix)]
#[tokio::test]
async fn write_and_edit_follow_symlinks_and_preserve_executable_mode() {
    use std::os::unix::fs::{PermissionsExt, symlink};
    let workspace = Workspace::new();
    let target = workspace.path.join("script");
    std::fs::write(&target, "old").unwrap();
    std::fs::set_permissions(&target, std::fs::Permissions::from_mode(0o755)).unwrap();
    symlink(&target, workspace.path.join("link")).unwrap();
    workspace
        .call("write", json!({"path":"link","content":"new"}))
        .await
        .unwrap();
    workspace
        .call(
            "edit",
            json!({"path":"link","edits":[{"oldText":"new","newText":"edited"}]}),
        )
        .await
        .unwrap();
    assert!(
        std::fs::symlink_metadata(workspace.path.join("link"))
            .unwrap()
            .file_type()
            .is_symlink()
    );
    assert_eq!(
        std::fs::metadata(&target).unwrap().permissions().mode() & 0o777,
        0o755
    );
    assert_eq!(std::fs::read_to_string(&target).unwrap(), "edited");
}

#[tokio::test]
async fn write_creates_parent_directories_and_read_recovers_content() {
    let workspace = Workspace::new();
    workspace
        .call(
            "write",
            json!({"path":"nested/dir/file","content":"one\ntwo\n"}),
        )
        .await
        .unwrap();
    let read = workspace
        .call(
            "read",
            json!({"path":"nested/dir/file","offset":2,"limit":1}),
        )
        .await
        .unwrap();
    assert_eq!(read["content"], "two\n");
    assert!(read["next_offset"].is_null());
}

#[cfg(unix)]
#[tokio::test]
async fn bash_returns_nonzero_status_and_workdir_does_not_change_file_tool_cwd() {
    let workspace = Workspace::new();
    std::fs::create_dir(workspace.path.join("sub")).unwrap();
    let result = workspace.call("bash", json!({"command":"printf stdout; printf stderr >&2; printf local > result; exit 7","workdir":"sub"})).await.unwrap();
    assert_eq!(result["exit_code"], 7);
    assert_eq!(result["timed_out"], false);
    assert!(result["output"].as_str().unwrap().contains("stdout"));
    assert!(result["output"].as_str().unwrap().contains("stderr"));
    assert!(result.get("output_path").is_none());
    let read = workspace
        .call("read", json!({"path":"sub/result"}))
        .await
        .unwrap();
    assert_eq!(read["content"], "local");
    assert!(!workspace.path.join("output").exists());
}

#[cfg(unix)]
#[tokio::test]
async fn bash_preserves_stdout_stderr_write_order() {
    let workspace = Workspace::new();
    let result = workspace
        .call(
            "bash",
            json!({"command":"printf out1; printf err1 >&2; printf out2; printf err2 >&2"}),
        )
        .await
        .unwrap();
    assert_eq!(result["output"], "out1err1out2err2");
}

#[cfg(unix)]
#[tokio::test]
async fn bash_drains_both_pipes_and_large_output_is_complete_and_readable() {
    let workspace = Workspace::new();
    let command = "python3 -c 'import sys; sys.stderr.write(\"stderr\\n\"*20000); sys.stdout.write(\"stdout\\n\"*20000); print(\"FINAL_MARKER\")'";
    let result = workspace
        .call("bash", json!({"command":command,"timeout_ms":10000}))
        .await
        .unwrap();
    assert_eq!(result["exit_code"], 0);
    assert_eq!(result["timed_out"], false);
    assert_eq!(result["truncated"], true);
    let preview = result["output"].as_str().unwrap();
    assert!(preview.len() <= output::MAX_OUTPUT_BYTES);
    assert!(preview.lines().count() <= output::MAX_OUTPUT_LINES);
    assert!(preview.contains("FINAL_MARKER"));
    let path = result["output_path"].as_str().unwrap();
    let full = std::fs::read_to_string(path).unwrap();
    assert_eq!(full.lines().filter(|line| *line == "stdout").count(), 20000);
    assert_eq!(full.lines().filter(|line| *line == "stderr").count(), 20000);
    let read = workspace
        .call("read", json!({"path":path,"offset":40001}))
        .await
        .unwrap();
    assert_eq!(read["content"], "FINAL_MARKER\n");
}

#[cfg(unix)]
#[tokio::test]
async fn line_limit_alone_spills_output_and_can_be_paged() {
    let workspace = Workspace::new();
    let result = workspace
        .call(
            "bash",
            json!({"command":"python3 -c 'print(\"x\\n\"*2001, end=\"\")'"}),
        )
        .await
        .unwrap();
    assert_eq!(result["truncated"], true);
    assert_eq!(result["output"].as_str().unwrap().lines().count(), 2000);
    let path = result["output_path"].as_str().unwrap();
    let first = workspace.call("read", json!({"path":path})).await.unwrap();
    assert_eq!(first["next_offset"], 2001);
    let last = workspace
        .call("read", json!({"path":path,"offset":2001}))
        .await
        .unwrap();
    assert_eq!(last["content"], "x\n");
    assert_eq!(last["truncated"], false);
}

#[cfg(unix)]
#[tokio::test]
async fn binary_output_keeps_a_bounded_text_preview_and_original_bytes() {
    let workspace = Workspace::new();
    let result = workspace.call("bash", json!({"command":"python3 -c 'import sys; sys.stdout.buffer.write(bytes([255])*30000)'"})).await.unwrap();
    assert_eq!(result["exit_code"], 0);
    assert!(result["output"].as_str().unwrap().len() <= output::MAX_OUTPUT_BYTES);
    assert_eq!(result["truncated"], true);
    assert_eq!(
        std::fs::read(result["output_path"].as_str().unwrap()).unwrap(),
        vec![255; 30000]
    );
}

#[cfg(unix)]
#[tokio::test]
async fn bash_timeout_terminates_descendants_and_preserves_captured_output() {
    let workspace = Workspace::new();
    let result = workspace.call("bash", json!({"command":"sleep 60 & child=$!; printf '%s' \"$child\" > child.pid; printf READY; wait","timeout_ms":1000})).await.unwrap();
    assert_eq!(result["timed_out"], true);
    assert!(result["output"].as_str().unwrap().contains("READY"));
    let pid = std::fs::read_to_string(workspace.path.join("child.pid")).unwrap();
    let status = tokio::process::Command::new("/bin/ps")
        .args(["-o", "stat=", "-p", &pid])
        .output()
        .await
        .unwrap();
    let state = String::from_utf8(status.stdout).unwrap();
    assert!(
        state.trim().is_empty() || state.trim().starts_with('Z'),
        "descendant is still running: {state}"
    );
}

#[cfg(unix)]
#[tokio::test]
async fn completed_shell_does_not_wait_for_background_descendants_holding_pipes() {
    let workspace = Workspace::new();
    let result = workspace
        .call(
            "bash",
            json!({"command":"sleep 60 & printf DONE","timeout_ms":1000}),
        )
        .await
        .unwrap();
    assert_eq!(result["timed_out"], false);
    assert_eq!(result["exit_code"], 0);
    assert_eq!(result["output"], "DONE");
}

#[cfg(unix)]
#[tokio::test]
async fn output_file_failure_terminates_the_command_group() {
    let workspace = Workspace::new();
    std::fs::write(workspace.path.join("output"), "not a directory").unwrap();
    let error = workspace.call("bash", json!({"command":"sleep 60 & printf '%s' \"$!\" > child.pid; python3 -c 'print(\"x\"*60000)'; wait"})).await.unwrap_err();
    assert!(matches!(error, ToolError::ExecutionFailed(_)));
    let pid = std::fs::read_to_string(workspace.path.join("child.pid")).unwrap();
    let status = tokio::process::Command::new("/bin/ps")
        .args(["-o", "stat=", "-p", &pid])
        .output()
        .await
        .unwrap();
    let state = String::from_utf8(status.stdout).unwrap();
    assert!(
        state.trim().is_empty() || state.trim().starts_with('Z'),
        "descendant survived output failure: {state}"
    );
}

#[cfg(unix)]
#[tokio::test]
async fn dropping_a_running_bash_future_kills_descendants() {
    let workspace = Workspace::new();
    let call = ToolCall::new(
        "test".to_owned(),
        "bash".to_owned(),
        json!({"command":"sleep 60 & printf '%s' \"$!\" > child.pid; printf READY; wait"}),
    );
    let mut future = Box::pin(workspace.registry.execution_tool(call));
    let pid = loop {
        tokio::select! {
            _ = &mut future => panic!("bash finished before cancellation"),
            _ = tokio::time::sleep(tokio::time::Duration::from_millis(10)) => {
                if let Ok(pid) = std::fs::read_to_string(workspace.path.join("child.pid")) {
                    let pid = pid.trim().to_owned();
                    if !pid.is_empty() {
                        break pid;
                    }
                }
            }
        }
    };
    // Dropping the execution future must kill the whole process group, not
    // just the shell; this is the cancellation path used by Esc.
    drop(future);
    let deadline = tokio::time::Instant::now() + tokio::time::Duration::from_secs(5);
    loop {
        let output = tokio::process::Command::new("/bin/ps")
            .args(["-o", "stat=", "-p", &pid])
            .output()
            .await
            .unwrap();
        let state = String::from_utf8_lossy(&output.stdout).trim().to_owned();
        if state.is_empty() || state.starts_with('Z') {
            break;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "descendant {pid} survived cancellation: {state}"
        );
        tokio::time::sleep(tokio::time::Duration::from_millis(20)).await;
    }
}
