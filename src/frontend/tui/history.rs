use crate::agent::message::UserMessage;

/// Per-process input history owned by the TUI.
///
/// It is built only from submitted drafts and Ctrl+C-cleared drafts (text plus
/// attachments), never from the conversation, so compaction cannot rewrite it.
/// It is not persisted.
#[derive(Default)]
pub struct InputHistory {
    entries: Vec<UserMessage>,
    /// Index of the entry currently recalled, or `None` when not navigating.
    position: Option<usize>,
}

impl InputHistory {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    pub fn navigating(&self) -> bool {
        self.position.is_some()
    }

    /// The recalled draft, used to decide whether Up/Down may keep navigating.
    pub fn recalled(&self) -> Option<&UserMessage> {
        self.position.and_then(|index| self.entries.get(index))
    }

    /// Adds a submitted or cleared draft. Adjacent duplicates are collapsed.
    pub fn push(&mut self, message: impl Into<UserMessage>) {
        let message = message.into();
        if message.is_empty() {
            return;
        }
        if self.entries.last() != Some(&message) {
            self.entries.push(message);
        }
        self.position = None;
    }

    /// Older entry, or `None` when there is nothing older to recall.
    pub fn older(&mut self) -> Option<UserMessage> {
        if self.entries.is_empty() {
            return None;
        }
        let next = match self.position {
            None => self.entries.len() - 1,
            Some(0) => return None,
            Some(index) => index - 1,
        };
        self.position = Some(next);
        Some(self.entries[next].clone())
    }

    /// Newer entry, or `None` when navigating past the newest (the caller then
    /// restores an empty draft).
    pub fn newer(&mut self) -> Option<UserMessage> {
        let position = self.position?;
        if position + 1 < self.entries.len() {
            self.position = Some(position + 1);
            Some(self.entries[position + 1].clone())
        } else {
            self.position = None;
            None
        }
    }
}
