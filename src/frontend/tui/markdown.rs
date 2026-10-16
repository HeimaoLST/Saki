use pulldown_cmark::{Event, Options, Parser, Tag, TagEnd};
use ratatui::style::{Modifier, Style};
use unicode_width::UnicodeWidthStr;

use crate::frontend::tui::theme;

/// Explicit Markdown column alignment.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Alignment {
    None,
    Left,
    Center,
    Right,
}

/// One structured table. Cells keep styled segments and their original source
/// offsets so the renderer can wrap them without touching the raw text.
pub struct Table {
    pub alignments: Vec<Alignment>,
    pub rows: Vec<TableRow>,
    pub source_start: usize,
    pub source_end: usize,
}

pub struct TableRow {
    pub cells: Vec<Vec<Segment>>,
    pub header: bool,
}

/// A parsed Markdown block. Flow content is wrapped by the caller; tables get
/// their own column layout.
pub enum Block {
    Flow(Vec<Segment>),
    Table(Table),
}

/// A styled run of rendered text together with the byte offset of its first
/// byte in the original Markdown and how many original bytes it represents.
///
/// Real text uses its full source span as `advance`. Synthetic decoration (a
/// rule, a list bullet, a spacing newline) uses `advance = 0`, so source
/// offsets never run past the raw text or go backwards. `hang` adds a hanging
/// indent to wrapped continuation lines and `lead` decorates every line the
/// segment starts (quotes).
pub struct Segment {
    pub text: String,
    pub style: Style,
    pub source: usize,
    pub advance: usize,
    pub hang: usize,
    pub lead: String,
}

#[derive(Default, Clone, Copy)]
struct Emphasis {
    strong: bool,
    emphasis: bool,
    code: bool,
    heading: Option<u8>,
    quote: bool,
}

impl Emphasis {
    fn style(self, theme: theme::Theme) -> Style {
        let mut style = Style::default().fg(theme.text);
        match self.heading {
            Some(1) => style = style.add_modifier(Modifier::BOLD | Modifier::UNDERLINED),
            Some(2) => style = style.add_modifier(Modifier::BOLD),
            Some(3) => style = style.add_modifier(Modifier::BOLD | Modifier::ITALIC),
            Some(_) => style = style.add_modifier(Modifier::BOLD),
            None => {}
        }
        if self.strong {
            style = style.add_modifier(Modifier::BOLD);
        }
        if self.emphasis {
            style = style.add_modifier(Modifier::ITALIC);
        }
        // Semantic foreground is assigned last, with code highest priority, so
        // bold/italic/heading modifiers survive inside code spans.
        if self.code {
            style = style.fg(theme.code);
        } else if self.heading.is_some() || self.strong {
            style = style.fg(theme.accent);
        } else if self.quote {
            style = style.fg(theme.secondary);
        }
        style
    }
}

struct ListState {
    next_number: Option<u64>,
}

struct TableBuild {
    alignments: Vec<Alignment>,
    rows: Vec<TableRow>,
    row: Option<Vec<Vec<Segment>>>,
    cell: Option<Vec<Segment>>,
    header: bool,
    source_start: usize,
    source_end: usize,
}

struct Markdown {
    blocks: Vec<Block>,
    flow: Vec<Segment>,
    state: Emphasis,
    lists: Vec<ListState>,
    hang_stack: Vec<usize>,
    item_hang: usize,
    pending_bullet: bool,
    quote_depth: usize,
    cursor: usize,
    table: Option<TableBuild>,
    theme: theme::Theme,
}

impl Markdown {
    fn new(theme: theme::Theme) -> Self {
        Self {
            blocks: Vec::new(),
            flow: Vec::new(),
            state: Emphasis::default(),
            lists: Vec::new(),
            hang_stack: Vec::new(),
            item_hang: 0,
            pending_bullet: false,
            quote_depth: 0,
            cursor: 0,
            table: None,
            theme,
        }
    }

    fn in_list(&self) -> bool {
        !self.lists.is_empty()
    }

    fn in_quote(&self) -> bool {
        self.quote_depth > 0
    }

    fn lead(&self) -> String {
        if self.in_quote() {
            "│ ".repeat(self.quote_depth)
        } else {
            String::new()
        }
    }

    fn style(&self) -> Style {
        self.state.style(self.theme)
    }

    fn flush_flow(&mut self) {
        if !self.flow.is_empty() {
            self.blocks
                .push(Block::Flow(std::mem::take(&mut self.flow)));
        }
    }

    fn push_text(&mut self, text: &str, style: Style, source: usize, advance: usize) {
        let hang = self.item_hang;
        self.push_segment(text.to_owned(), style, source, advance, hang, self.lead());
    }

    fn push_segment(
        &mut self,
        text: String,
        style: Style,
        source: usize,
        advance: usize,
        hang: usize,
        lead: String,
    ) {
        if text.is_empty() {
            return;
        }
        if let Some(last) = self.flow.last_mut() {
            // Synthetic decoration (`advance == 0`) must stay its own segment:
            // merging it into real text would compress the newline onto the
            // text's start offset and break blank-line anchors.
            if advance > 0
                && last.advance > 0
                && last.style == style
                && last.hang == hang
                && last.lead == lead
                && last.source + last.advance == source
            {
                last.text.push_str(&text);
                last.advance += advance;
                return;
            }
        }
        self.flow.push(Segment {
            text,
            style,
            source,
            advance,
            hang,
            lead,
        });
    }

    fn take_bullet(&mut self) {
        if !self.pending_bullet {
            return;
        }
        self.pending_bullet = false;
        let depth = self.lists.len().saturating_sub(1);
        let indent = "  ".repeat(depth);
        let label = match self.lists.last_mut() {
            Some(state) => match state.next_number {
                Some(number) => {
                    state.next_number = Some(number + 1);
                    format!("{number}. ")
                }
                None => "• ".to_owned(),
            },
            None => "• ".to_owned(),
        };
        let hang = UnicodeWidthStr::width(indent.as_str()) + UnicodeWidthStr::width(label.as_str());
        self.item_hang = hang;
        self.push_segment(
            format!("{indent}{label}"),
            Style::default(),
            self.cursor,
            0,
            0,
            self.lead(),
        );
    }

    fn take_task_marker(&mut self, checked: bool) {
        if !self.pending_bullet {
            return;
        }
        self.pending_bullet = false;
        let depth = self.lists.len().saturating_sub(1);
        let indent = "  ".repeat(depth);
        let numbering = match self.lists.last_mut() {
            Some(state) => match state.next_number {
                Some(number) => {
                    state.next_number = Some(number + 1);
                    format!("{number}. ")
                }
                None => String::new(),
            },
            None => String::new(),
        };
        let task = if checked { "☑ " } else { "☐ " };
        let label = format!("{indent}{numbering}{task}");
        let hang = UnicodeWidthStr::width(label.as_str());
        self.item_hang = hang;
        self.push_segment(label, Style::default(), self.cursor, 0, 0, self.lead());
    }

    fn ensure_blank(&mut self) {
        self.add_newlines(2);
    }

    fn ensure_newline(&mut self) {
        self.add_newlines(1);
    }

    fn add_newlines(&mut self, want: usize) {
        // Block separation must never create a leading empty line at the very
        // start of a message; the first content sits on the first line.
        if self.flow.is_empty() {
            return;
        }
        let have = trailing_newlines(&self.flow);
        if have < want {
            self.push_segment(
                "\n".repeat(want - have),
                Style::default(),
                self.cursor,
                0,
                0,
                String::new(),
            );
        }
    }
}

/// Parses Markdown into flow content and structured tables using the active
/// terminal theme. This test adapter keeps the historical `segments`-style
/// checks available; production rendering passes an explicit palette.
#[cfg(test)]
pub fn blocks(text: &str) -> Vec<Block> {
    blocks_with_theme(text, theme::Theme::fallback())
}

/// Parses Markdown with an explicit theme so rendering can be verified for the
/// light and dark palettes independently.
pub fn blocks_with_theme(text: &str, theme: theme::Theme) -> Vec<Block> {
    let mut parser = Markdown::new(theme);
    for (event, range) in
        Parser::new_ext(text, Options::ENABLE_TABLES | Options::ENABLE_TASKLISTS).into_offset_iter()
    {
        match event {
            Event::Start(Tag::Heading { level, .. }) => {
                parser.ensure_blank();
                parser.state.heading = Some(level as u8);
            }
            Event::End(TagEnd::Heading(_)) => {
                parser.state.heading = None;
                parser.ensure_blank();
            }
            Event::Start(Tag::Paragraph) => {
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
            }
            Event::End(TagEnd::Paragraph) => {
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
            }
            Event::Start(Tag::Emphasis) => parser.state.emphasis = true,
            Event::End(TagEnd::Emphasis) => parser.state.emphasis = false,
            Event::Start(Tag::Strong) => parser.state.strong = true,
            Event::End(TagEnd::Strong) => parser.state.strong = false,
            Event::Start(Tag::CodeBlock(kind)) => {
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
                let language = match kind {
                    pulldown_cmark::CodeBlockKind::Fenced(language) => language.to_string(),
                    pulldown_cmark::CodeBlockKind::Indented => String::new(),
                };
                if !language.is_empty() {
                    // A quiet language label marks the block boundary without
                    // adding syntax coloring the renderer does not own.
                    let style = Style::default().fg(parser.theme.secondary);
                    parser.push_text(&format!("{language}\n"), style, range.start, 0);
                }
                parser.state.code = true;
            }
            Event::End(TagEnd::CodeBlock) => {
                parser.state.code = false;
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
            }
            Event::Start(Tag::List(start)) => {
                if parser.in_list() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
                // pulldown only reports a start number for the first ordered
                // item or nested ordered lists.
                parser.lists.push(ListState { next_number: start });
            }
            Event::End(TagEnd::List(_)) => {
                parser.lists.pop();
                if parser.in_list() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
            }
            Event::Start(Tag::Item) => {
                parser.ensure_newline();
                parser.hang_stack.push(parser.item_hang);
                parser.pending_bullet = true;
            }
            Event::End(TagEnd::Item) => {
                parser.ensure_newline();
                parser.item_hang = parser.hang_stack.pop().unwrap_or(0);
                parser.pending_bullet = false;
            }
            Event::TaskListMarker(checked) => parser.take_task_marker(checked),
            Event::Start(Tag::BlockQuote(_)) => {
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
                parser.quote_depth += 1;
                parser.state.quote = true;
            }
            Event::End(TagEnd::BlockQuote(_)) => {
                parser.quote_depth = parser.quote_depth.saturating_sub(1);
                parser.state.quote = parser.quote_depth > 0;
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
            }
            Event::Start(Tag::Table(alignments)) => {
                if parser.in_list() || parser.in_quote() {
                    parser.ensure_newline();
                } else {
                    parser.ensure_blank();
                }
                parser.table = Some(TableBuild {
                    alignments: alignments.into_iter().map(convert_alignment).collect(),
                    rows: Vec::new(),
                    row: None,
                    cell: None,
                    header: false,
                    source_start: range.start,
                    source_end: range.end,
                });
            }
            Event::Start(Tag::TableHead) => {
                if let Some(table) = &mut parser.table {
                    table.row = Some(Vec::new());
                    table.header = true;
                }
            }
            Event::End(TagEnd::TableHead) => {
                if let Some(table) = &mut parser.table {
                    let cells = table.row.take().unwrap_or_default();
                    table.rows.push(TableRow {
                        cells,
                        header: true,
                    });
                }
            }
            Event::Start(Tag::TableRow) => {
                if let Some(table) = &mut parser.table {
                    table.row = Some(Vec::new());
                    table.header = false;
                }
            }
            Event::End(TagEnd::TableRow) => {
                if let Some(table) = &mut parser.table {
                    let cells = table.row.take().unwrap_or_default();
                    table.rows.push(TableRow {
                        cells,
                        header: false,
                    });
                }
            }
            Event::Start(Tag::TableCell) => {
                if let Some(table) = &mut parser.table {
                    table.cell = Some(Vec::new());
                }
            }
            Event::End(TagEnd::TableCell) => {
                if let Some(table) = &mut parser.table
                    && let (Some(row), Some(cell)) = (&mut table.row, table.cell.take())
                {
                    row.push(cell);
                }
            }
            Event::End(TagEnd::Table) => {
                parser.ensure_blank();
                let Some(mut table) = parser.table.take() else {
                    continue;
                };
                table.source_end = table.source_end.max(range.end);
                parser.flush_flow();
                parser.blocks.push(Block::Table(Table {
                    alignments: table.alignments,
                    rows: table.rows,
                    source_start: table.source_start,
                    source_end: table.source_end,
                }));
            }
            Event::Text(item) => {
                let style = parser.style();
                push_into(
                    &mut parser,
                    item.as_ref(),
                    style,
                    range.start,
                    range.len(),
                    range.end,
                );
            }
            Event::Code(code) => {
                let style = parser.style().fg(parser.theme.code);
                push_into(
                    &mut parser,
                    code.as_ref(),
                    style,
                    range.start,
                    range.len(),
                    range.end,
                );
            }
            Event::SoftBreak => {
                let cursor = parser.cursor;
                push_into(&mut parser, " ", Style::default(), cursor, 0, cursor);
            }
            Event::HardBreak => parser.ensure_newline(),
            Event::Rule => {
                parser.ensure_blank();
                let style = Style::default().fg(parser.theme.rule);
                parser.push_text("─────", style, parser.cursor, 0);
                parser.ensure_blank();
            }
            _ => {}
        }
    }
    parser.flush_flow();
    parser.blocks
}

fn push_into(
    parser: &mut Markdown,
    text: &str,
    style: Style,
    source: usize,
    advance: usize,
    end: usize,
) {
    let lead = parser.lead();
    if let Some(table) = &mut parser.table
        && let Some(cell) = &mut table.cell
    {
        push_cell_segment(cell, text, style, source, advance, lead);
        return;
    }
    // The bullet is synthetic and must take the item/text start offset, before
    // the cursor advances past the wrapped text, so every continuation line can
    // anchor back to the item.
    parser.take_bullet();
    parser.cursor = parser.cursor.max(end);
    parser.push_text(text, style, source, advance);
}

fn push_cell_segment(
    cell: &mut Vec<Segment>,
    text: &str,
    style: Style,
    source: usize,
    advance: usize,
    lead: String,
) {
    if text.is_empty() {
        return;
    }
    if let Some(last) = cell.last_mut()
        && advance > 0
        && last.advance > 0
        && last.style == style
        && last.lead == lead
        && last.source + last.advance == source
    {
        last.text.push_str(text);
        last.advance += advance;
        return;
    }
    cell.push(Segment {
        text: text.to_owned(),
        style,
        source,
        advance,
        hang: 0,
        lead,
    });
}

fn convert_alignment(alignment: pulldown_cmark::Alignment) -> Alignment {
    match alignment {
        pulldown_cmark::Alignment::None => Alignment::None,
        pulldown_cmark::Alignment::Left => Alignment::Left,
        pulldown_cmark::Alignment::Center => Alignment::Center,
        pulldown_cmark::Alignment::Right => Alignment::Right,
    }
}

/// Flat styled view of the flow content. Tables are structured blocks; this
/// compatibility helper keeps their raw source so nothing silently disappears.
#[cfg(test)]
pub fn segments(text: &str) -> Vec<Segment> {
    let mut out = Vec::new();
    for block in blocks(text) {
        match block {
            Block::Flow(flow) => out.extend(flow),
            Block::Table(table) => out.push(Segment {
                text: text[table.source_start..table.source_end].to_owned(),
                style: Style::default(),
                source: table.source_start,
                advance: table.source_end.saturating_sub(table.source_start),
                hang: 0,
                lead: String::new(),
            }),
        }
    }
    out
}

pub fn code_blocks(text: &str) -> Vec<String> {
    let mut blocks = Vec::new();
    let mut current = String::new();
    let mut in_block = false;
    for event in Parser::new_ext(text, Options::ENABLE_TABLES | Options::ENABLE_TASKLISTS) {
        match event {
            Event::Start(Tag::CodeBlock(_)) => {
                current.clear();
                in_block = true;
            }
            Event::End(TagEnd::CodeBlock) => {
                if in_block {
                    // Keep the block source verbatim, including the trailing
                    // newline, so copying matches the original text.
                    blocks.push(std::mem::take(&mut current));
                    in_block = false;
                }
            }
            Event::Text(text) if in_block => current.push_str(&text),
            _ => {}
        }
    }
    blocks
}

fn trailing_newlines(out: &[Segment]) -> usize {
    // Count across adjacent segments so a code-styled `\n` followed by a
    // default-styled separator is still recognized as one blank line.
    let mut count = 0;
    for segment in out.iter().rev() {
        for byte in segment.text.bytes().rev() {
            if byte == b'\n' {
                count += 1;
            } else {
                return count;
            }
        }
    }
    count
}

#[cfg(test)]
mod tests {
    use super::*;
    use unicode_width::UnicodeWidthStr;

    fn plain(blocks: Vec<Block>) -> String {
        let mut out = String::new();
        for block in blocks {
            match block {
                Block::Flow(flow) => {
                    for segment in flow {
                        out.push_str(&segment.text);
                    }
                }
                Block::Table(table) => {
                    for row in table.rows {
                        for cell in row.cells {
                            for segment in cell {
                                out.push_str(&segment.text);
                            }
                            out.push('|');
                        }
                        out.push('\n');
                    }
                }
            }
        }
        out
    }

    #[test]
    fn tables_parse_into_rows_and_alignments() {
        let text = "| Name | Count |\n| :--- | ---: |\n| alpha | 12 |\n| 中文 | 3 |";
        let blocks = blocks(text);
        let table = blocks
            .into_iter()
            .find_map(|block| match block {
                Block::Table(table) => Some(table),
                _ => None,
            })
            .expect("table block");
        assert_eq!(table.alignments, vec![Alignment::Left, Alignment::Right]);
        assert_eq!(table.rows.len(), 3);
        assert!(table.rows[0].header);
        assert_eq!(plain_table_cell(&table.rows[1].cells[0]), "alpha");
        assert_eq!(plain_table_cell(&table.rows[2].cells[0]), "中文");
    }

    #[test]
    fn flow_content_keeps_numbered_and_task_lists() {
        let rendered = plain(blocks("1. first\n2. [x] done\n   - [ ] todo"));
        assert!(rendered.contains("1. first"), "{rendered:?}");
        assert!(rendered.contains("2. ☑ done"), "{rendered:?}");
        assert!(rendered.contains("☐ todo"), "{rendered:?}");
    }

    #[test]
    fn headings_and_quotes_carry_styles() {
        let blocks = blocks("# Top\n\n> quoted\n\n### Detail");
        let mut seen = Vec::new();
        for block in blocks {
            if let Block::Flow(flow) = block {
                for segment in flow {
                    if segment.text.contains("Top")
                        || segment.text.contains("quoted")
                        || segment.text.contains("Detail")
                    {
                        seen.push((segment.text.clone(), segment.style, segment.lead.clone()));
                    }
                }
            }
        }
        assert!(
            seen.iter().any(|(text, style, _)| text == "Top"
                && style.add_modifier.contains(Modifier::UNDERLINED))
        );
        assert!(
            seen.iter().any(|(text, style, _)| text == "Detail"
                && style.add_modifier.contains(Modifier::ITALIC))
        );
        assert!(
            seen.iter()
                .any(|(text, _, lead)| text.contains("quoted") && lead.starts_with('│'))
        );
    }

    fn plain_table_cell(cell: &[Segment]) -> String {
        cell.iter().map(|segment| segment.text.as_str()).collect()
    }

    #[test]
    fn table_cells_keep_cjk_width() {
        let blocks = blocks("| 名称 | 数量 |\n| --- | --- |\n| 中文 | 3 |");
        let table = blocks
            .into_iter()
            .find_map(|block| match block {
                Block::Table(table) => Some(table),
                _ => None,
            })
            .unwrap();
        let width = UnicodeWidthStr::width(plain_table_cell(&table.rows[1].cells[0]).as_str());
        assert_eq!(width, 4);
    }
}
