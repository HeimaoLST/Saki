# Session management

Updated: 2026-10-20. Status: implemented. Local slash commands are implemented; see [Local command system](command-system.md). The accepted machine API contract and phased runtime work are documented in [Runtime API](runtime-api.md); implementation is delivered in #60.

## Behavior

SAKI supports multiple persistent conversations with **one active job at a time**. A personal-assistant host can always select the same session ID; a development host can select different IDs for different jobs. Both use the same Agent loop.

Plain startup always creates a fresh session; it never restores stored history implicitly. Existing history is restored only through an explicit `SAKI_SESSION=<id>`, `/resume <id>`, or the session picker. `SAKI_SESSION=new` explicitly requests the same fresh-session behavior as plain startup. Opening a session reads history; it does not call the model, resume an unfinished job, or execute tools. Opening also takes an exclusive journal lock; because `fork(2)` copies the descriptor table, a child that has not `exec`'d yet can briefly keep a released lock alive, so lock acquisition retries for a bounded window before reporting the session as locked.

A session preserves its model route, original system message, working directory, and compaction configuration. Current environment defaults apply to new sessions. This prevents opening an old session under an unrelated model's context budget. API credentials and the current endpoint remain host configuration and are never written into session metadata. A saved custom model route must still be supported by that endpoint.

A session also records the wire protocol it was created on (`api`: `openai-responses` or `openai-chat-completions`) and can only be opened while the running client speaks the same protocol. Startup and `/resume` share one check entry, `SessionStore::open_for_api`, which compares the saved API before restoring history: opening a mismatched session fails explicitly, makes no model request, and leaves the stored history and the currently active session unchanged. There is no automatic protocol migration in this version.

Session isolation covers conversation state and output directories. Two sessions using the same working directory still modify the same files. Switching sessions does not roll back the filesystem, create a worktree, or provide a sandbox.

A session is not SAKI's long-term identity. Shared beliefs, experiences, and relationship history remain outside this implementation; session contents are not automatically mixed into one another's prompts.

## Use

| Entry | Behavior |
| --- | --- |
| `cargo run` | Create and start a new session; stored history is not restored implicitly |
| `SAKI_SESSION=new cargo run` | Start a new session explicitly (same as plain startup) |
| `SAKI_SESSION=<id> cargo run` | Open a specific session; fail explicitly if unavailable or locked |
| Console: `/resume <id>` | Open an existing session by exact ID (explicit restore) |
| `SAKI_SESSION_DIR=/path/to/sessions cargo run` | Use a different storage root |
| TUI: Ctrl+S or click the session title | Open the session picker |
| Picker: Up/Down, Enter | Select and open a session |
| Picker: / then type | Filter the listed sessions locally; terms are case-insensitive and all must match title, ID, working directory or model |
| Picker: Up/Down while searching | Move through the matching results only |
| Picker: n | Create and open a new session (outside search) |
| Picker: r | Rename the selected session; Enter saves, Esc cancels (outside search) |
| Picker: Esc in search | Clear the search field and keep the picker open |
| Picker: Esc or Ctrl+S | Close the picker (Esc closes after search is cleared) |

The picker shows titles, model routes, working directories, recent activity, and unavailable/in-use/interrupted states. It is displayed on demand; there is no permanent sidebar or shortcut list. A local `/` search filters the already-listed summaries: terms are case-insensitive, whitespace-separated and must all match the title, ID, working directory or model. An empty query shows all sessions in the existing recent-activity order; no matches show an explicit empty state, and Enter or `r` do nothing. Backspace is grapheme-safe, and while the field is active `n` and `r` are query text, not create/rename shortcuts. Search is frontend-only and never reaches core. New sessions start with the default title `New session` and a persisted `pending` provenance. After the first successful turn, one bounded background job asks the active model (tools disabled, small text-only excerpt, 30 s timeout) for a one-line title. Dispatch persists `auto` provenance first, so a failed, empty, timed-out, or restarted attempt is never retried; a manual rename records `manual` and always wins. Generated titles are English by design; only new sessions are eligible, and legacy metadata is never auto-titled. Metadata written before this feature has no provenance and keeps its explicit title.

The TUI keeps each session's unsent draft, image attachments, reading position, selected record, and reasoning/tool expansion state during in-process switching. Reopening reads disk history again, so a cached view cannot hide newly saved messages. Cross-restart restoration of unsent drafts is not implemented. Restored reasoning is collapsed by default.

While a job runs, listing and renaming remain available. Switching, creating an active session, and sending another message return Busy. The TUI preserves the draft and asks the user to wait. While a job runs, Esc in the TUI (or `SessionCommand::Cancel { session_id }`) requests cancellation for the active session. Cancellation is cooperative: the Agent stops at await boundaries, persists already-streamed assistant partials in order, closes pending tool pairs as interrupted without replaying them, writes `TaskFinished { Interrupted }`, and emits exactly one terminal `TurnInterrupted`. Stale, repeated, or other-session cancels are harmless no-ops. Bash cancellation kills the whole process group, including descendants. Cancellation is not transactional: a partially started filesystem write from an in-flight tool may have finished or may remain half-written; inspect state before retrying. For an interrupted or unfinished turn, streamed public reasoning remains UI-only and is never persisted as a provider payload; normal completed turns keep their existing reasoning persistence. Only assistant text is made durable on cancellation (final messages always, plus the ordered streamed assistant partials). Closing the input connection allows the current job to finish before the worker exits; background title jobs are aborted when the manager stops.

The console restores historical messages, reasoning, tool markers, compact records, and failure notices. Piped input is sent one message at a time and EOF does not discard an in-flight reply. Console callers can also use `/new`, `/sessions`, `/resume <id>`, and `/rename <title>`; startup configuration remains supported.

## Modules and dependencies

| Component | Responsibility |
| --- | --- |
| [SessionStore](../../src/session/store.rs) | Session directories, metadata, create/list/open/rename |
| [SessionManager](../../src/session/mod.rs) | Active Agent lifecycle and typed request/event routing |
| [Conversation](../../src/agent/conversation.rs) | Active model context, usage baseline, compact, and task persistence boundaries |
| [Journal](../../src/agent/journal.rs) | One JSONL format, append writer, validation and replay |
| [Agent](../../src/agent/mod.rs) | Existing model/tool loop; saves each tool result before proceeding |
| [TUI sessions](../../src/frontend/tui/sessions.rs) | Picker interaction and per-session presentation cache |
| [app](../../src/app.rs) | Configuration, startup selection, channels and frontend assembly |

The journal lives under `agent/` so Conversation and the standalone experiment can use persistence without depending on SessionManager, a frontend, or application assembly. SessionStore reuses that same journal reader. There is no second parser for UI history.

Frontends exchange `SessionCommand` and `SessionEvent`. Agent events are wrapped as `SessionEvent::Agent { session_id, event }`; the AgentEvent variants remain unchanged. An `Opened` event supplies session metadata, a history/context snapshot, and current context information before subsequent job events. Frontends ignore events for other session IDs.

`SessionCommand` is a typed core request, not a slash-command parser. Available requests are Send, List, Create, Open, and Rename. `SessionStore::create` returns an ID without selecting it; the manager's Create request combines creation and opening for the current application. Agent can still be called directly without session management or a TUI.

## Storage

```text
.saki/
  sessions/
    <session-id>/
      metadata.json
      history.jsonl
      tool-output/
```

`metadata.json` contains schema version, stable ID, editable title plus title provenance (`pending`/`auto`/`manual`; missing means legacy/manual), creation time, working directory, model route, API identifier, and compaction settings. Updates use a temporary file and rename. No separate SQLite database or index is maintained.

`history.jsonl` is authoritative for conversation and execution history. New records have `schema_version: 1`, a millisecond timestamp, and one of these types:

| Record | Meaning |
| --- | --- |
| `messages` | Newly appended protocol items, including original image snapshots and provider continuation data |
| `compaction` | Summary, `keep_from`, before/after context size and summary usage |
| `task_started` | A user task began, identified by task ID |
| `task_finished` | Matching completion, failure, or recovered interruption, with error details when applicable |

There is one write lock per journal, held for the loaded Conversation's lifetime. A second process cannot open the same session for writing. The OS releases the lock when the process exits. Listing does not require taking over the active session. Writes synchronize journal data before reporting success; this is not an exactly-once transaction across external tool side effects and the journal.

Listing scans session journals and reads metadata, then sorts by recent activity. Older records without timestamps use file modification time as a fallback. The picker's local search filters this in-memory summary list only; there is no persistent or incremental search index yet, so very large collections may need one after measuring actual listing costs.

Tool-output files remain session-local dependencies of the history. No cleanup, deletion, export, automatic workspace relocation, or attachment migration is introduced.

Session, task, message and context identifiers are opaque strings. New IDs include a process-local sequence so parallel calls in the same clock tick cannot collide; existing IDs remain valid without renaming or migrating stored directories. The generator is covered by a fixed-timestamp concurrent regression, rather than relying on wall-clock timing.

## Immutable context resources (#62 P1)

Implementation candidate (2026-10-24): the Runtime exposes journal-derived `history` and compacted `working` resources through `context.open/read/search/release`. Reading does not load an Agent, acquire its journal write lock, repair a torn tail, or start model/MCP execution. The resource captures a fixed file prefix; an ongoing append or compaction cannot alter an existing reference. This is separate from the mutable UI history projection and from the most recent actual model request.

The shared journal decoder/reducer has a bounded prefix entry point. The resource table reserves capacity before reading/decoding/projecting, including conservative structural and clone allowances, and rejects excess work rather than allocating first. It currently permits one preparation reader, up to 32 references, a 256 MiB accounting budget and a 64 MiB source ceiling; effective values and the ten-minute idle TTL are returned in capabilities. Read/search construction reserves a separate 2 MiB operation allowance from that same pool, and even an empty source requires the reader buffer allowance before I/O allocation. Accounting estimates are not a claim to measure allocator RSS. Read/search work runs outside the actor control loop; task cancellation and reference release retain their own lifecycle.

Full contract and phased status: [Session context access](session-context-access.md). Textual source positions belong to their particular reference, and the `working` view is rebuilt from the committed journal, rather than invented from streamed deltas or guessed request configuration. Request snapshots and independent model analysis remain #135, with actual consumer/cache/quality evidence in #136.

## Replay and compact

One journal pass returns two outputs:

1. **Model context**: append original protocol items; on a compact record, replace the working prefix with the original system message and summary, retaining `keep_from..` unchanged.
2. **Display history**: retain all original message batches, compact markers and execution records. The frontend renders these without interpreting a compact summary as a new assistant reply.

The restore API installs the rebuilt working context and opens the journal for append. `persisted_items` starts at the rebuilt context length, preventing restored items from being written again. The summary flag is preserved so later compact operations use the same legal boundary rules.

Usage baselines are not restored. Conversation uses its existing estimate until the next response provides usage. Restoring does not invent measured token counts. Streaming deltas remain display-only: only completed model output is saved, so unfinished text shown before a crash may disappear after reopening.

Existing version-1 message/compact journals can be replayed by Conversation. Old session directories without metadata appear as unavailable in the picker and are not silently upgraded. Unsupported schema versions, invalid complete records, and broken tool/checkpoint structure are reported with a path and line number.

An incomplete JSON record at EOF without a newline is a torn tail. Under the write lock, restore truncates to the last complete record before appending. A valid final record without a newline is retained and separated before append. Complete JSON with an unsupported schema is never treated as an incomplete write. Because restore can repair a torn tail, the expected-API check runs before restore (see Behavior): a protocol mismatch never rewrites the history file.

## Interrupted execution

A task-start record without its matching task finish is Interrupted. Older histories without task boundaries have Unknown completion status. Opening only reports that state; it does not execute anything or fabricate a model answer.

When the user next sends a message, Conversation records the previous task's interruption and closes any unmatched tool calls with explicit interrupted results. These state that the tool may already have run and its external side effects must be inspected before retrying. Recorded results are retained. No earlier call is automatically replayed.

Each completed tool result is saved before the next tool begins. A crash during a later tool therefore does not discard earlier results from the same batch. There is still an unavoidable interval between a tool causing an external effect and saving its result; this version deliberately treats that outcome as uncertain rather than promising exactly-once execution.

If a target session fails to load, the existing active Agent and its frontend draft remain available. If journal persistence fails, the task reports failure rather than a reliable saved success.

## Validation and limits

Behavior tests are grouped in [session tests](../../tests/session/mod.rs), [conversation tests](../../tests/conversation/mod.rs), and [TUI session tests](../../tests/tui/sessions.rs). They cover A/B/A context and model separation, session working directories, save/reopen, Busy handling, failed-open preservation, file locking, invalid versions, torn-tail recovery, interrupted tools, draft/attachment/view preservation, session ID filtering, and picker search (metadata matching, empty state, key precedence, grapheme-safe editing, and selection stability across rename/title updates). Existing tests now exercise production restore after repeated compact checkpoints and compare native requests containing images and reasoning before/after restore. M1 adds the API mismatch cases (2026-10-16): `a_session_created_on_another_api_is_not_opened_or_changed` (the refused open leaves the active session and files untouched) and `cross_api_open_with_torn_tail_fails_before_repairing_the_history` (fails before restore, history bytes unchanged, while the matching API still repairs the tail).

- Historical all-target regression (2026-10-13): **197 main tests + 40 experiment tests passed**. Formatting, build and whitespace checks also passed.
- Current acceptance for automatic titles and cancellation (2026-10-15, frozen binary SHA `63b74e217c5d239e…`): combined serial suite passed **282 main + 40 experiment tests** with `cargo fmt --all -- --check` clean; the independent 5-scenario PTY harness passed 5/5; a real Magpie gateway live smoke (`opencode-go/deepseek-v4.1-flash`) completed with no tool calls and generated the title `Simple Greeting Request and Response` (report: `/tmp/saki-title-cancel-qa/live_report.json`).

Additional acceptance used the actual binary with an isolated mock Responses server and temporary session roots:

- Fresh-startup default (current): two plain console startups created two distinct sessions with no restored transcript and no model connection; explicit `SAKI_SESSION=<id>` restored the seeded transcript; a missing or locked explicit ID failed without creating or falling back. Test: [startup regression](../../tests/console/startup.rs). Independent compiled-binary acceptance also confirmed two plain launches created distinct IDs and explicit reopen created no additional session. Combined serial verification on 2026-10-15 passed 251 main tests and 40 experiment tests, with 6 manual fixtures ignored; log: `/tmp/saki-color-session-supervisor-tests.log`.
- Historical (implicit resume existed then): two process lifetimes: automatic resume, restored visible history, saved context sent on the next request, cross-process lock rejection, and zero model calls during open. Evidence: `/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-session-process-omyhw7sa/report.json`.
- Hard-kill after a completed write tool: the saved result survived, the lock was released, reopening reported Interrupted, continuing recorded the interruption, and the write was not replayed. Evidence: `/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-session-crash-e60gvqja/report.json`.
- Real PTY with terminal-screen decoding: create/open/rename through the picker, A/B draft restoration, and clean exit. Evidence: `/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-session-tui-qx05xgyc/report.json`.

These session checks used local mock model responses; no new real-provider request was required. Existing user tmux panes and running sessions were not restarted or modified.

Remaining scope: parallel/background jobs, session deletion/export/fork, old-directory migration, full-text history search (the picker only filters list metadata), cross-restart drafts, library packaging, and shared long-term identity.

## Local command system

The command layer should parse local user intent and dispatch existing typed operations. It should not introduce a second session implementation or send local commands as ordinary model messages.

The implemented vocabulary is `/new`, `/sessions`, `/resume <id>`, `/rename <title>`, and `/help`. TUI picker buttons/keys and slash commands call the same session handlers. The command implementation documents completion, argument errors, literal-slash escaping, draft preservation, and Busy feedback. TUI Esc cancellation and `SessionCommand::Cancel` are implemented and accepted (see Behavior). Future commands such as model switching or manual compact should be added only alongside their own supported core operations.

Implemented local input routing: [Local command system](command-system.md).

## Machine host

The `--stdio` entry exposes the accepted [runtime API](runtime-api.md) through `src/session/runtime.rs`. It starts without selecting or creating a session, retains loaded sessions by ID, and permits loading/viewing B while A executes. One task executes at a time in this first phase; a second send returns busy. It reuses the same Agent builder, tool loop and journal reader as the terminal host. New TaskStarted records optionally include the caller-visible message_id; legacy records remain readable without invented IDs. Machine history pages include record offsets and Unix millisecond timestamps (0 when absent in old records).
