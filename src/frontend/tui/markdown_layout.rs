use ratatui::{
    style::{Modifier, Style},
    text::{Line, Span},
};
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

use super::markdown::{self, Alignment};
use super::state::VisualLine;
use super::textwrap::wrap_words;
use super::theme;

/// Normal prose is capped at this many terminal cells so wide terminals stay
/// scannable. Code and tables may use the full viewport.
pub(crate) const PROSE_MAX_WIDTH: usize = 96;

/// Renders Markdown blocks: prose is capped for readability while tables use
/// the full viewport width.
pub(crate) fn push_markdown(
    out: &mut Vec<VisualLine>,
    text: &str,
    width: usize,
    padding: &str,
    theme: theme::Theme,
) {
    for block in markdown::blocks_with_theme(text, theme) {
        match block {
            markdown::Block::Flow(segments) => {
                let prose_width = width.min(PROSE_MAX_WIDTH);
                push_styled(
                    out,
                    &segments,
                    prose_width,
                    padding,
                    padding,
                    Style::default(),
                );
            }
            markdown::Block::Table(table) => {
                push_table(out, &table, width, padding, theme);
                // One deliberate blank separates a table from the following
                // paragraph; the source anchor stays at the table's raw end.
                let source = out
                    .last()
                    .map(|visual| visual.source_start + 1)
                    .unwrap_or(table.source_end)
                    .min(text.len());
                out.push(VisualLine {
                    line: Line::from(String::new()),
                    source_start: source
                        .max(out.last().map(|visual| visual.source_start).unwrap_or(0)),
                });
            }
        }
    }
}

/// Pushes styled Markdown flow segments, wrapping rendered text while keeping
/// each visual line's start as an original-text byte offset. `hang` adds list
/// continuation indentation and `lead` decorates quote lines.
fn push_styled(
    out: &mut Vec<VisualLine>,
    segments: &[markdown::Segment],
    width: usize,
    first_prefix: &str,
    continuation_prefix: &str,
    prefix_style: Style,
) {
    let base = out.last().map(|visual| visual.source_start).unwrap_or(0);
    out.extend(styled_lines(
        segments,
        width,
        first_prefix,
        continuation_prefix,
        prefix_style,
        base,
    ));
}

fn styled_lines(
    segments: &[markdown::Segment],
    width: usize,
    first_prefix: &str,
    continuation_prefix: &str,
    prefix_style: Style,
    base: usize,
) -> Vec<VisualLine> {
    let prefix_width =
        UnicodeWidthStr::width(first_prefix).max(UnicodeWidthStr::width(continuation_prefix));
    let body_width = width.saturating_sub(prefix_width).max(1);
    let mut out = Vec::new();
    let mut spans: Vec<Span<'static>> = Vec::new();
    let mut used = 0usize;
    let mut content = false;
    let mut line_source = base;
    // Synthetic decoration (rules, bullets) may render more bytes than it
    // consumes; keep each line start at or after the previous one so paging and
    // `find_line` never see offsets move backwards.
    let mut last_source = base;
    let mut first_line = true;
    let mut line_cost;
    let mut available = body_width;
    for segment in segments {
        let rendered_len = segment.text.len();
        let mut consumed = 0usize;
        for grapheme in segment.text.graphemes(true) {
            let source = consumed
                .saturating_mul(segment.advance)
                .checked_div(rendered_len)
                .map_or(segment.source, |scaled| segment.source + scaled);
            if grapheme == "\n" {
                // A blank line must take its own source position from this
                // newline, not reuse the previous content line's start, or
                // `find_line` would resolve an anchor to the blank instead of
                // the content.
                if !content {
                    line_source = source.max(last_source);
                }
                out.push(VisualLine {
                    line: Line::from(std::mem::take(&mut spans)),
                    source_start: line_source,
                });
                last_source = line_source;
                used = 0;
                content = false;
                first_line = false;
                consumed += grapheme.len();
                continue;
            }
            let width_of = UnicodeWidthStr::width(grapheme).max(1);
            if content && used + width_of > available {
                out.push(VisualLine {
                    line: Line::from(std::mem::take(&mut spans)),
                    source_start: line_source,
                });
                last_source = line_source;
                used = 0;
                content = false;
                first_line = false;
            }
            if !content {
                // A new line takes the decoration of the segment that starts
                // it: quote bars every line, list hanging indent on wrap.
                let lead = if segment.lead.is_empty() {
                    String::new()
                } else {
                    segment.lead.clone()
                };
                let outer = if first_line {
                    first_prefix
                } else {
                    continuation_prefix
                };
                spans.clear();
                let indent = " ".repeat(segment.hang);
                let prefix = format!("{outer}{lead}{indent}");
                line_cost = UnicodeWidthStr::width(lead.as_str()) + segment.hang;
                spans.push(Span::styled(prefix, prefix_style));
                available = body_width.saturating_sub(line_cost).max(1);
                line_source = source.max(last_source);
                last_source = line_source;
            }
            match spans.last_mut() {
                Some(last) if last.style == segment.style => {
                    last.content.to_mut().push_str(grapheme);
                }
                _ => spans.push(Span::styled(grapheme.to_owned(), segment.style)),
            }
            used += width_of;
            content = true;
            consumed += grapheme.len();
        }
    }
    if content || out.is_empty() {
        if !content && spans.is_empty() {
            let outer = if first_line {
                first_prefix
            } else {
                continuation_prefix
            };
            spans.push(Span::styled(outer.to_owned(), prefix_style));
        }
        out.push(VisualLine {
            line: Line::from(spans),
            source_start: line_source.max(last_source),
        });
    }
    out
}

/// Renders a structured table with allocated, wrapped columns. When the
/// columns cannot stay readable every cell falls back to a key/value record.
fn push_table(
    out: &mut Vec<VisualLine>,
    table: &markdown::Table,
    width: usize,
    padding: &str,
    theme: theme::Theme,
) {
    if table.rows.is_empty() {
        return;
    }
    let columns = table
        .rows
        .iter()
        .map(|row| row.cells.len())
        .max()
        .unwrap_or(0)
        .max(table.alignments.len());
    if columns == 0 {
        return;
    }
    let pad_width = UnicodeWidthStr::width(padding);
    let available = width.saturating_sub(pad_width).max(1);
    let separators = columns.saturating_sub(1) * 3;
    let max_widths: Vec<usize> = (0..columns)
        .map(|column| {
            table
                .rows
                .iter()
                .map(|row| {
                    row.cells
                        .get(column)
                        .map(|cell| cell_text_width(cell))
                        .unwrap_or(0)
                })
                .max()
                .unwrap_or(0)
        })
        .collect();
    let min_widths: Vec<usize> = (0..columns)
        .map(|column| {
            table
                .rows
                .iter()
                .map(|row| {
                    row.cells
                        .get(column)
                        .map(|cell| cell_min_width(cell))
                        .unwrap_or(1)
                })
                .max()
                .unwrap_or(1)
                .max(1)
        })
        .collect();
    if available < columns * 3 + separators {
        push_table_fallback(out, table, width, padding, theme);
        return;
    }
    let mut widths: Vec<usize> = (0..columns)
        .map(|column| max_widths[column].max(min_widths[column]).min(available))
        .collect();
    while widths.iter().sum::<usize>() + separators > available {
        let Some(column) = (0..columns)
            .filter(|column| widths[*column] > min_widths[*column])
            .max_by_key(|column| widths[*column])
        else {
            break;
        };
        widths[column] -= 1;
    }
    if widths.iter().sum::<usize>() + separators > available {
        push_table_fallback(out, table, width, padding, theme);
        return;
    }

    let divider = Span::styled(" │ ", Style::default().fg(theme.rule));
    let mut anchor_source = table.source_start;
    for (row_index, row) in table.rows.iter().enumerate() {
        let wraps: Vec<Vec<VisualLine>> = (0..columns)
            .map(|column| {
                let empty = Vec::new();
                let cell = row.cells.get(column).unwrap_or(&empty);
                styled_lines(
                    cell,
                    widths[column],
                    "",
                    "",
                    Style::default(),
                    out.last().map(|visual| visual.source_start).unwrap_or(0),
                )
            })
            .collect();
        let height = wraps.iter().map(Vec::len).max().unwrap_or(1).max(1);
        for line_index in 0..height {
            let mut spans: Vec<Span<'static>> = Vec::new();
            let mut natural = None;
            for column in 0..columns {
                if column > 0 {
                    spans.push(divider.clone());
                }
                let visual = wraps[column].get(line_index);
                let used = visual.map(|visual| visual.line.width()).unwrap_or(0);
                let pad = widths[column].saturating_sub(used);
                let align = table
                    .alignments
                    .get(column)
                    .copied()
                    .unwrap_or(Alignment::None);
                let (left, right) = match align {
                    Alignment::Right => (pad, 0),
                    Alignment::Center => (pad / 2, pad - pad / 2),
                    Alignment::None | Alignment::Left => (0, pad),
                };
                if left > 0 {
                    spans.push(Span::styled(" ".repeat(left), Style::default()));
                }
                if let Some(visual) = visual {
                    natural = natural.or(Some(visual.source_start));
                    for span in &visual.line.spans {
                        let style = if row.header {
                            let mut style = span.style.add_modifier(Modifier::BOLD);
                            // Tint plain header text with the accent; explicit
                            // hues (code, quotes, already-accented strong spans)
                            // stay untouched.
                            let plain = style.fg == Some(theme.text)
                                || style.fg == Some(ratatui::style::Color::Reset);
                            if plain {
                                style = style.fg(theme.accent);
                            }
                            style
                        } else {
                            span.style
                        };
                        spans.push(Span::styled(span.content.clone(), style));
                    }
                }
                if right > 0 {
                    spans.push(Span::styled(" ".repeat(right), Style::default()));
                }
            }
            if !padding.is_empty() {
                spans.insert(0, Span::styled(padding.to_owned(), Style::default()));
            }
            let source = bump_anchor(
                &mut anchor_source,
                natural.unwrap_or(table.source_start),
                table.source_end,
            );
            out.push(VisualLine {
                line: Line::from(spans),
                source_start: source,
            });
        }
        // Weak horizontal rule under the header row, not after every row. It
        // gets its own anchor so PageUp can step from a body row back to the
        // header through the rule instead of skipping the whole table.
        if row.header && row_index + 1 < table.rows.len() {
            let mut rule = String::from(padding);
            for (column, width) in widths.iter().enumerate().take(columns) {
                if column > 0 {
                    rule.push_str("─┼─");
                }
                rule.push_str(&"─".repeat(*width));
            }
            let source = bump_anchor(&mut anchor_source, table.source_start, table.source_end);
            out.push(VisualLine {
                line: Line::from(Span::styled(rule, Style::default().fg(theme.rule))),
                source_start: source,
            });
        }
    }
}

/// Advances a table-local anchor so every rendered line has a strictly
/// increasing source offset; shared offsets would make `find_line` collapse
/// header, rule and body onto one navigable stop.
fn bump_anchor(current: &mut usize, natural: usize, end: usize) -> usize {
    let value = natural.max(*current + 1).min(end.max(*current));
    *current = value;
    value
}

/// Key/value fallback that keeps every cell and field name when columns cannot
/// remain readable. Long labels are clipped and, at tiny widths, move onto
/// their own line so no prefix can exceed the viewport.
fn push_table_fallback(
    out: &mut Vec<VisualLine>,
    table: &markdown::Table,
    width: usize,
    padding: &str,
    theme: theme::Theme,
) {
    let headers: Vec<String> = table
        .rows
        .iter()
        .find(|row| row.header)
        .map(|row| row.cells.iter().map(|cell| cell_plain(cell)).collect())
        .unwrap_or_default();
    let body: Vec<&markdown::TableRow> = table.rows.iter().filter(|row| !row.header).collect();
    let rows: Vec<&markdown::TableRow> = if body.is_empty() {
        table.rows.iter().collect()
    } else {
        body
    };
    let mut anchor_source = table.source_start;
    let mut first = true;
    for row in rows {
        if !first {
            let source = bump_anchor(&mut anchor_source, table.source_start, table.source_end);
            out.push(VisualLine {
                line: Line::from(String::new()),
                source_start: source,
            });
        }
        first = false;
        for (column, cell) in row.cells.iter().enumerate() {
            let label = headers
                .get(column)
                .filter(|label| !label.is_empty())
                .cloned()
                .unwrap_or_else(|| format!("Column {}", column + 1));
            push_fallback_cell(
                out,
                cell,
                &label,
                width,
                padding,
                theme,
                &mut anchor_source,
                table.source_end,
            );
        }
    }
}

#[expect(
    clippy::too_many_arguments,
    reason = "private single-caller fallback helper; its inputs are one rendering context"
)]
fn push_fallback_cell(
    out: &mut Vec<VisualLine>,
    cell: &[markdown::Segment],
    label: &str,
    width: usize,
    padding: &str,
    theme: theme::Theme,
    anchor: &mut usize,
    end: usize,
) {
    let pad_width = UnicodeWidthStr::width(padding);
    let available = width.saturating_sub(pad_width).max(1);
    let label_budget = available.saturating_sub(2);
    if label_budget >= 1 && UnicodeWidthStr::width(label) <= label_budget {
        // The `Header: value` prefix fits; continuation aligns under the value.
        let prefix = format!("{padding}{label}: ");
        let continuation = " ".repeat(UnicodeWidthStr::width(prefix.as_str()));
        let lines = styled_lines(
            cell,
            width,
            &prefix,
            &continuation,
            Style::default().fg(theme.accent),
            *anchor,
        );
        if lines.is_empty() {
            let source = bump_anchor(anchor, *anchor, end);
            out.push(VisualLine {
                line: Line::from(Span::styled(prefix, Style::default().fg(theme.accent))),
                source_start: source,
            });
        } else {
            for mut visual in lines {
                visual.source_start = bump_anchor(anchor, visual.source_start, end);
                out.push(visual);
            }
        }
        return;
    }
    // A too-long label wraps onto its own line(s) in full so every field name
    // stays readable; the value then keeps the full width with no prefix.
    for part in wrap_words(label, width.max(1)) {
        let source = bump_anchor(anchor, *anchor, end);
        out.push(VisualLine {
            line: Line::from(Span::styled(part, Style::default().fg(theme.accent))),
            source_start: source,
        });
    }
    let lines = styled_lines(
        cell,
        width,
        padding,
        padding,
        Style::default().fg(theme.text),
        *anchor,
    );
    for mut visual in lines {
        visual.source_start = bump_anchor(anchor, visual.source_start, end);
        out.push(visual);
    }
}

fn cell_plain(cell: &[markdown::Segment]) -> String {
    cell.iter()
        .map(|segment| segment.text.as_str())
        .collect::<String>()
        .replace('\n', " ")
}

fn cell_text_width(cell: &[markdown::Segment]) -> usize {
    cell.iter()
        .map(|segment| UnicodeWidthStr::width(segment.text.as_str()))
        .sum()
}

/// Longest unbreakable run in a cell, used as the minimum readable width.
fn cell_min_width(cell: &[markdown::Segment]) -> usize {
    let text = cell_plain(cell);
    text.split_whitespace()
        .map(UnicodeWidthStr::width)
        .max()
        .unwrap_or(1)
        .max(1)
}
