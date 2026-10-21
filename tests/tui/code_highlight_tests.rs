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
    for theme in [Theme::dark(), Theme::light(), Theme::fallback()] {
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
