use std::time::Duration;

use crate::{
    agent::AgentEvent,
    client::ModelClient,
    protocol::{
        InputContentPart, InputItem, InputMessageContent, MessageRole, ModelRequest, OutputItem,
        OutputText,
    },
};

use super::store;

/// Longest excerpt from each side of the first exchange given to the title job.
pub(super) const TITLE_INPUT_CHARS: usize = 2000;
/// Upper bound on a stored generated title.
const TITLE_MAX_CHARS: usize = 60;
const TITLE_OUTPUT_TOKENS: Option<u32> = Some(1024);
const TITLE_TIMEOUT: Duration = Duration::from_secs(30);
const TITLE_SYSTEM_PROMPT: &str = "You write concise English conversation titles. Reply with only the title: \
                                   one line, at most 8 words, plain text, no quotes, no trailing punctuation.";

pub(super) fn truncate_chars(text: &str, max: usize) -> String {
    text.chars().take(max).collect()
}

/// Sanitizes the model's reply into a one-line title, or `None` when it is
/// empty, unparsable, or just the default title.
pub(super) fn title_from_output(output: &[OutputItem]) -> Option<String> {
    let text = output.iter().find_map(|item| match item {
        OutputItem::Message { content, .. } => {
            let joined: String = content
                .iter()
                .map(|OutputText { text }| text.as_str())
                .collect();
            (!joined.trim().is_empty()).then_some(joined)
        }
        _ => None,
    })?;
    let line = text.lines().map(str::trim).find(|line| !line.is_empty())?;
    let collapsed = line
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .trim_matches(['"', '\'', '`', '*', '#'])
        .trim()
        .to_owned();
    let title: String = collapsed.chars().take(TITLE_MAX_CHARS).collect();
    let title = title.trim_matches(['"', '\'', '`']).trim();
    (!title.is_empty() && title != store::DEFAULT_TITLE).then(|| title.to_owned())
}

pub(super) fn remember_reply(reply: &mut String, event: &AgentEvent) {
    if let AgentEvent::ResponseText { text, .. } = event {
        let remaining = TITLE_INPUT_CHARS.saturating_sub(reply.chars().count());
        if remaining > 0 {
            reply.extend(text.chars().take(remaining));
            reply.push('\n');
        }
    }
}

pub(super) fn reply_excerpt(items: &[InputItem]) -> String {
    let Some(content) = items
        .iter()
        .rev()
        .take_while(|item| {
            !matches!(
                item,
                InputItem::Message {
                    role: MessageRole::User,
                    ..
                }
            )
        })
        .find_map(|item| match item {
            InputItem::Message {
                role: MessageRole::Assistant,
                content,
            } => Some(content),
            _ => None,
        })
    else {
        return String::new();
    };
    match content {
        InputMessageContent::Text(text) => truncate_chars(text, TITLE_INPUT_CHARS),
        InputMessageContent::Parts(parts) => parts
            .iter()
            .filter_map(|part| match part {
                InputContentPart::Text { text } => Some(text.as_str()),
                _ => None,
            })
            .flat_map(str::chars)
            .take(TITLE_INPUT_CHARS)
            .collect(),
    }
}

pub(super) async fn generate(
    client: ModelClient,
    model: String,
    user_text: String,
    reply_text: String,
) -> Option<String> {
    let prompt = format!(
        "User: {}\n\nAssistant: {}",
        truncate_chars(&user_text, TITLE_INPUT_CHARS),
        truncate_chars(&reply_text, TITLE_INPUT_CHARS),
    );
    let items = [
        InputItem::Message {
            role: MessageRole::System,
            content: InputMessageContent::Text(TITLE_SYSTEM_PROMPT.to_owned()),
        },
        InputItem::Message {
            role: MessageRole::User,
            content: InputMessageContent::Text(prompt),
        },
    ];
    // Independent output budget: reasoning models may spend tokens on
    // reasoning before producing the short visible title.
    let request = ModelRequest::new(model, &items, Vec::new()).with_generation(
        TITLE_OUTPUT_TOKENS,
        None,
        None,
    );
    match tokio::time::timeout(TITLE_TIMEOUT, client.respond(request)).await {
        Ok(Ok(response)) => title_from_output(&response.output),
        _ => None,
    }
}
