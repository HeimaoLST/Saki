use std::io::{Read, Seek, SeekFrom};

use crate::frontend::tui::state::{App, LogView, Record};

const LOG_PAGE_BYTES: u64 = 16 * 1024;
const LOG_PAGE_LINES: usize = 300;
const MAX_LOG_LINE_BYTES: usize = 2000;

/// One bounded page of a spilled tool log, expressed as a byte range.
pub struct LogPage {
    pub text: String,
    pub next: u64,
    pub eof: bool,
}

/// Reads one bounded log page starting at `start`. The whole file is never
/// loaded. Offsets are computed from the raw read buffer, so invalid UTF-8 or a
/// page that cuts a character cannot skip or repeat bytes. A single line longer
/// than the page budget is clipped and `next` still advances.
pub fn page(path: &str, start: u64) -> LogPage {
    let mut file = match std::fs::File::open(path) {
        Ok(file) => file,
        Err(error) => {
            return LogPage {
                text: format!("(cannot open log: {error})"),
                next: start,
                eof: true,
            };
        }
    };
    if let Err(error) = file.seek(SeekFrom::Start(start)) {
        return LogPage {
            text: format!("(cannot seek log: {error})"),
            next: start,
            eof: true,
        };
    }
    let mut buffer = Vec::new();
    let read = match file.take(LOG_PAGE_BYTES).read_to_end(&mut buffer) {
        Ok(read) => read,
        Err(error) => {
            return LogPage {
                text: format!("(cannot read log: {error})"),
                next: start,
                eof: true,
            };
        }
    };
    if read == 0 {
        return LogPage {
            text: String::new(),
            next: start,
            eof: true,
        };
    }
    let full = read as u64 == LOG_PAGE_BYTES;
    let mut out = String::new();
    let mut consumed = 0usize;
    let mut stopped = false;
    for (line_count, raw) in buffer.split_inclusive(|byte| *byte == b'\n').enumerate() {
        if line_count >= LOG_PAGE_LINES {
            stopped = true;
            break;
        }
        if raw.last() != Some(&b'\n') && full {
            // The page cut mid-line; leave the remainder for the next page.
            stopped = true;
            break;
        }
        let length = raw.len();
        if length > MAX_LOG_LINE_BYTES {
            let end = boundary_bytes(raw, MAX_LOG_LINE_BYTES);
            out.push_str(&String::from_utf8_lossy(&raw[..end]));
            out.push_str(" …\n");
        } else {
            out.push_str(&String::from_utf8_lossy(raw));
        }
        consumed += length;
    }
    if consumed == 0 {
        // The first line is longer than the page budget: clip a chunk and
        // advance by it so paging always makes progress.
        let end = boundary_bytes(&buffer, MAX_LOG_LINE_BYTES).max(1);
        out.push_str(&String::from_utf8_lossy(&buffer[..end]));
        out.push_str(" …\n");
        return LogPage {
            text: out,
            next: start + end as u64,
            eof: !full && end >= buffer.len(),
        };
    }
    LogPage {
        text: out,
        next: start + consumed as u64,
        eof: !full && !stopped,
    }
}

/// Inline first-page preview for an expanded tool record.
pub fn first_page(path: &str) -> String {
    page(path, 0).text
}

/// Opens the bounded log sub-view for the selected tool, remembering the
/// reading position so Esc can restore it.
pub fn open(app: &mut App) -> bool {
    let Some(Record::Tool { output, .. }) = app.records.get(app.selected) else {
        return false;
    };
    let Some(path) = output
        .as_ref()
        .and_then(|value| value.get("output_path"))
        .and_then(serde_json::Value::as_str)
    else {
        return false;
    };
    let page = page(path, 0);
    // The log view replaces the transcript; logical row selection does not
    // cross into it.
    app.text_selection.clear();
    let saved_visual_anchor = app.visual_anchor;
    app.visual_anchor = None;
    app.log_view = Some(LogView {
        path: path.to_owned(),
        offset: 0,
        next: page.next,
        eof: page.eof,
        text: page.text,
        scroll: 0,
        history: Vec::new(),
        saved_follow: app.follow,
        saved_anchor: app.anchor,
        saved_visual_anchor,
    });
    true
}

/// Closes the log sub-view and restores the transcript reading position.
pub fn close(app: &mut App) {
    if let Some(view) = app.log_view.take() {
        if view.saved_follow {
            app.end();
        } else {
            app.follow = false;
            app.anchor = view.saved_anchor;
            app.visual_anchor = view.saved_visual_anchor;
        }
    }
}

pub fn next_page(app: &mut App) {
    let Some(view) = app.log_view.as_mut() else {
        return;
    };
    if view.eof {
        return;
    }
    let start = view.next;
    let next = page(&view.path, start);
    view.history.push(view.offset);
    view.offset = start;
    view.next = next.next;
    view.eof = next.eof;
    view.text = next.text;
    view.scroll = 0;
}

pub fn prev_page(app: &mut App) {
    let Some(view) = app.log_view.as_mut() else {
        return;
    };
    let Some(offset) = view.history.pop() else {
        return;
    };
    let previous = page(&view.path, offset);
    view.offset = offset;
    view.next = previous.next;
    view.eof = previous.eof;
    view.text = previous.text;
    view.scroll = 0;
}

/// Largest prefix length that is valid UTF-8.
fn boundary_bytes(bytes: &[u8], limit: usize) -> usize {
    let mut end = limit.min(bytes.len());
    while end > 0 && std::str::from_utf8(&bytes[..end]).is_err() {
        end -= 1;
    }
    end
}
