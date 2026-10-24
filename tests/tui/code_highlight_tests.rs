use super::*;
use crate::frontend::tui::code_highlight;
use crate::frontend::tui::markdown;
use crate::frontend::tui::theme::Theme;
use ratatui::style::Modifier;

fn assistant_with(text: &str) -> App {
    let mut app = App::new();
    app.theme = Theme::dark();
    app.push(assistant(text));
    app.last_width = 80;
    app.last_height = 30;
    app
}

fn styled_spans(app: &mut App, width: usize) -> Vec<(String, ratatui::style::Style)> {
    view::record_layout(app, 0, width)
        .into_iter()
        .flat_map(|line| line.line.spans.into_iter())
        .map(|span| (span.content.to_string(), span.style))
        .collect()
}

fn has_span(
    spans: &[(String, ratatui::style::Style)],
    text: &str,
    fg: ratatui::style::Color,
    modifier: Modifier,
) -> bool {
    spans.iter().any(|(content, style)| {
        content.contains(text) && style.fg == Some(fg) && style.add_modifier.contains(modifier)
    })
}

#[test]
fn rust_block_gets_keyword_string_number_and_multiline_comment_roles() {
    let text = "```rust\n// line one\nlet x = \"hi\";\n/* block\n   inner */\nfn main() { let n = 42; }\n```";
    let mut app = assistant_with(text);
    let theme = app.theme;
    let spans = styled_spans(&mut app, 80);
    assert!(has_span(&spans, "let", theme.accent, Modifier::BOLD));
    assert!(has_span(&spans, "fn", theme.accent, Modifier::BOLD));
    assert!(has_span(&spans, "\"hi\"", theme.code, Modifier::empty()));
    assert!(has_span(&spans, "42", theme.warning, Modifier::empty()));
    assert!(has_span(
        &spans,
        "line one",
        theme.secondary,
        Modifier::ITALIC
    ));
    assert!(has_span(
        &spans,
        "inner */",
        theme.secondary,
        Modifier::ITALIC
    ));
}

#[test]
fn neighboring_blocks_reset_state_and_incomplete_fence_stays_usable() {
    let text = "```rust\n/* open comment\n```\n\n```rust\nfn second() {}\n```";
    let mut app = assistant_with(text);
    let theme = app.theme;
    let spans = styled_spans(&mut app, 80);
    assert!(
        has_span(&spans, "fn", theme.accent, Modifier::BOLD),
        "the second block must start from a fresh parse state"
    );
    assert!(
        has_span(&spans, "second", theme.text, Modifier::empty()),
        "second-block text is not swallowed by the first block"
    );

    // Streaming: an incomplete fence parses at flush, then appending keeps it usable.
    let mut app = assistant_with("```python\ndef open(");
    let theme = app.theme;
    let spans = styled_spans(&mut app, 80);
    assert!(has_span(&spans, "def", theme.accent, Modifier::BOLD));
    let text = "```python\ndef open(name):\n    return name\n```";
    let mut app = assistant_with(text);
    let spans = styled_spans(&mut app, 80);
    assert!(has_span(&spans, "def", theme.accent, Modifier::BOLD));
    assert!(has_span(&spans, "return", theme.accent, Modifier::BOLD));
}

#[test]
fn unknown_unlabelled_and_indented_blocks_keep_label_text_and_one_color() {
    let theme = Theme::dark();
    for text in [
        "```weirdlang\nlet x = 1;\n```",
        "```\nlet x = 1;\n```",
        "    let x = 1;\n",
    ] {
        let mut app = App::new();
        app.theme = theme;
        app.push(assistant(text));
        let spans = styled_spans(&mut app, 80);
        if text.starts_with("```weirdlang") {
            assert!(
                spans
                    .iter()
                    .any(|(content, _)| content.contains("weirdlang")),
                "unknown language label stays visible"
            );
        }
        assert!(
            !has_span(&spans, "let", theme.accent, Modifier::BOLD),
            "fallback blocks must not get syntax colors"
        );
        assert!(spans.iter().any(|(content, style)| {
            content.contains("let x = 1;") && style.fg == Some(theme.code)
        }));
        assert!(
            markdown::code_blocks(text)
                .iter()
                .any(|block| block.contains("let x = 1;"))
        );
    }
}

#[test]
fn narrow_wide_reflow_keeps_monotonic_sources_and_raw_copy() {
    let text = "> - item\n>   ```rust\n>   let value = \"wide text that wraps a lot\";\n>   ```\n";
    let mut app = assistant_with(text);
    let original = match &app.records[0] {
        Record::Assistant { text } => text.clone(),
        other => panic!("expected assistant, got {other:?}"),
    };
    for width in [8usize, 20, 80] {
        let layout = view::record_layout(&mut app, 0, width);
        assert!(
            layout
                .windows(2)
                .all(|pair| pair[0].source_start <= pair[1].source_start),
            "width {width}: source offsets must stay monotonic"
        );
        assert!(
            layout
                .iter()
                .all(|visual| visual.source_start <= original.len()),
            "width {width}: offsets stay inside the raw record"
        );
        let rendered: String = layout.iter().map(|line| line.line.to_string()).collect();
        let compact: String = rendered
            .chars()
            .filter(|c| !c.is_whitespace() && *c != '│')
            .collect();
        assert!(
            compact.contains("letvalue=\"widetextthatwrapsalot\";"),
            "width {width}: code text is preserved: {compact}"
        );
    }
    assert_eq!(view::copy_source(&app), Some(original));
}

#[test]
fn search_highlights_and_selection_marker_do_not_break_syntax_roles() {
    let text = "```rust\nlet value = \"search me\";\n```";
    let mut app = assistant_with(text);
    let theme = app.theme;
    app.focus = Focus::Transcript;
    app.selected = 0;
    let layout = view::record_layout(&mut app, 0, 80);
    let first = &layout[0].line.to_string();
    assert!(first.starts_with('▌'), "selection marker on the label row");
    assert!(has_span(
        &styled_spans(&mut app, 80),
        "let",
        theme.accent,
        Modifier::BOLD
    ));

    crate::frontend::tui::transcript_search::open(&mut app);
    crate::frontend::tui::transcript_search::push_str(&mut app, "search");
    let lines = view::visible_lines(&mut app, 80, 20);
    let highlighted = lines.iter().flat_map(|line| line.spans.iter()).any(|span| {
        span.content.contains("search")
            && span.style.add_modifier.contains(Modifier::REVERSED)
            && span.style.add_modifier.contains(Modifier::UNDERLINED)
    });
    assert!(highlighted, "current search hit overlays the syntax span");
    crate::frontend::tui::transcript_search::cancel(&mut app);
}

#[test]
fn roles_use_existing_light_dark_and_derived_palettes() {
    let derived_dark =
        crate::frontend::tui::theme::native_theme(crate::frontend::tui::theme::NativeColors {
            foreground: (0xdc, 0xe4, 0xde),
            background: (0x27, 0x39, 0x2f),
        });
    let derived_light =
        crate::frontend::tui::theme::native_theme(crate::frontend::tui::theme::NativeColors {
            foreground: (0x29, 0x33, 0x2f),
            background: (0xfa, 0xfa, 0xf7),
        });
    for theme in [
        Theme::dark(),
        Theme::light(),
        Theme::fallback(),
        derived_dark,
        derived_light,
    ] {
        let text = "```rust\nlet n = 1; // c\n```";
        let mut app = App::new();
        app.theme = theme;
        app.push(assistant(text));
        let spans = styled_spans(&mut app, 80);
        assert!(has_span(&spans, "let", theme.accent, Modifier::BOLD));
        assert!(has_span(&spans, "1", theme.warning, Modifier::empty()));
        assert!(has_span(&spans, "// c", theme.secondary, Modifier::ITALIC));
    }
}

#[test]
fn oversized_blocks_and_lines_fall_back_to_one_color_without_text_loss() {
    let long_line = format!("let value = \"{}\";", "a".repeat(5_000));
    assert!(
        code_highlight::roles(&long_line, "rust").is_none(),
        "a line over the measured bound stays single-color"
    );
    let huge = (0..700)
        .map(|index| format!("fn big{index}() {{ let n = {index}; }}"))
        .collect::<Vec<_>>()
        .join("\n");
    assert!(
        code_highlight::roles(&huge, "rust").is_none(),
        "a block over the measured byte bound stays single-color"
    );

    let text = format!("```rust\n{long_line}\n```");
    let mut app = assistant_with(&text);
    let theme = app.theme;
    let spans = styled_spans(&mut app, 80);
    assert!(
        !has_span(&spans, "let", theme.accent, Modifier::BOLD),
        "fallback has no syntax colors"
    );
    assert!(
        spans.iter().any(|(content, style)| {
            content.contains("let value") && style.fg == Some(theme.code)
        })
    );
    assert!(
        markdown::code_blocks(&text)
            .iter()
            .any(|block| block.contains("let value"))
    );
}

#[test]
fn syntax_parse_is_reused_across_width_and_theme_but_not_content_or_language() {
    let text = "```rust\nfn cached() { let n = 1; }\n```";
    let mut app = assistant_with(text);
    let before = code_highlight::test_parse_count();
    let _ = view::record_layout(&mut app, 0, 40);
    let first = code_highlight::test_parse_count();
    assert!(first > before, "first layout parses");
    let _ = view::record_layout(&mut app, 0, 120);
    let wide = code_highlight::test_parse_count();
    assert_eq!(wide, first, "width-only reflow reuses the parse");

    app.set_theme(Theme::light());
    let _ = view::record_layout(&mut app, 0, 40);
    assert_eq!(
        code_highlight::test_parse_count(),
        first,
        "theme-only relayout reuses the parse"
    );

    match &mut app.records[0] {
        Record::Assistant { text } => *text = text.replace("cached()", "changed()"),
        _ => unreachable!(),
    }
    app.layouts[0] = None;
    let _ = view::record_layout(&mut app, 0, 40);
    let changed = code_highlight::test_parse_count();
    assert!(changed > first, "content changes reparse");

    app.push(assistant("```python\ndef fn_name():\n    pass\n```"));
    let _ = view::record_layout(&mut app, 1, 40);
    assert!(
        code_highlight::test_parse_count() > changed,
        "a different language/code parses separately"
    );
}

#[test]
fn oversized_known_code_renders_unchanged_without_parsing() {
    let long_line = format!("let value = \"{}\";", "a".repeat(5_000));
    let text = format!("```rust\n{long_line}\nlet second = 2;\n```");
    let mut app = assistant_with(&text);
    let theme = app.theme;
    let before = code_highlight::test_parse_count();
    let layout = view::record_layout(&mut app, 0, 80);
    let after = code_highlight::test_parse_count();
    assert_eq!(after, before, "oversized block must skip the syntax parse");
    let spans: Vec<_> = layout
        .into_iter()
        .flat_map(|line| line.line.spans.into_iter())
        .map(|span| (span.content.to_string(), span.style))
        .collect();
    assert!(
        spans
            .iter()
            .all(|(_, style)| style.fg != Some(theme.accent)),
        "no syntax colors on the fallback block"
    );
    assert!(
        spans.iter().any(|(content, style)| {
            content.contains("let value") && style.fg == Some(theme.code)
        })
    );
    assert!(spans.iter().any(|(content, style)| {
        content.contains("let second") && style.fg == Some(theme.code)
    }));
    let original = match &app.records[0] {
        Record::Assistant { text } => text.clone(),
        other => panic!("expected assistant, got {other:?}"),
    };
    assert_eq!(view::copy_source(&app), Some(original));
}

#[test]
fn highlight_perf_probe() {
    let rust = (0..40)
        .map(|index| format!("fn item{index}() -> u64 {{ let value = {index}; value + 1 }}"))
        .collect::<Vec<_>>()
        .join("\n");
    let python = (0..40)
        .map(|index| {
            format!(
                "def item{index}(value):\n    # comment {index}\n    return f\"{{value}}-{index}\""
            )
        })
        .collect::<Vec<_>>()
        .join("\n");
    let json = format!(
        "{{\n{}\n}}",
        (0..30)
            .map(|index| format!("  \"key{index}\": [{index}, true, null]"))
            .collect::<Vec<_>>()
            .join(",\n")
    );
    for (language, code) in [("rust", &rust), ("python", &python), ("json", &json)] {
        let started = std::time::Instant::now();
        let runs = code_highlight::roles(code, language).expect("syntax");
        let first = started.elapsed();
        let started = std::time::Instant::now();
        let _ = code_highlight::roles(code, language).expect("cached syntax");
        let cached = started.elapsed();
        eprintln!(
            "perf {language}: {} bytes, {} runs, first={first:?}, cached={cached:?}",
            code.len(),
            runs.len()
        );
        let text = format!("```{language}\n{code}\n```");
        let mut app = assistant_with(&text);
        for width in [40usize, 120, 40, 120] {
            let started = std::time::Instant::now();
            let _ = view::record_layout(&mut app, 0, width);
            eprintln!(
                "perf {language} width {width}: layout={:?}",
                started.elapsed()
            );
        }
    }
}

#[test]
fn nested_quote_list_code_line_sources_match_raw_offsets() {
    let text =
        "> - item\n>   ```rust\n>   // first\n>   let second = 2;\n>   fn third() {}\n>   ```\n";
    let mut app = assistant_with(text);
    let layout = view::record_layout(&mut app, 0, 80);
    for needle in ["// first", "let second", "fn third"] {
        let expected = text.find(needle).expect("raw line");
        let row = layout
            .iter()
            .find(|visual| visual.line.to_string().contains(needle))
            .unwrap_or_else(|| {
                let rendered: String = layout.iter().map(|line| line.line.to_string()).collect();
                panic!("rendered {needle}: {rendered}")
            });
        eprintln!(
            "CHECK {needle} expected {expected} actual {}",
            row.source_start
        );
        assert_eq!(
            row.source_start, expected,
            "{needle}: source_start must point at its raw code position"
        );
    }
    // An anchor at the raw second-line position resolves there after reflow.
    let anchor = text.find("let second").unwrap();
    app.follow = false;
    app.anchor = Some(Anchor {
        record: 0,
        offset: anchor,
    });
    for width in [40usize, 80] {
        let lines = view::visible_lines(&mut app, width, 8);
        let joined = lines
            .iter()
            .map(|line| line.to_string())
            .collect::<Vec<_>>()
            .join("\n");
        assert!(
            joined.contains("let second"),
            "width {width}: anchor keeps {anchor} on screen: {joined}"
        );
    }
}

#[test]
fn indented_fallback_line_sources_match_raw_offsets() {
    let text = "    let first = 1;\n    let second = 2;\n    let third = 3;\n";
    let mut app = assistant_with(text);
    let layout = view::record_layout(&mut app, 0, 80);
    for needle in ["let first", "let second", "let third"] {
        let expected = text.find(needle).expect("raw line");
        let row = layout
            .iter()
            .find(|visual| visual.line.to_string().contains(needle))
            .unwrap_or_else(|| {
                let rendered: String = layout.iter().map(|line| line.line.to_string()).collect();
                panic!("rendered {needle}: {rendered}")
            });
        assert_eq!(row.source_start, expected, "{needle}: indented source");
    }
}

#[test]
#[ignore = "manual performance probe: no wall-clock assertions in CI"]
fn highlight_large_probe() {
    for lines in [100usize, 1000] {
        let code = (0..lines)
            .map(|index| {
                format!("fn item{index}() {{ let value = \"text {index}\"; let n = {index}; }}")
            })
            .collect::<Vec<_>>()
            .join("\n");
        let started = std::time::Instant::now();
        let outcome = code_highlight::roles(&code, "rust");
        let cold = started.elapsed();
        let started = std::time::Instant::now();
        let cached = code_highlight::roles(&code, "rust");
        let warm = started.elapsed();
        eprintln!(
            "large rust {lines} lines / {} bytes: {} cold={cold:?} warm={warm:?}",
            code.len(),
            describe(&outcome)
        );
        assert_eq!(
            outcome.is_some(),
            cached.is_some(),
            "cache mirrors the bounded decision"
        );
    }
    for lines in [50usize, 200, 400] {
        let code = (0..lines)
            .map(|index| format!("fn changed{index}_{}() {{ let n = {index}; }}", index * 7))
            .collect::<Vec<_>>()
            .join("\n");
        let started = std::time::Instant::now();
        let outcome = code_highlight::roles(&code, "rust");
        eprintln!(
            "changed rust {lines} lines / {} bytes: {} parse={:?}",
            code.len(),
            describe(&outcome),
            started.elapsed()
        );
    }
    for length in [8_000usize, 16_000, 32_000] {
        let long = format!("let value = \"{}\"; // tail", "a".repeat(length));
        let started = std::time::Instant::now();
        let outcome = code_highlight::roles(&long, "rust");
        eprintln!(
            "long rust line / {} bytes: {} parse={:?}",
            long.len(),
            describe(&outcome),
            started.elapsed()
        );
    }
    let long = format!("let value = \"{}\"; // tail", "a".repeat(40_000));
    let started = std::time::Instant::now();
    let outcome = code_highlight::roles(&long, "rust");
    eprintln!(
        "large rust one line / {} bytes: {} cold={:?}",
        long.len(),
        describe(&outcome),
        started.elapsed()
    );

    // Supported ~15KB block: cached narrow/wide layout after chunk mapping.
    let code = (0..380)
        .map(|index| format!("fn layout{index}() {{ let value = \"text {index}\"; }}"))
        .collect::<Vec<_>>()
        .join("\n");
    assert!(
        code.len() < 16 * 1024,
        "probe block stays inside the supported bound: {}",
        code.len()
    );
    let text = format!("```rust\n{code}\n```");
    let mut app = assistant_with(&text);
    assert!(code_highlight::roles(&code, "rust").is_some(), "supported");
    for width in [40usize, 120, 40, 120] {
        let started = std::time::Instant::now();
        let _ = view::record_layout(&mut app, 0, width);
        eprintln!(
            "layout 15KB {} bytes width {width}: {:?}",
            code.len(),
            started.elapsed()
        );
    }
}

fn describe(outcome: &Option<std::sync::Arc<Vec<code_highlight::RoleRun>>>) -> String {
    match outcome {
        Some(runs) => format!("highlighted({} runs)", runs.len()),
        None => "fallback(single color)".to_owned(),
    }
}

#[test]
fn list_item_starting_with_fence_keeps_bullet_hang_and_offsets() {
    let text = "- ```rust\n  let first = 1;\n  let long_value = 12345;\n  ```\n";
    let mut app = assistant_with(text);
    let layout = view::record_layout(&mut app, 0, 80);
    let rows: Vec<String> = layout.iter().map(|line| line.line.to_string()).collect();
    assert!(
        rows.iter().any(|row| row.contains("• let first")),
        "the pending list bullet must land on the first code line: {rows:?}"
    );
    // The first code row starts at the synthetic list bullet, while the second
    // code row must retain its real raw offset.
    let second = "let long_value";
    let expected = text.find(second).expect("raw line");
    let row = layout
        .iter()
        .find(|visual| visual.line.to_string().contains(second))
        .unwrap_or_else(|| panic!("rendered {second}: {rows:?}"));
    assert_eq!(row.source_start, expected, "{second}: list code source");

    // Narrow wrapping keeps the list hang for continuation lines.
    let narrow = view::record_layout(&mut app, 0, 16);
    let rows: Vec<String> = narrow.iter().map(|line| line.line.to_string()).collect();
    let continued = rows
        .iter()
        .find(|row| row.contains("12345"))
        .unwrap_or_else(|| panic!("wrapped continuation: {rows:?}"));
    let leading = continued.chars().take_while(|c| *c == ' ').count();
    assert!(
        leading >= 5,
        "continuation keeps padding+bullet hang: {continued:?} ({rows:?})"
    );
}
