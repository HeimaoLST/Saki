use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

use crate::agent::message::{ImageAttachment, UserMessage};

/// Codex-style word separator run; word motion treats these as one piece.
const WORD_SEPARATORS: &str = "`~!@#$%^&*()-=+[{]}\\|;:'\",.<>/?";

/// Multi-line draft editor.
///
/// Each logical line keeps its original text; grapheme boundaries are derived
/// from the final line after every edit. This keeps separately typed combining
/// marks and ZWJ sequences as one unit for movement and deletion. The editor
/// also owns one kill buffer and the preferred display column; the TUI owns
/// input history.
pub struct Editor {
    lines: Vec<String>,
    /// Grapheme index within the cursor row.
    col: usize,
    row: usize,
    /// Single-entry kill buffer shared by word/line kills and Ctrl+Y.
    kill: Option<String>,
    /// Preferred display column for consecutive vertical moves.
    preferred_col: Option<usize>,
    /// Draft image attachments, kept with the draft so recall/clear move them
    /// together with the text.
    images: Vec<ImageAttachment>,
}

impl Default for Editor {
    fn default() -> Self {
        Self::new()
    }
}

/// One wrapped input line. `cells` holds the cell offset of every grapheme
/// boundary, so display and navigation share one wrap calculation.
pub struct DisplayLine {
    pub text: String,
    pub row: usize,
    pub grapheme_start: usize,
    pub grapheme_end: usize,
    pub cells: Vec<usize>,
    /// True when this display line ends its logical line, so the end-of-line
    /// position is selectable; a soft-wrap fragment must not claim the next
    /// fragment's start.
    pub is_logical_end: bool,
}

impl DisplayLine {
    /// Maps a target display cell to a (logical row, grapheme index). A soft
    /// wrap fragment clamps into its last grapheme instead of returning the
    /// boundary owned by the next visual line; only the logical end may return
    /// EOL.
    pub fn position_for_cell(&self, cell: usize) -> (usize, usize) {
        let count = self.grapheme_end - self.grapheme_start;
        let mut index = 0usize;
        while index < count && self.cells[index + 1] <= cell {
            index += 1;
        }
        if index == count && !self.is_logical_end && count > 0 {
            index = count - 1;
        }
        (self.row, self.grapheme_start + index)
    }
}

/// Wrapped input layout used by both rendering and vertical navigation.
pub struct EditorLayout {
    pub lines: Vec<DisplayLine>,
    pub cursor_line: usize,
    pub cursor_cell: usize,
}

impl Editor {
    pub fn new() -> Self {
        Self {
            lines: vec![String::new()],
            col: 0,
            row: 0,
            kill: None,
            preferred_col: None,
            images: Vec::new(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.lines.len() == 1 && self.lines[0].is_empty() && self.images.is_empty()
    }

    pub fn images(&self) -> &[ImageAttachment] {
        &self.images
    }

    /// Appends a draft attachment.
    pub fn insert_image(&mut self, image: ImageAttachment) {
        self.images.push(image);
        self.preferred_col = None;
    }

    /// Removes the last draft attachment; returns `false` when there was none.
    pub fn remove_last_image(&mut self) -> bool {
        let removed = self.images.pop().is_some();
        if removed {
            self.preferred_col = None;
        }
        removed
    }

    pub fn text(&self) -> String {
        self.lines.join("\n")
    }

    /// Clears the draft but keeps the kill buffer and resets vertical state.
    pub fn clear(&mut self) {
        self.lines = vec![String::new()];
        self.row = 0;
        self.col = 0;
        self.preferred_col = None;
        self.images.clear();
    }

    /// Removes and returns the draft, e.g. when submitting a message. The
    /// image snapshots are moved, not cloned, then the editor is cleared.
    pub fn take(&mut self) -> UserMessage {
        let text = self.text();
        let images = std::mem::take(&mut self.images);
        self.clear();
        UserMessage { text, images }
    }

    /// Replaces the whole draft (history recall) with the cursor at the end.
    /// The kill buffer survives.
    pub fn set_draft(&mut self, message: impl Into<UserMessage>) {
        let message = message.into();
        self.lines = if message.text.is_empty() {
            vec![String::new()]
        } else {
            message.text.split('\n').map(str::to_owned).collect()
        };
        self.images = message.images;
        self.row = self.lines.len() - 1;
        self.col = grapheme_count(&self.lines[self.row]);
        self.preferred_col = None;
    }

    pub fn cursor(&self) -> (usize, usize) {
        (self.row, self.col)
    }

    /// True when the cursor is at the start or the end of the whole draft.
    pub fn at_document_boundary(&self) -> bool {
        (self.row == 0 && self.col == 0)
            || (self.row + 1 == self.lines.len()
                && self.col == grapheme_count(&self.lines[self.row]))
    }

    /// Byte offset of the cursor in the full draft text.
    pub fn global_offset(&self) -> usize {
        self.global_offset_of(self.row, self.col)
    }

    fn global_offset_of(&self, row: usize, col: usize) -> usize {
        let mut offset = 0;
        for index in 0..row {
            offset += self.lines[index].len() + 1;
        }
        offset + byte_offset(&self.lines[row], col)
    }

    pub fn set_global_offset(&mut self, mut offset: usize) {
        let mut row = 0;
        loop {
            let line_len = self.lines[row].len();
            if offset <= line_len || row + 1 >= self.lines.len() {
                break;
            }
            offset -= line_len + 1;
            row += 1;
        }
        self.row = row;
        self.col = grapheme_count(&self.lines[row][..offset.min(self.lines[row].len())]);
    }

    fn set_text_with_cursor(&mut self, text: &str, cursor: usize) {
        self.lines = if text.is_empty() {
            vec![String::new()]
        } else {
            text.split('\n').map(str::to_owned).collect()
        };
        self.row = 0;
        self.col = 0;
        self.set_global_offset(cursor.min(text.len()));
        self.preferred_col = None;
    }

    fn replace_global_range(&mut self, start: usize, end: usize, insert: &str) {
        let text = self.text();
        let mut replaced = String::with_capacity(text.len() + insert.len());
        replaced.push_str(&text[..start]);
        replaced.push_str(insert);
        replaced.push_str(&text[end..]);
        let cursor = start + insert.len();
        self.set_text_with_cursor(&replaced, cursor);
    }

    fn kill_range(&mut self, start: usize, end: usize) {
        if start >= end {
            return;
        }
        self.kill = Some(self.text()[start..end].to_owned());
        self.replace_global_range(start, end, "");
    }

    /// Inserts pasted or typed text. Newlines inside the text never submit.
    pub fn insert_str(&mut self, text: &str) {
        let normalized = text.replace("\r\n", "\n").replace('\r', "\n");
        let mut parts = normalized.split('\n');
        self.insert_at_cursor(parts.next().unwrap_or(""));
        for part in parts {
            self.insert_newline();
            self.insert_at_cursor(part);
        }
        self.preferred_col = None;
    }

    fn insert_at_cursor(&mut self, text: &str) {
        if text.is_empty() {
            return;
        }
        let line = &mut self.lines[self.row];
        let offset = byte_offset(line, self.col);
        line.insert_str(offset, text);
        // Re-segment the final logical line: a combining mark typed separately
        // must not advance the cursor past a grapheme that now merges.
        self.col = grapheme_count(&line[..offset + text.len()]);
    }

    pub fn insert_newline(&mut self) {
        let line = self.lines[self.row].clone();
        let offset = byte_offset(&line, self.col);
        let tail = line[offset..].to_owned();
        self.lines[self.row].truncate(offset);
        self.lines.insert(self.row + 1, tail);
        self.row += 1;
        self.col = 0;
        self.preferred_col = None;
    }

    pub fn backspace(&mut self) {
        if self.lines.len() == 1 && self.lines[0].is_empty() && !self.images.is_empty() {
            self.remove_last_image();
            return;
        }
        if self.col > 0 {
            let line = &mut self.lines[self.row];
            let end = byte_offset(line, self.col);
            let start = byte_offset(line, self.col - 1);
            line.replace_range(start..end, "");
            self.col -= 1;
        } else if self.row > 0 {
            let line = self.lines.remove(self.row);
            self.row -= 1;
            self.col = grapheme_count(&self.lines[self.row]);
            self.lines[self.row].push_str(&line);
        }
        self.preferred_col = None;
    }

    pub fn delete(&mut self) {
        if self.col < grapheme_count(&self.lines[self.row]) {
            let line = &mut self.lines[self.row];
            let start = byte_offset(line, self.col);
            let end = byte_offset(line, self.col + 1);
            line.replace_range(start..end, "");
        } else if self.row + 1 < self.lines.len() {
            let next = self.lines.remove(self.row + 1);
            self.lines[self.row].push_str(&next);
        }
        self.preferred_col = None;
    }

    pub fn move_left(&mut self) {
        if self.col > 0 {
            self.col -= 1;
        } else if self.row > 0 {
            self.row -= 1;
            self.col = grapheme_count(&self.lines[self.row]);
        }
        self.preferred_col = None;
    }

    pub fn move_right(&mut self) {
        if self.col < grapheme_count(&self.lines[self.row]) {
            self.col += 1;
        } else if self.row + 1 < self.lines.len() {
            self.row += 1;
            self.col = 0;
        }
        self.preferred_col = None;
    }

    /// Ctrl+A: line start; already there moves to the previous line start.
    pub fn move_line_start(&mut self) {
        if self.col > 0 {
            self.col = 0;
        } else if self.row > 0 {
            self.row -= 1;
            self.col = 0;
        }
        self.preferred_col = None;
    }

    /// Ctrl+E: line end; already there moves to the next line end.
    pub fn move_line_end(&mut self) {
        let end = grapheme_count(&self.lines[self.row]);
        if self.col < end {
            self.col = end;
        } else if self.row + 1 < self.lines.len() {
            self.row += 1;
            self.col = grapheme_count(&self.lines[self.row]);
        }
        self.preferred_col = None;
    }

    /// Home: start of the current logical line only.
    pub fn home(&mut self) {
        self.col = 0;
        self.preferred_col = None;
    }

    /// End: end of the current logical line only.
    pub fn end(&mut self) {
        self.col = grapheme_count(&self.lines[self.row]);
        self.preferred_col = None;
    }

    pub fn move_word_left(&mut self) {
        let text = self.text();
        let cursor = self.global_offset();
        let target = snap_grapheme_start(&text, beginning_of_previous_word(&text, cursor));
        self.set_global_offset(target);
        self.preferred_col = None;
    }

    pub fn move_word_right(&mut self) {
        let text = self.text();
        let cursor = self.global_offset();
        let target = snap_grapheme_end(&text, end_of_next_word(&text, cursor));
        self.set_global_offset(target);
        self.preferred_col = None;
    }

    pub fn delete_word_backward(&mut self) {
        let text = self.text();
        let cursor = self.global_offset();
        let start = snap_grapheme_start(&text, beginning_of_previous_word(&text, cursor));
        self.kill_range(start, cursor);
    }

    pub fn delete_word_forward(&mut self) {
        let text = self.text();
        let cursor = self.global_offset();
        let end = snap_grapheme_end(&text, end_of_next_word(&text, cursor));
        self.kill_range(cursor, end);
    }

    /// Ctrl+U: kill to the logical line start; at the boundary kill the
    /// preceding newline instead.
    pub fn kill_to_line_start(&mut self) {
        let cursor = self.global_offset();
        if self.col > 0 {
            let start = self.global_offset_of(self.row, 0);
            self.kill_range(start, cursor);
        } else if self.row > 0 {
            self.kill_range(cursor - 1, cursor);
        }
    }

    /// Ctrl+K: kill to the logical line end; at the boundary kill the trailing
    /// newline instead.
    pub fn kill_to_line_end(&mut self) {
        let cursor = self.global_offset();
        let end = grapheme_count(&self.lines[self.row]);
        if self.col < end {
            let target = self.global_offset_of(self.row, end);
            self.kill_range(cursor, target);
        } else if self.row + 1 < self.lines.len() {
            self.kill_range(cursor, cursor + 1);
        }
    }

    /// Ctrl+Y: insert the single kill buffer at the cursor.
    pub fn yank(&mut self) {
        if let Some(text) = self.kill.clone() {
            self.insert_str(&text);
        }
    }

    /// Moves the cursor one wrapped display line up, restoring the preferred
    /// display column when a longer line follows a shorter one. On the first
    /// visual line it goes to the document start; the preferred column is
    /// clamped to the current width and reset at that boundary.
    pub fn move_visual_up(&mut self, width: usize) {
        let layout = self.layout(width);
        if layout.cursor_line == 0 {
            self.row = 0;
            self.col = 0;
            self.preferred_col = None;
            return;
        }
        let desired = self
            .preferred_col
            .unwrap_or(layout.cursor_cell)
            .min(width.saturating_sub(1));
        let target = &layout.lines[layout.cursor_line - 1];
        let (row, col) = target.position_for_cell(desired);
        self.row = row;
        self.col = col;
        self.preferred_col = Some(desired);
    }

    pub fn move_visual_down(&mut self, width: usize) {
        let layout = self.layout(width);
        if layout.cursor_line + 1 >= layout.lines.len() {
            let last = self.lines.len() - 1;
            self.row = last;
            self.col = grapheme_count(&self.lines[last]);
            self.preferred_col = None;
            return;
        }
        let desired = self
            .preferred_col
            .unwrap_or(layout.cursor_cell)
            .min(width.saturating_sub(1));
        let target = &layout.lines[layout.cursor_line + 1];
        let (row, col) = target.position_for_cell(desired);
        self.row = row;
        self.col = col;
        self.preferred_col = Some(desired);
    }

    /// Wraps the draft for both rendering and navigation. A last logical line
    /// that exactly fills the width gets a stable virtual line for the
    /// end-of-text cursor, so the layout never jumps as the cursor moves.
    pub fn layout(&self, width: usize) -> EditorLayout {
        let width = width.max(1);
        let mut lines: Vec<DisplayLine> = Vec::new();
        for row in 0..self.lines.len() {
            let text = &self.lines[row];
            let graphemes: Vec<&str> = text.graphemes(true).collect();
            if graphemes.is_empty() {
                lines.push(DisplayLine {
                    text: String::new(),
                    row,
                    grapheme_start: 0,
                    grapheme_end: 0,
                    cells: vec![0],
                    is_logical_end: true,
                });
                continue;
            }
            let mut start = 0usize;
            let mut used = 0usize;
            let mut current = String::new();
            let mut cells = vec![0usize];
            for (index, grapheme) in graphemes.iter().enumerate() {
                let cell_width = UnicodeWidthStr::width(*grapheme).max(1);
                if used + cell_width > width && !current.is_empty() {
                    lines.push(DisplayLine {
                        text: std::mem::take(&mut current),
                        row,
                        grapheme_start: start,
                        grapheme_end: index,
                        cells: std::mem::replace(&mut cells, vec![0usize]),
                        is_logical_end: false,
                    });
                    start = index;
                    used = 0;
                }
                current.push_str(grapheme);
                used += cell_width;
                cells.push(used);
            }
            lines.push(DisplayLine {
                text: current,
                row,
                grapheme_start: start,
                grapheme_end: graphemes.len(),
                cells,
                is_logical_end: true,
            });
        }
        if let Some(last) = lines.last() {
            let full = last.grapheme_end > last.grapheme_start
                && last.cells.last().copied().unwrap_or(0) >= width;
            if full {
                let row = last.row;
                let end = last.grapheme_end;
                lines.push(DisplayLine {
                    text: String::new(),
                    row,
                    grapheme_start: end,
                    grapheme_end: end,
                    cells: vec![0],
                    is_logical_end: true,
                });
            }
        }

        let (cursor_row, cursor_col) = self.cursor();
        let mut cursor_line = 0usize;
        let mut cursor_cell = 0usize;
        for (index, line) in lines.iter().enumerate() {
            if line.row == cursor_row
                && cursor_col >= line.grapheme_start
                && cursor_col <= line.grapheme_end
            {
                cursor_line = index;
                let local = (cursor_col - line.grapheme_start).min(line.cells.len() - 1);
                cursor_cell = line.cells[local];
            }
        }
        EditorLayout {
            lines,
            cursor_line,
            cursor_cell,
        }
    }
}

fn is_word_separator(ch: char) -> bool {
    WORD_SEPARATORS.contains(ch)
}

/// Splits a non-whitespace run into word/separator pieces at Unicode word
/// boundaries, further separating mixed punctuation where Codex does.
fn split_word_pieces(run: &str) -> Vec<(usize, &str)> {
    let mut pieces = Vec::new();
    for (segment_start, segment) in run.split_word_bound_indices() {
        let mut piece_start = 0;
        let mut chars = segment.char_indices();
        let Some((_, first_char)) = chars.next() else {
            continue;
        };
        let mut in_separator = is_word_separator(first_char);
        for (index, ch) in chars {
            let is_sep = is_word_separator(ch);
            if is_sep == in_separator {
                continue;
            }
            pieces.push((segment_start + piece_start, &segment[piece_start..index]));
            piece_start = index;
            in_separator = is_sep;
        }
        pieces.push((segment_start + piece_start, &segment[piece_start..]));
    }
    pieces
}

fn beginning_of_previous_word(text: &str, cursor: usize) -> usize {
    let prefix = &text[..cursor.min(text.len())];
    let Some((first_non_ws_idx, ch)) = prefix
        .char_indices()
        .rev()
        .find(|&(_, ch)| !ch.is_whitespace())
    else {
        return 0;
    };
    let run_start = prefix[..first_non_ws_idx]
        .char_indices()
        .rev()
        .find(|&(_, ch)| ch.is_whitespace())
        .map_or(0, |(index, ch)| index + ch.len_utf8());
    let run_end = first_non_ws_idx + ch.len_utf8();
    let pieces = split_word_pieces(&prefix[run_start..run_end]);
    let mut pieces = pieces.into_iter().rev().peekable();
    let Some((piece_start, piece)) = pieces.next() else {
        return run_start;
    };
    let mut start = run_start + piece_start;
    if piece.chars().all(is_word_separator) {
        while let Some((index, piece)) = pieces.peek() {
            if !piece.chars().all(is_word_separator) {
                break;
            }
            start = run_start + *index;
            pieces.next();
        }
    }
    start
}

fn end_of_next_word(text: &str, cursor: usize) -> usize {
    let cursor = cursor.min(text.len());
    let suffix = &text[cursor..];
    let Some(first_non_ws) = suffix.find(|ch: char| !ch.is_whitespace()) else {
        return text.len();
    };
    let run = &suffix[first_non_ws..];
    let run = &run[..run.find(char::is_whitespace).unwrap_or(run.len())];
    let mut pieces = split_word_pieces(run).into_iter().peekable();
    let Some((start, piece)) = pieces.next() else {
        return cursor + first_non_ws;
    };
    let word_start = cursor + first_non_ws + start;
    let mut end = word_start + piece.len();
    if piece.chars().all(is_word_separator) {
        while let Some((index, piece)) = pieces.peek() {
            if !piece.chars().all(is_word_separator) {
                break;
            }
            end = cursor + first_non_ws + *index + piece.len();
            pieces.next();
        }
    }
    end
}

fn grapheme_count(text: &str) -> usize {
    text.graphemes(true).count()
}

/// Largest grapheme boundary at or before `offset`.
fn snap_grapheme_start(text: &str, offset: usize) -> usize {
    let offset = offset.min(text.len());
    let mut boundary = 0;
    for (index, _) in text.grapheme_indices(true) {
        if index > offset {
            break;
        }
        boundary = index;
    }
    boundary
}

/// Smallest grapheme boundary at or after `offset`.
fn snap_grapheme_end(text: &str, offset: usize) -> usize {
    let offset = offset.min(text.len());
    for (index, grapheme) in text.grapheme_indices(true) {
        let end = index + grapheme.len();
        if offset <= index {
            return index;
        }
        if offset < end {
            return end;
        }
    }
    text.len()
}

fn byte_offset(text: &str, grapheme_index: usize) -> usize {
    text.grapheme_indices(true)
        .nth(grapheme_index)
        .map(|(offset, _)| offset)
        .unwrap_or(text.len())
}
