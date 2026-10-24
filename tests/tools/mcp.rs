use super::*;
use crate::{agent::journal, tools::tool_registry::ToolCall};
use rmcp::model::CallToolResult;

fn context() -> ToolContext {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let cwd = std::env::temp_dir().join(format!(
        "saki-mcp-output-{}-{}",
        journal::new_id().unwrap(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir(&cwd).unwrap();
    ToolContext {
        output_dir: cwd.join("outputs"),
        cwd,
    }
}

#[tokio::test]
async fn spilled_structured_result_is_exactly_recoverable_through_selected_read() {
    let context = context();
    let mut result = CallToolResult::structured(
        json!({"rows":(0..4000).map(|index|format!("原始資料-{index}")).collect::<Vec<_>>()}),
    );
    result.content = vec![rmcp::model::ContentBlock::text("4000 structured rows")];
    let expected = serde_json::to_value(&result).unwrap();
    let output = convert_result(result, &context, true, false).await.unwrap();
    assert_eq!(output["readback_tool"], "read");
    assert_eq!(output["readback_available"], true);
    assert!(output["preview"].as_str().unwrap().len() <= MAX_OUTPUT_BYTES);
    let read = crate::tools::read::init(Arc::new(ToolContext {
        cwd: context.cwd.clone(),
        output_dir: context.output_dir.clone(),
    }));
    let mut registry = ToolRegistry::new();
    registry.registry(read);
    let mut recovered = String::new();
    let mut offset = 1;
    loop {
        let page = registry
            .execution_tool(ToolCall::new(
                "readback".into(),
                "read".into(),
                json!({"path":output["output_path"],"offset":offset}),
            ))
            .await
            .unwrap();
        recovered.push_str(page["content"].as_str().unwrap());
        match page["next_offset"].as_u64() {
            Some(next) => offset = next,
            None => break,
        }
    }
    assert_eq!(serde_json::from_str::<Value>(&recovered).unwrap(), expected);
    assert_eq!(
        std::fs::read_to_string(output["output_path"].as_str().unwrap()).unwrap(),
        recovered
    );
    std::fs::remove_dir_all(context.cwd).unwrap();
}

#[tokio::test]
async fn oversized_lines_require_bash_and_never_advertise_unusable_read_continuation() {
    let context = context();
    let result = CallToolResult::success(vec![rmcp::model::ContentBlock::text("界".repeat(30000))]);
    let read_only = convert_result(result.clone(), &context, true, false)
        .await
        .unwrap();
    assert_eq!(read_only["readback_available"], false);
    assert!(read_only["readback_tool"].is_null());
    let with_bash = convert_result(result.clone(), &context, true, true)
        .await
        .unwrap();
    assert_eq!(with_bash["readback_tool"], "bash");
    let no_tools = convert_result(result, &context, false, false)
        .await
        .unwrap();
    assert_eq!(no_tools["readback_available"], false);
    let tool = crate::tools::bash::init(Arc::new(ToolContext {
        cwd: context.cwd.clone(),
        output_dir: context.output_dir.clone(),
    }));
    let command = format!(
        "dd if='{}' bs=1024 count=1 2>/dev/null",
        with_bash["output_path"].as_str().unwrap()
    );
    let slice = (tool.handler)(json!({"command":command})).await.unwrap();
    let bytes = std::fs::read(with_bash["output_path"].as_str().unwrap()).unwrap();
    assert_eq!(
        slice["output"],
        String::from_utf8_lossy(&bytes[..1024]).as_ref()
    );
    std::fs::remove_dir_all(context.cwd).unwrap();
}

#[tokio::test]
async fn unsupported_content_is_explicit_and_small_business_errors_are_preserved() {
    let context = context();
    let image: CallToolResult = serde_json::from_value(
        json!({"content":[{"type":"image","data":"AA==","mimeType":"image/png"}]}),
    )
    .unwrap();
    let error = convert_result(image, &context, true, true)
        .await
        .unwrap_err();
    assert!(
        error
            .to_string()
            .contains("unsupported image/audio/resource")
    );
    let business =
        CallToolResult::error(vec![rmcp::model::ContentBlock::text("user record missing")]);
    let expected = serde_json::to_value(&business).unwrap();
    assert_eq!(
        convert_result(business, &context, false, false)
            .await
            .unwrap(),
        expected
    );
    assert!(!context.output_dir.exists());
    std::fs::remove_dir_all(context.cwd).unwrap();
}
