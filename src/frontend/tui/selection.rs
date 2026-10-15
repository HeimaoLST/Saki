use std::collections::BTreeMap;
use std::time::{Duration, Instant};

use ratatui::{
    buffer::Buffer,
    layout::{Position, Rect},
    style::Style,
};
use unicode_width::UnicodeWidthStr;

/// Delay between stationary edge auto-scroll steps while dragging.
pub const EDGE_SCROLL_INTERVAL: Duration = Duration::from_millis(60);

/// Logical identity of one rendered transcript row. Record indices are
/// monotonic in display order, so lexicographic ordering matches the visible
/// projection without a global row ordinal.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Debug)]
pub struct RowKey {
    pub record: usize,
    pub line: usize,
}

/// Exact top viewport row at a fixed width. Source offsets remain the reflow
/// fallback; this is only trusted while the width matches.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct VisualAnchor {
    pub key: RowKey,
    pub width: usize,
}

/// A selection endpoint: logical row plus the absolute terminal column.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
pub struct RowPoint {
    pub key: RowKey,
    pub column: u16,
}

/// Which transcript edge a stationary pointer is holding.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Edge {
    Top,
    Bottom,
}

/// One rendered viewport row published by `view::visible_lines`.
#[derive(Clone, Debug)]
pub struct ViewportRow {
    pub key: RowKey,
    pub row: u16,
    pub cells: Vec<(String, usize)>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct RowSnapshot {
    cells: Vec<(String, usize)>,
}

struct TranscriptSelection {
    anchor: RowPoint,
    cursor: RowPoint,
    rows: BTreeMap<RowKey, RowSnapshot>,
    dragging: bool,
    /// Set once a real Drag event moves the pointer; a long press without
    /// movement never arms edge scrolling.
    dragged: bool,
    initial_pointer: Position,
    /// Follow was frozen at mouse-down; a click without a drag restores it.
    restore_follow: bool,
    edge: Option<Edge>,
    pointer: Position,
    area: Rect,
    scroll_due: Option<Instant>,
}

#[derive(Default)]
pub struct TextSelection {
    screen: Buffer,
    anchor: Option<Position>,
    cursor: Position,
    dragging: bool,
    transcript: Option<TranscriptSelection>,
}

impl TextSelection {
    pub fn clear(&mut self) {
        self.anchor = None;
        self.dragging = false;
        self.transcript = None;
    }

    /// Whether a selection exists (including while dragging).
    pub fn active(&self) -> bool {
        self.anchor.is_some() || self.transcript.is_some()
    }

    pub fn is_transcript(&self) -> bool {
        self.transcript.is_some()
    }

    /// Screen-buffer selection used for input, help, status and log views.
    pub fn start(&mut self, position: Position) {
        self.clear();
        if self.screen.area.contains(position) {
            self.anchor = Some(position);
            self.cursor = position;
            self.dragging = true;
        }
    }

    pub fn drag(&mut self, position: Position) {
        if self.dragging {
            self.cursor = Position::new(
                position
                    .x
                    .clamp(self.screen.area.x, self.screen.area.right()),
                position.y.clamp(
                    self.screen.area.y,
                    self.screen.area.bottom().saturating_sub(1),
                ),
            );
        }
    }

    /// Starts a conversation selection on a rendered viewport row.
    pub fn start_transcript(
        &mut self,
        area: Rect,
        position: Position,
        viewport: &[ViewportRow],
        restore_follow: bool,
    ) -> bool {
        self.clear();
        let Some(point) = point_at(area, position, viewport) else {
            return false;
        };
        let pointer = clamp_pointer(position, area);
        self.transcript = Some(TranscriptSelection {
            anchor: point,
            cursor: point,
            rows: BTreeMap::new(),
            dragging: true,
            dragged: false,
            initial_pointer: pointer,
            restore_follow,
            // Edge scrolling only arms after real drag movement.
            edge: None,
            pointer,
            area,
            scroll_due: None,
        });
        self.snapshot_viewport(viewport);
        true
    }

    pub fn drag_transcript(&mut self, area: Rect, position: Position, viewport: &[ViewportRow]) {
        let Some(transcript) = self.transcript.as_mut() else {
            return;
        };
        if !transcript.dragging || transcript.area != area {
            return;
        }
        let pointer = clamp_pointer(position, area);
        let Some(point) = point_at(area, pointer, viewport) else {
            return;
        };
        transcript.pointer = pointer;
        transcript.cursor = point;
        if pointer != transcript.initial_pointer {
            transcript.dragged = true;
            transcript.restore_follow = false;
        }
        let next_edge = if transcript.dragged {
            edge_for(area, pointer)
        } else {
            None
        };
        if next_edge != transcript.edge {
            transcript.edge = next_edge;
            transcript.scroll_due = next_edge.map(|_| Instant::now() + EDGE_SCROLL_INTERVAL);
        }
        self.snapshot_viewport(viewport);
    }

    pub fn finish(
        &mut self,
        position: Position,
        area: Rect,
        viewport: &[ViewportRow],
    ) -> Option<String> {
        if self.transcript.is_some() {
            self.drag_transcript(area, position, viewport);
            if let Some(transcript) = self.transcript.as_mut() {
                transcript.dragging = false;
                transcript.edge = None;
                transcript.scroll_due = None;
            }
            return self.text();
        }
        if !self.dragging {
            return None;
        }
        self.drag(position);
        self.dragging = false;
        self.text()
    }

    /// True when a mouse-down froze follow but no actual drag followed, so the
    /// release can restore the follow state.
    pub fn take_follow_restore(&mut self) -> bool {
        let restore = self
            .transcript
            .as_mut()
            .is_some_and(|transcript| transcript.restore_follow && !transcript.dragged);
        if restore {
            if let Some(transcript) = self.transcript.as_mut() {
                transcript.restore_follow = false;
            }
        }
        restore
    }

    /// Clears the selection when a record that owns cached rows changes, called
    /// from the frontend's own mutation boundary.
    pub fn invalidate_record(&mut self, record: usize) {
        let affected = self
            .transcript
            .as_ref()
            .is_some_and(|transcript| transcript.rows.keys().any(|key| key.record == record));
        if affected {
            self.transcript = None;
        }
    }

    pub fn edge(&self) -> Option<Edge> {
        self.transcript.as_ref().and_then(|t| t.edge)
    }

    pub fn pointer(&self) -> Position {
        self.transcript.as_ref().map_or(self.cursor, |t| t.pointer)
    }

    pub fn clear_edge(&mut self) {
        if let Some(transcript) = self.transcript.as_mut() {
            transcript.edge = None;
            transcript.scroll_due = None;
        }
    }

    /// Rearms the next step after a successful edge scroll tick.
    pub fn reset_scroll_due(&mut self) {
        if let Some(transcript) = self.transcript.as_mut() {
            transcript.scroll_due = transcript
                .edge
                .map(|_| Instant::now() + EDGE_SCROLL_INTERVAL);
        }
    }

    pub fn edge_deadline(&self) -> Option<Instant> {
        self.transcript.as_ref().and_then(|t| t.scroll_due)
    }

    /// Cached row count, used by tests to confirm bounded traversal.
    #[cfg(test)]
    pub fn cached_rows(&self) -> usize {
        self.transcript.as_ref().map_or(0, |t| t.rows.len())
    }

    /// Drops the selection if a cached row now renders different text. Only
    /// rows currently visible can be compared; off-screen mutation is
    /// intentionally conservative: unverified later rather than silently
    /// copying replaced content.
    pub fn observe_viewport(&mut self, viewport: &[ViewportRow]) {
        let Some(transcript) = self.transcript.as_ref() else {
            return;
        };
        let changed = viewport.iter().any(|current| {
            transcript
                .rows
                .get(&current.key)
                .is_some_and(|cached| cached.cells != current.cells)
        });
        if changed {
            self.transcript = None;
        }
    }

    fn snapshot_viewport(&mut self, viewport: &[ViewportRow]) {
        let Some(transcript) = self.transcript.as_mut() else {
            return;
        };
        let (start, end) = ordered_points(transcript.anchor, transcript.cursor);
        let mut changed = false;
        for row in viewport {
            if row.key < start.key || row.key > end.key {
                continue;
            }
            match transcript.rows.get(&row.key) {
                Some(cached) if cached.cells != row.cells => {
                    changed = true;
                    break;
                }
                Some(_) => {}
                None => {
                    transcript.rows.insert(
                        row.key,
                        RowSnapshot {
                            cells: row.cells.clone(),
                        },
                    );
                }
            }
        }
        if changed {
            self.transcript = None;
        }
    }

    fn bounds(&self) -> Option<(Position, Position)> {
        let anchor = self.anchor?;
        if anchor == self.cursor {
            return None;
        }
        if (anchor.y, anchor.x) < (self.cursor.y, self.cursor.x) {
            Some((anchor, self.cursor))
        } else {
            Some((self.cursor, anchor))
        }
    }

    fn columns(&self, row: u16, start: Position, end: Position) -> std::ops::Range<u16> {
        let left = if row == start.y {
            start.x
        } else {
            self.screen.area.x
        };
        let right = if row == end.y {
            end.x
        } else {
            self.screen.area.right()
        };
        left..right
    }

    pub fn text(&self) -> Option<String> {
        if let Some(transcript) = self.transcript.as_ref() {
            return transcript_text(transcript);
        }
        let (start, end) = self.bounds()?;
        let mut lines = Vec::new();
        for row in start.y..=end.y {
            let columns = self.columns(row, start, end);
            let mut line = String::new();
            let mut column = self.screen.area.x;
            while column < self.screen.area.right() {
                let symbol = self.screen[(column, row)].symbol();
                let next = column + symbol.width().max(1) as u16;
                if column < columns.end && next > columns.start {
                    line.push_str(symbol);
                }
                column = next;
            }
            lines.push(line.trim_end().to_owned());
        }
        let text = lines.join("\n");
        (!text.is_empty()).then_some(text)
    }

    pub fn render(
        &mut self,
        buffer: &mut Buffer,
        style: Style,
        viewport: &[(RowKey, u16)],
        area: Rect,
    ) {
        if let Some(transcript) = self.transcript.as_ref() {
            if transcript.area != area {
                self.transcript = None;
                return;
            }
            let (start, end) = ordered_points(transcript.anchor, transcript.cursor);
            let (start_key, end_key) = (start.key, end.key);
            let (start_column, end_column) = (start.column, end.column);
            for (key, row) in viewport {
                if *key < start_key || *key > end_key {
                    continue;
                }
                let left = if *key == start_key {
                    start_column
                } else {
                    area.x
                };
                let right = if *key == end_key {
                    end_column
                } else {
                    area.right()
                };
                let mut column = area.x;
                while column < area.right() {
                    let next = column + buffer[(column, *row)].symbol().width().max(1) as u16;
                    if column < right && next > left {
                        for cell in column..next.min(area.right()) {
                            buffer[(cell, *row)].set_style(style);
                        }
                    }
                    column = next;
                }
            }
            return;
        }
        if self.screen.area != buffer.area {
            self.clear();
        } else if let Some((start, end)) = self.bounds() {
            let changed = (start.y..=end.y).any(|row| {
                (buffer.area.x..buffer.area.right()).any(|column| {
                    self.screen[(column, row)].symbol() != buffer[(column, row)].symbol()
                })
            });
            if changed {
                self.clear();
            }
        }
        self.screen.clone_from(buffer);
        if let Some((start, end)) = self.bounds() {
            for row in start.y..=end.y {
                let columns = self.columns(row, start, end);
                let mut column = buffer.area.x;
                while column < buffer.area.right() {
                    let next = column + buffer[(column, row)].symbol().width().max(1) as u16;
                    if column < columns.end && next > columns.start {
                        for cell in column..next.min(buffer.area.right()) {
                            buffer[(cell, row)].set_style(style);
                        }
                    }
                    column = next;
                }
            }
        }
    }
}

fn transcript_text(transcript: &TranscriptSelection) -> Option<String> {
    let (start, end) = (transcript.anchor, transcript.cursor);
    if start == end {
        return None;
    }
    let (first, last) = ordered_points(start, end);
    let (start_key, end_key) = (first.key, last.key);
    let (start_column, end_column) = (first.column, last.column);
    let mut lines = Vec::new();
    for (key, snapshot) in transcript.rows.range(start_key..=end_key) {
        let left = if *key == start_key {
            start_column
        } else {
            transcript.area.x
        };
        let right = if *key == end_key {
            end_column
        } else {
            transcript.area.right()
        };
        let mut line = String::new();
        let mut column = transcript.area.x;
        for (grapheme, width) in &snapshot.cells {
            let next = column + *width as u16;
            if column < right && next > left {
                line.push_str(grapheme);
            }
            column = next;
        }
        lines.push(line.trim_end().to_owned());
    }
    let text = lines.join("\n");
    (!text.is_empty()).then_some(text)
}

fn ordered_points(anchor: RowPoint, cursor: RowPoint) -> (RowPoint, RowPoint) {
    if anchor <= cursor {
        (anchor, cursor)
    } else {
        (cursor, anchor)
    }
}

fn clamp_pointer(position: Position, area: Rect) -> Position {
    // The right edge is an exclusive endpoint: clamping beyond the last cell
    // keeps that cell inside the selection.
    Position::new(
        position.x.clamp(area.x, area.right()),
        position.y.clamp(area.y, area.bottom().saturating_sub(1)),
    )
}

fn edge_for(area: Rect, pointer: Position) -> Option<Edge> {
    if area.height < 2 {
        return None;
    }
    if pointer.y <= area.y {
        Some(Edge::Top)
    } else if pointer.y >= area.bottom() - 1 {
        Some(Edge::Bottom)
    } else {
        None
    }
}

fn point_at(area: Rect, position: Position, viewport: &[ViewportRow]) -> Option<RowPoint> {
    if viewport.is_empty() || area.height == 0 {
        return None;
    }
    let row = viewport
        .iter()
        .find(|row| row.row == position.y)
        .or_else(|| {
            viewport
                .iter()
                .min_by_key(|row| row.row.abs_diff(position.y))
        })?;
    Some(RowPoint {
        key: row.key,
        column: position.x,
    })
}
