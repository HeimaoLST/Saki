# Local command system

Updated: 2026-10-23. Status: implemented for TUI and console. Persistent session operations are reused; no new model/tool loop is introduced.

## Boundary

A local command is user input addressed to the host application. It is not a model message, an LLM function call, or a new session-management implementation.

```text
Composer / console input
  -> classify and parse
     -> ordinary message -> SessionRequest{request_id, Send} -> Runtime
     -> local command -> application handler
                        -> SessionRequest{request_id, session operation}
     <- SessionEventPacket{event, association, request_id}
        Accepted (send) / Opened / Listed / Renamed / TitleUpdated / Error
        Agent (full session/task/message) / Finished (unique terminal)
```

Parsing must happen before the frontend removes the draft, appends a visible user message, or marks the Agent as running. A submitted message is not removed or displayed as running until the Runtime `Accepted` reply carries its full association. Successful local commands do not enter Conversation/history.jsonl as user messages. Session operations can still create their normal metadata/history changes.

The parser accepts text and returns a typed intent or an argument error. It does not import Ratatui, access the filesystem, call a model, execute shell commands, or hold an Agent. Session lifecycle and persistence remain in the Runtime/service ([runtime.rs](../../src/session/runtime.rs), [store.rs](../../src/session/store.rs)); the terminal manager only adapts request IDs and packets.

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

Session actions use host-assigned monotonic `request_id` values on the bounded `SessionRequest` channel. Replies are matched by `request_id`; Agent/Finished events are matched by the packet’s full `(session_id, task_id, message_id)` association. Frontend actions such as help are applied by the host. Agent, Conversation, client, protocol and tools do not understand slash syntax.

While another session is running:

- `/help`, `/sessions`, `/rename`, `/new`, and `/resume` remain locally usable; viewing/creating/switching B does not cancel A.
- An ordinary message to a session with an active task (or full Runtime capacity) is rejected with the Runtime’s verified `busy` code/reason; the exact draft text/images/cursor stays editable and is not auto-retried. Explicit retry is another Send.
- Command/Agent feedback never fabricates a second terminal; only `Finished` finalizes the task. Cancel acknowledgment (`requested`/`already_requested`/`not_running`) is not a terminal.

The TUI command draft is consumed only after its matching `request_id` successful response and only if the draft still equals the dispatched snapshot. A parse error, busy response, missing session, failed rename, or a draft changed/switched after dispatch preserves the current editor. The plain console echoes rejected input with `Input kept:` so it can be retried; it does not auto-requeue. Opening a session through `/new` or `/resume` consumes the command text before caching the old session state; otherwise returning could restore an executed command as an unsent draft.

## TUI

A small completion list appears above the composer while typing a command name. Tab or a mouse click completes the selected name, Up/Down navigate the list, and Escape closes it without erasing input. Enter executes the exact typed command; selecting a suggestion does not execute it. Command completion must take priority over input-history navigation only while this list is active. Ordinary text retains the current editing and history behavior.

Descriptions and usage are shown on demand. No permanent keyboard legend or command catalog is added to the status line. Ctrl+S and clicking the session title remain direct routes to the picker.

## Implementation sequence

1. Implement the pure input classifier/parser and command definitions, with boundary tests.
2. Integrate dispatch before submission in console and TUI, reusing session requests and feedback paths.
3. Add completion/help presentation and verify draft preservation, Busy behavior, literal text, and zero model requests for local commands.

## Implementation and validation

- [Command module](../../src/command/mod.rs): pure text classification, argument validation, one static vocabulary, and translation to typed session requests.
- [Session requests](../../src/frontend/session_requests.rs): host request IDs, bounded `try_send`, pending-send/command/cancel ownership.
- [TUI commands](../../src/frontend/tui/commands.rs): completion presentation and command dispatch; no eager draft consumption.
- [TUI submission](../../src/frontend/tui/mod.rs): classification before `Accepted`, visible user records, or Agent execution; per-session projections route associated packets.
- [Console](../../src/frontend/console.rs): bounded request/event consumption, current-task Ctrl-C cancel, associated HistoryRestored append-only repair, and piped EOF draining.
- [Runtime service](../../src/session/runtime.rs): authoritative sessions, task IDs/message IDs, unique Finished terminal, cancellation and title jobs. `TitleUpdated` remains non-acknowledging; `Renamed` is the explicit rename response.

Help and completion read the same definitions. Product feedback remains English. The command layer adds no API calls, database, plugin system, permanent footer legend, or persistence format.

Latest #109 all-target gate: **596 main tests + 70 support tests, 0 failed, 11 ignored**. An earlier parallel run recorded one Runtime-owned title-test failure (cause not yet determined); that raw log is preserved (`test-final2.log`, `test-pause2.log`), the test passed in isolation and in the final run. Earlier full-harness failures are preserved separately; stale/harness-only results are not rewritten as passes. New behavior tests live in [parser tests](../../tests/command/mod.rs), [TUI command tests](../../tests/tui/commands.rs), [TUI session projections](../../tests/tui/sessions.rs), and [console tests](../../tests/console/mod.rs). They cover literal paths, multiline/slash escapes, attachments/cursor retention, bounded request rejection, full task/message association guards, A-running/B-view isolation, unique Finished, associated HistoryRestored canonical repair, targeted cancel acknowledgment (not terminal), automatic-title races, outgoing-session draft caching, keyboard/mouse completion, dispatch during jobs, and EOF draining.

The actual binary was also exercised with a local mock Responses server and an isolated PTY decoded as a terminal screen. Console and TUI local commands made **zero model requests**. `/new`, `/sessions`, `/resume`, `/rename`, `/help`, invalid-input preservation, completion, literal `//help`, multiline paste, saved history, and clean exit passed. Exactly two model requests were made for the two deliberate ordinary messages, and only those messages created task records.

Evidence: `/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-commands-1o8o4iu8/report.json`. The same directory contains `terminal.ansi` and `console.txt`. These checks used mock model responses and did not change the user's existing tmux panes or clipboard.

2026-10-23 Runtime consumer facts: terminal requests are bounded `SessionRequest{request_id, command}` with host IDs and `try_send`; replies are matched by request_id and task events by full `(session_id,task_id,message_id)`. Send is committed/displayed only on `Accepted`; rejection retains the exact editor draft and creates no User/Activity/running record. Console Ctrl-C cancels only the current observed task with its exact session/task and its acknowledgment is not terminal. Console `HistoryRestored` appends the authoritative canonical body for the associated task without replaying that task’s terminal; the one ensuing `Finished` reports status/error/persistence/history_required. No second protocol, parser, or unbounded queue is used.

Known console lifecycle limitation (#109): before this migration an uncaught SIGINT terminated the console process. The new handler requests an orderly exit by ending the run loop; however, Tokio's pending stdin blocking read can hold runtime shutdown after that, so an idle second Ctrl+C may leave the process alive until EOF. `/help` continuation after the one cancelled `Finished` works, and EOF is the scoped exit. This is a concrete remaining bug, not preexisting passing behavior; no stdin-reader redesign was introduced in this checkpoint. Ctrl+C acknowledgement is never treated as a terminal.

Remaining scope: argument/session-ID completion, aliases, persistent command history, plugins, model switching, manual compact, and background/offscreen cancellation UI.
