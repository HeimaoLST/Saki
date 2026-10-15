use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

/// Wraps text at grapheme boundaries, returning each line with the byte offset
/// in `text` where its body starts.
pub(crate) fn wrap_bytes(text: &str, width: usize) -> Vec<(String, usize)> {
    let width = width.max(1);
    let mut out = Vec::new();
    let mut current = String::new();
    let mut used = 0usize;
    let mut byte = 0usize;
    let mut start = 0usize;
    for grapheme in text.graphemes(true) {
        if grapheme == "\n" {
            out.push((std::mem::take(&mut current), start));
            used = 0;
            byte += 1;
            start = byte;
            continue;
        }
        let width_of = UnicodeWidthStr::width(grapheme).max(1);
        if used + width_of > width && !current.is_empty() {
            out.push((std::mem::take(&mut current), start));
            used = 0;
            start = byte;
        }
        current.push_str(grapheme);
        used += width_of;
        byte += grapheme.len();
    }
    out.push((current, start));
    out
}

/// Wraps words without splitting them; a word wider than the line falls back to
/// grapheme wrapping. Used for fallback field labels.
pub(crate) fn wrap_words(text: &str, width: usize) -> Vec<String> {
    let width = width.max(1);
    let mut out = Vec::new();
    let mut line = String::new();
    let mut used = 0usize;
    for word in text.split_whitespace() {
        let word_width = UnicodeWidthStr::width(word);
        if word_width > width {
            if !line.is_empty() {
                out.push(std::mem::take(&mut line));
                used = 0;
            }
            for (part, _) in wrap_bytes(word, width) {
                out.push(part);
            }
            continue;
        }
        let needed = if used == 0 {
            word_width
        } else {
            word_width + 1
        };
        if used + needed > width {
            out.push(std::mem::take(&mut line));
            used = 0;
        }
        if used > 0 {
            line.push(' ');
            used += 1;
        }
        line.push_str(word);
        used += word_width;
    }
    if !line.is_empty() || out.is_empty() {
        out.push(line);
    }
    out
}
