//! Fenced-code syntax roles via syntect.
//!
//! Only roles/byte ranges are cached, keyed by exact language token + code and
//! bounded by entries and retained bytes. Palette application happens later, so
//! width- or theme-only relayouts reuse the parse. Unknown, unlabelled and
//! indented blocks return `None` and keep the previous single-color rendering.

use std::sync::{Mutex, OnceLock};

use ratatui::style::{Modifier, Style};
use syntect::parsing::{ParseState, ScopeStack, SyntaxSet};

use super::theme::Theme;

const MAX_ENTRIES: usize = 64;
const MAX_RETAINED_BYTES: usize = 2 * 1024 * 1024;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Role {
    Plain,
    Keyword,
    String,
    Number,
    Comment,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RoleRun {
    pub start: usize,
    pub end: usize,
    pub role: Role,
}

impl RoleRun {
    pub fn style(self, theme: Theme) -> Style {
        match self.role {
            Role::Plain => Style::default().fg(theme.text),
            Role::Keyword => Style::default()
                .fg(theme.accent)
                .add_modifier(Modifier::BOLD),
            Role::String => Style::default().fg(theme.code),
            Role::Number => Style::default().fg(theme.warning),
            Role::Comment => Style::default()
                .fg(theme.secondary)
                .add_modifier(Modifier::ITALIC),
        }
    }
}

struct Entry {
    language: String,
    code: String,
    runs: std::sync::Arc<Vec<RoleRun>>,
}

struct Cache {
    entries: std::collections::VecDeque<Entry>,
    bytes: usize,
}

static SYNTAXES: OnceLock<SyntaxSet> = OnceLock::new();
static CACHE: OnceLock<Mutex<Cache>> = OnceLock::new();

fn syntaxes() -> &'static SyntaxSet {
    SYNTAXES.get_or_init(SyntaxSet::load_defaults_newlines)
}

fn cache() -> &'static Mutex<Cache> {
    CACHE.get_or_init(|| {
        Mutex::new(Cache {
            entries: std::collections::VecDeque::new(),
            bytes: 0,
        })
    })
}

/// Highlight roles for one fenced block. `None` means "keep single color".
pub fn roles(code: &str, language: &str) -> Option<std::sync::Arc<Vec<RoleRun>>> {
    let token = language.split_whitespace().next()?.to_ascii_lowercase();
    if token.is_empty() {
        return None;
    }
    if let Ok(lock) = cache().lock()
        && let Some(entry) = lock
            .entries
            .iter()
            .find(|entry| entry.language == token && entry.code == code)
    {
        return Some(entry.runs.clone());
    }
    let syntax_set = syntaxes();
    let syntax = syntax_set
        .find_syntax_by_token(&token)
        .or_else(|| syntax_set.find_syntax_by_extension(&token))?;
    if syntax.name == "Plain Text" {
        return None;
    }
    let runs = std::sync::Arc::new(parse(code, syntax, syntax_set)?);
    if let Ok(mut lock) = cache().lock() {
        if let Some(index) = lock
            .entries
            .iter()
            .position(|entry| entry.language == token && entry.code == code)
        {
            let old = lock.entries.remove(index)?;
            lock.bytes = lock.bytes.saturating_sub(old.code.len());
        }
        while !lock.entries.is_empty()
            && (lock.entries.len() >= MAX_ENTRIES || lock.bytes + code.len() > MAX_RETAINED_BYTES)
        {
            if let Some(old) = lock.entries.pop_front() {
                lock.bytes = lock.bytes.saturating_sub(old.code.len());
            }
        }
        if code.len() <= MAX_RETAINED_BYTES {
            lock.bytes += code.len();
            lock.entries.push_back(Entry {
                language: token,
                code: code.to_owned(),
                runs: runs.clone(),
            });
        }
    }
    Some(runs)
}

fn parse(
    code: &str,
    syntax: &syntect::parsing::SyntaxReference,
    syntax_set: &SyntaxSet,
) -> Option<Vec<RoleRun>> {
    let mut state = ParseState::new(syntax);
    let mut stack = ScopeStack::new();
    let mut runs: Vec<RoleRun> = Vec::new();
    let mut offset = 0usize;
    for line in code.split_inclusive('\n') {
        let ops = state.parse_line(line, syntax_set).ok()?;
        let mut last = 0usize;
        for (position, op) in ops {
            let position = position.min(line.len()).max(last);
            if position > last {
                push_run(&mut runs, offset + last, offset + position, &stack);
            }
            stack.apply(&op).ok()?;
            last = position;
        }
        if last < line.len() {
            push_run(&mut runs, offset + last, offset + line.len(), &stack);
        }
        offset += line.len();
    }
    #[cfg(test)]
    TEST_PARSES.with(|count| count.set(count.get() + 1));
    Some(runs)
}

fn push_run(runs: &mut Vec<RoleRun>, start: usize, end: usize, stack: &ScopeStack) {
    if end <= start {
        return;
    }
    let role = classify(stack);
    if let Some(last) = runs.last_mut()
        && last.role == role
        && last.end == start
    {
        last.end = end;
        return;
    }
    runs.push(RoleRun { start, end, role });
}

fn classify(stack: &ScopeStack) -> Role {
    for scope in stack.as_slice().iter().rev() {
        let name = scope.build_string();
        if name.starts_with("comment") {
            return Role::Comment;
        }
        if name.starts_with("string") {
            return Role::String;
        }
        if name.starts_with("constant.numeric") {
            return Role::Number;
        }
        if (name.starts_with("keyword") && !name.starts_with("keyword.operator"))
            || name.starts_with("storage")
        {
            return Role::Keyword;
        }
        if name.starts_with("constant") {
            return Role::Keyword;
        }
    }
    Role::Plain
}

#[cfg(test)]
thread_local! {
    static TEST_PARSES: std::cell::Cell<u64> = const { std::cell::Cell::new(0) };
}

#[cfg(test)]
pub fn test_parse_count() -> u64 {
    TEST_PARSES.with(std::cell::Cell::get)
}
