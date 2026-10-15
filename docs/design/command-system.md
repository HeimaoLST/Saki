# Local command system

Updated: 2026-10-13. Status: implemented for TUI and console. Persistent session operations are reused; no new model/tool loop is introduced.

## Boundary

A local command is user input addressed to the host application. It is not a model message, an LLM function call, or a new session-management implementation.

```text
Composer / console input
  -> classify and parse
     -> ordinary message -> SessionCommand::Send -> Agent
     -> local command -> application handler
                        -> existing session operation or frontend action
```

Parsing must happen before the frontend removes the draft, appends a visible user message, or marks the Agent as running. Successful local commands do not enter Conversation/history.jsonl as user messages. Session operations can still create their normal metadata/history changes.

The parser accepts text and returns a typed intent or an argument error. It does not import Ratatui, access the filesystem, call a model, execute shell commands, or hold an Agent. Session lifecycle and persistence remain in [SessionManager](../../src/session/mod.rs) and [SessionStore](../../src/session/store.rs).

## Initial vocabulary

| Command | Contract |
| --- | --- |
| `/new` | Create and open a session using the host defaults; no arguments |
| `/sessions` | TUI opens the existing picker; console displays the session list |
| `/resume <id>` | Open the exact ID through the existing session operation |
| `/rename <title>` | Rename the current session; the rest of the line is the title |
| `/help` | Display command and interaction help through the current frontend |

`/resume <id>` gives console users a switching path without depending on a graphical picker. The session picker and slash commands must dispatch the same core requests. `/help` has no model or session mutation requirement.

Do not add `/compact`, `/model`, `/cancel`, `/delete`, or plugin commands in this first vocabulary. They need their own core operations and behavior contracts. Existing automatic compact continues independently.

## Input rules

- Classify a command only for a single-line input whose first non-whitespace character is `/`.
- Match command names exactly, without case folding or guessed aliases.
- For `/rename`, use the remaining trimmed text as the title; no shell expansion or quoting language is needed.
- `/resume` requires one exact ID. Missing/extra arguments produce local usage feedback.
- An unknown simple command such as `/resuem` produces a local error and keeps the draft. It is not silently sent to the model.
- A token that is clearly an absolute path, such as `/Users/me/src/main.rs`, remains ordinary input. To explicitly send command-looking text, use `//`; remove one leading slash from the outgoing message.
- Multiline pasted content remains a model message, even when its first line starts with `/`. Pasting text never executes a command before the user presses Send.
- A recognized command with image attachments reports a local error and retains the attachments. Commands must not silently discard images or redirect them into a new session.

These rules are covered by parser and frontend behavior tests. A root filename containing a dot, such as `/README.md`, also stays a message. A bare single-component path such as `/tmp` is ambiguous and uses the unknown-command path; `//tmp` explicitly sends it as text. Multiline submission applies to the TUI/API parser: the plain console remains line-oriented and handles each input line separately.

## Dispatch and feedback

Keep a small static definition list for command name, description and usage. Help and completion read this list so their supported vocabulary stays consistent. A straightforward typed enum and match-based dispatcher are sufficient; no dynamic plugin registry is needed.

Session actions use existing SessionCommand variants. Frontend actions such as showing help are applied by the host. Agent, Conversation, client, protocol and tools do not need to understand slash syntax.

While a job is running:

- `/help`, `/sessions`, and `/rename` remain usable.
- `/new`, `/resume`, and ordinary model messages report Busy and retain the draft.
- Command feedback does not emit TurnCompleted/TurnFailed for the active Agent job.

The TUI command draft is consumed after its matching successful session response, rather than when it is merely sent. A parse error, Busy response, missing session, or failed rename preserves it. The plain console echoes rejected input with `Input kept:` so it can be retried; it does not provide a TUI draft editor. Opening a session through `/new` or `/resume` must consume the command text before caching the old session's remaining frontend state; otherwise returning to the old session could restore the already executed command as an unsent draft.

## TUI

A small completion list appears above the composer while typing a command name. Tab or a mouse click completes the selected name, Up/Down navigate the list, and Escape closes it without erasing input. Enter executes the exact typed command; selecting a suggestion does not execute it. Command completion must take priority over input-history navigation only while this list is active. Ordinary text retains the current editing and history behavior.

Descriptions and usage are shown on demand. No permanent keyboard legend or command catalog is added to the status line. Ctrl+S and clicking the session title remain direct routes to the picker.

## Implementation sequence

1. Implement the pure input classifier/parser and command definitions, with boundary tests.
2. Integrate dispatch before submission in console and TUI, reusing session requests and feedback paths.
3. Add completion/help presentation and verify draft preservation, Busy behavior, literal text, and zero model requests for local commands.

## Implementation and validation

- [Command module](../../src/command/mod.rs): pure text classification, argument validation, one static vocabulary, and translation to typed session requests.
- [TUI commands](../../src/frontend/tui/commands.rs): completion presentation, pending command state, Busy feedback, and acknowledgement-driven draft consumption.
- [TUI submission](../../src/frontend/tui/mod.rs): classification before `Editor::take`, visible user records, or Agent execution.
- [Console](../../src/frontend/console.rs): local commands remain usable while a job runs; piped ordinary messages are drained sequentially before EOF closes the worker input.
- [Session manager](../../src/session/mod.rs): automatic title notifications use `TitleUpdated`, while explicit rename responses use `Renamed`. A delayed automatic title notification cannot acknowledge `/rename`, clear its draft, or unlock pending input.

Help and completion read the same definitions. Product feedback remains English. The command layer adds no API calls, database, plugin system, permanent footer legend, or persistence format.

All-target regression passed: **197 main tests + 40 experiment tests**. New behavior tests live in [parser tests](../../tests/command/mod.rs), [TUI command tests](../../tests/tui/commands.rs), and [console tests](../../tests/console/mod.rs). They cover literal paths, multiline and slash escapes, invalid arguments, attachments, Busy state, successful/failed acknowledgements, automatic-title races, outgoing-session draft caching, keyboard/mouse completion, command dispatch during jobs, and EOF draining.

The actual binary was also exercised with a local mock Responses server and an isolated PTY decoded as a terminal screen. Console and TUI local commands made **zero model requests**. `/new`, `/sessions`, `/resume`, `/rename`, `/help`, invalid-input preservation, completion, literal `//help`, multiline paste, saved history, and clean exit passed. Exactly two model requests were made for the two deliberate ordinary messages, and only those messages created task records.

Evidence: `/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-commands-1o8o4iu8/report.json`. The same directory contains `terminal.ansi` and `console.txt`. These checks used mock model responses and did not change the user's existing tmux panes or clipboard.

Remaining scope: argument/session-ID completion, aliases, persistent command history, plugins, model switching, manual compact, and cancellation commands.
