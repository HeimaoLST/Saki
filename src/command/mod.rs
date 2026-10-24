use crate::session::SessionCommand;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommandKind {
    New,
    Sessions,
    Resume,
    Rename,
    Help,
}

pub struct Definition {
    pub kind: CommandKind,
    pub name: &'static str,
    pub usage: &'static str,
    pub description: &'static str,
}

pub const DEFINITIONS: &[Definition] = &[
    Definition {
        kind: CommandKind::New,
        name: "new",
        usage: "/new",
        description: "Create and open a session",
    },
    Definition {
        kind: CommandKind::Sessions,
        name: "sessions",
        usage: "/sessions",
        description: "Browse saved sessions",
    },
    Definition {
        kind: CommandKind::Resume,
        name: "resume",
        usage: "/resume <id>",
        description: "Open a session by ID",
    },
    Definition {
        kind: CommandKind::Rename,
        name: "rename",
        usage: "/rename <title>",
        description: "Rename the current session",
    },
    Definition {
        kind: CommandKind::Help,
        name: "help",
        usage: "/help",
        description: "Show local commands and help",
    },
];

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Command {
    New,
    Sessions,
    Resume { session_id: String },
    Rename { title: String },
    Help,
}

#[derive(Debug, PartialEq, Eq)]
pub enum ParsedInput {
    Message(String),
    Command(Command),
}

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum CommandError {
    #[error(
        "Unknown command /{0}. Use /help to see available commands, or // to send literal text."
    )]
    Unknown(String),
    #[error("Usage: {0}")]
    Usage(&'static str),
    #[error(
        "Local commands do not accept images. Remove attachments or use // to send literal text."
    )]
    Attachments,
}

/// Classifies one submission without executing it or consuming its draft.
/// Multiline content and absolute paths stay ordinary messages.
pub fn parse(text: &str, has_images: bool) -> Result<ParsedInput, CommandError> {
    let trimmed = text.trim_start();
    if text.contains(['\n', '\r']) || !trimmed.starts_with('/') {
        return Ok(ParsedInput::Message(text.to_owned()));
    }
    if let Some(literal) = trimmed.strip_prefix("//") {
        return Ok(ParsedInput::Message(format!(
            "{}/{literal}",
            &text[..text.len() - trimmed.len()]
        )));
    }
    let (name, argument) = trimmed[1..]
        .split_once(char::is_whitespace)
        .map_or((&trimmed[1..], ""), |(name, rest)| (name, rest.trim()));
    if name.contains('/') || name.contains('.') {
        return Ok(ParsedInput::Message(text.to_owned()));
    }
    let definition = DEFINITIONS
        .iter()
        .find(|definition| definition.name == name)
        .ok_or_else(|| CommandError::Unknown(name.to_owned()))?;
    if has_images {
        return Err(CommandError::Attachments);
    }
    let usage = || CommandError::Usage(definition.usage);
    let command = match definition.kind {
        CommandKind::New if argument.is_empty() => Command::New,
        CommandKind::Sessions if argument.is_empty() => Command::Sessions,
        CommandKind::Help if argument.is_empty() => Command::Help,
        CommandKind::Resume if !argument.is_empty() && argument.split_whitespace().count() == 1 => {
            Command::Resume {
                session_id: argument.to_owned(),
            }
        }
        CommandKind::Rename if !argument.is_empty() => Command::Rename {
            title: argument.to_owned(),
        },
        _ => return Err(usage()),
    };
    Ok(ParsedInput::Command(command))
}

/// The completion applies only to the command-name portion of a single line.
pub fn completions(text: &str) -> Vec<&'static Definition> {
    if text.contains(['\r', '\n']) {
        return Vec::new();
    }
    let trimmed = text.trim_start();
    let Some(prefix) = trimmed.strip_prefix('/') else {
        return Vec::new();
    };
    if prefix.contains(char::is_whitespace) || prefix.starts_with('/') {
        return Vec::new();
    }
    DEFINITIONS
        .iter()
        .filter(|definition| definition.name.starts_with(prefix))
        .collect()
}

impl Command {
    pub fn request(&self, active_session: &str) -> Option<SessionCommand> {
        Some(match self {
            Self::New => SessionCommand::Create,
            Self::Sessions => SessionCommand::List,
            Self::Resume { session_id } => SessionCommand::Open {
                session_id: session_id.clone(),
            },
            Self::Rename { title } => SessionCommand::Rename {
                session_id: active_session.to_owned(),
                title: title.clone(),
            },
            Self::Help => return None,
        })
    }
}

#[cfg(test)]
#[path = "../../tests/command/mod.rs"]
mod tests;
