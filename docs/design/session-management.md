# Session management

Updated: 2026-11-01. Status: implemented session storage and host behavior. Machine admission, status, cancellation, configuration and caller-input facts below are checked against `dev@d0502695`; see [Runtime API](runtime-api.md) for the wire contract.

## Behavior

SAKI supports multiple persistent conversations. At `dev@d0502695`, the machine Runtime defaults to `max_active_tasks=1` and supports a configured higher capacity across different sessions; each individual session still accepts at most one accepted/running task. A second send to an occupied session is rejected as `busy/session_busy`; an exhausted global capacity is `busy/capacity_exceeded`; neither rejection is queued. The terminal frontend's selected session, draft and Busy presentation are terminal-host policy, not a service-wide single-task limit. A host can reuse a session or create separate sessions, using the same Agent loop; neither choice defines the final Nagi contact model.

Plain startup always creates a fresh session; it never restores stored history implicitly. Existing history is restored only through an explicit `SAKI_SESSION=<id>`, `/resume <id>`, or the session picker. `SAKI_SESSION=new` explicitly requests the same fresh-session behavior as plain startup. Opening a session reads history; it does not call the model, resume an unfinished job, or execute tools. Opening also takes an exclusive journal lock; because `fork(2)` copies the descriptor table, a child that has not `exec`'d yet can briefly keep a released lock alive, so lock acquisition retries for a bounded window before reporting the session as locked.

Both metadata schemas retain the session's model, API/protocol, working directory and compaction configuration; configuration revision and capacity source have legacy defaults. Only explicit schema 2 stores the enhanced connection identity/binding, system prompt and tool selection/schemas ([creation](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/store.rs#L200-L221)). Schema 1 requires these enhanced fields to be absent (tool schemas empty); its initial system message remains in the journal, so old metadata is not evidence of a verified endpoint or enhanced configuration ([validation](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/store.rs#L657-L666)). Current environment defaults apply to new sessions. `session.config.update` validates idle changes and applies them to the next task without rewriting history. Credentials and live clients remain host-owned; schema 2's saved [ConnectionIdentity](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/client/connections.rs#L16-L21) includes connection_id, API and normalized endpoint as historical routing identity. Contact/profile data stays host/GUI-owned; Runtime has no profile record. A saved custom model route must still be supported by its resolved endpoint.

A session records its wire protocol (`api`: `openai-responses` or `openai-chat-completions`). Legacy single-client startup and `/resume` use `SessionStore::open_for_api` to reject a protocol mismatch without a model request or history rewrite. A configured multi-connection service instead resolves the session's saved connection identity; it does not require every session to use the host's default client. Unknown legacy origin is not a verified binding, and explicit binding/upgrade follows the [connection contract](runtime-api.md#106-连接增强配置与显式升级候选实现事实). There is no silent protocol migration or fallback to another endpoint.

Session isolation covers conversation state and output directories. Two sessions using the same working directory still modify the same files. Switching sessions does not roll back the filesystem, create a worktree, or provide a sandbox.

A session is a context and history boundary, not an application-level contact or work item. Nagi's target model associates a continuing conversation and separate work sessions with the same contact; see [Nagi product requirements](../requirements/desktop.md). Shared beliefs, experiences, and relationship history remain outside this session implementation; session contents are not automatically mixed into one another's prompts.

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

The [terminal adapter](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/terminal.rs#L333-L391) permits listing, renaming, creating and opening another session while a task runs; opening another session does not cancel the old task. Sending remains subject to the same per-session/capacity admission rules, and the TUI preserves a rejected draft. While a job runs, Esc in the TUI (or `SessionCommand::Cancel { session_id, task_id }`) requests cancellation for the active session. Cancellation is cooperative: the Agent stops at await boundaries, persists already-streamed assistant partials in order, closes pending tool pairs as interrupted without replaying them, writes `TaskFinished { Interrupted }`, and emits exactly one terminal `TurnInterrupted`. Stale or mismatched session/task pairs do not cancel another task; a repeated matching cancel does not create a second terminal. Bash cancellation kills the whole process group, including descendants. Cancellation is not transactional: a partially started filesystem write from an in-flight tool may have finished or may remain half-written; inspect state before retrying. For an interrupted or unfinished turn, streamed public reasoning remains UI-only and is never persisted as a provider payload; normal completed turns keep their existing reasoning persistence. Only assistant text is made durable on cancellation (final messages always, plus the ordered streamed assistant partials). Closing the input connection drains accepted tasks before the worker exits; background title jobs are aborted when the manager stops.

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

`SessionCommand` is a typed core request, not a slash-command parser. Available requests are Send, List, Create, Open, Rename, and Cancel; Cancel carries both `session_id` and `task_id` ([definition](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/terminal.rs#L30-L48)). `SessionStore::create` returns an ID without selecting it; the manager's Create request combines creation and opening for the current application. Agent can still be called directly without session management or a TUI.

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

Remaining scope: durable queued follow-up/steering, session deletion/export/fork, old-directory migration, full-text history search (the picker only filters list metadata), cross-restart drafts, library packaging, and shared long-term identity. Cross-session execution already exists; it is not a same-session queue.

## Local command system

The command layer should parse local user intent and dispatch existing typed operations. It should not introduce a second session implementation or send local commands as ordinary model messages.

The implemented vocabulary is `/new`, `/sessions`, `/resume <id>`, `/rename <title>`, and `/help`. TUI picker buttons/keys and slash commands call the same session handlers. The command implementation documents completion, argument errors, literal-slash escaping, draft preservation, and Busy feedback. TUI Esc cancellation and `SessionCommand::Cancel { session_id, task_id }` are implemented and accepted (see Behavior). Future commands such as manual compact should be added only alongside their own supported core operations; model/compaction changes already have the idle `session.config.update` path.

Implemented local input routing: [Local command system](command-system.md).

## Machine host

The `--stdio` entry exposes the [runtime API](runtime-api.md) through `src/session/runtime.rs`. It starts without selecting or creating a session, retains loaded sessions by ID, and permits loading/viewing B while A executes. It reuses the same Agent builder, tool loop and journal reader as the terminal host.

Machine execution is per session: each session has at most one accepted/running task. Different sessions can overlap up to the Runtime capacity; a same-session send is rejected as `busy/session_busy`, and a send when global capacity is exhausted is `busy/capacity_exceeded`. Rejections do not allocate a task, write user input, or enter a hidden queue; callers must explicitly submit again after capacity is released. See [admission](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/runtime.rs#L2010-L2020), [concurrency/no-queue test](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/tests/session/concurrent.rs#L424-L460), and [busy send test](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/tests/session/service.rs#L509-L575).

`session.status` separates `loaded`, execution `state`, `active_task` and reported `status`; none alone is an input-durability receipt. `task.status {session_id,task_id}` separates `phase`, `status`, `cancel_requested` and any `persistence` warning; a recovered start without a matching finish remains unconfirmed, without replaying work ([status fields](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/runtime.rs#L110-L174), [recovery lookup](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/session/runtime.rs#L2705-L2740)). `task.cancel {session_id,task_id}` is directed to that observed task and returns `requested`, `already_requested`, or `not_running`; cancel is cooperative, is not proof of a terminal, and does not undo an already-executed tool. `session.config.update` changes model/compaction only while idle and applies the new configuration from the next task; contact/profile data remains host-owned.

`task.send` accepts optional caller `context:[{name,text}]` in addition to `message.text/images`; context-only messages are valid. The serialized context is limited to 1 MiB per user turn, and the complete stdio request frame remains limited to 8 MiB. Both typed and stdio paths validate before task-ID allocation, user-history write, or model request; invalid input is rejected rather than truncated. The context is user data, not system authority, and is retained in history; it does not create a Runtime profile field. Chat Completions rejects image input before task admission; input compatibility is not inferred from a model name.

## Next-task input and evidence (#31)

The PM decision at [comment 6386080367](https://github.com/HeimaoLST/Saki/issues/31#issuecomment-6386080367) accepts the finite MVP next-task path; the fuller [PR145 document](https://github.com/HeimaoLST/Saki/pull/145) at `acf0d9b7` is a content source, not a claim that a GUI flow is complete.

**Decision and application responsibility.** The application, not Runtime, persists each additional input revision and its immutable snapshot while a task is running. After the current task reaches its actual terminal, the application may explicitly submit the selected revision with the existing `task.send`/`context`; no enqueue mode, automatic retry, steering injection, or next-task scheduler is added. A `busy` rejection leaves the application's pending revision intact. If acceptance is unknown (lost response, timeout, disconnect), the application must reconcile before resubmitting; it must not automatically resend. Revision IDs and reconciliation bookkeeping are host metadata, not new fields on `task.send`. `request.id` is correlation only, not an idempotency key.

**Evidence boundaries.** The following observations are distinct and must not be collapsed:

| Observation | Safe meaning | Does not prove |
| --- | --- | --- |
| Application has persisted a revision | Host received/can display its own pending input | Runtime acceptance |
| `task.send` accepted response with task/message IDs | Runtime accepted the addressed submission; bind the IDs to the saved snapshot | Durable queue, task start, or model use |
| `task_started` | The execution boundary was recorded and the task began | The user input was already saved or sent to a model; [order](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/agent/mod.rs#L347-L353) is started, then add/save |
| Matching task/message boundary plus the full input in paginated `session.history` | The named input snapshot was recorded for that task | Exact current model request after compaction, provider receipt, understanding, or fulfillment |
| `model_request_started` (`purpose=generation`) | The execution loop is about to attempt generation | Upstream HTTP acceptance, a token, or per-source adoption |
| `task_finished` / `task.status` terminal | The Runtime execution completed, failed, or was interrupted | Every requested result was produced or every tool succeeded |

Use the service's paginated `session.history`, not the JSONL file. Locate the TaskStarted matching session/task/message IDs, then the complete user input before the next task boundary; compare text and ordered context with the saved immutable snapshot. Individual message records do not independently carry message_id, and an identical string elsewhere is not an association. Missing pages, legacy missing IDs or a persistence warning leave input evidence unconfirmed rather than permitting invented IDs or automatic resubmission. Cancellation targets the observed task; its request acknowledgment is not a terminal and does not withdraw recorded context or undo executed tools.

This finite MVP does not claim that a model understood or adopted a revision, that every requested result was fulfilled, that the GUI end-to-end path has passed, that a durable queue exists, that FIFO is implemented, or that steering is supported. FIFO remains a historical unimplemented proposal (PR65); #31's referenced FIFO/steering recommendation is not a prerequisite for this path. Context resources for compacted history (#62) and authoritative applied-edit results (#33) remain separate future work.

Deterministic boundaries already available for a consuming host include the busy/no-queue tests ([concurrent](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/tests/session/concurrent.rs#L424-L460), [service](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/tests/session/service.rs#L509-L575)), stdio caller-context forwarding and oversized rejection ([context tests](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/tests/session/context.rs#L468-L537)), and typed/stdio input limits in [message validation](https://github.com/HeimaoLST/Saki/blob/d0502695446e4826c34c1d9d8e841d0e10cdcd09/src/agent/message.rs#L77-L120). Additional consumer persistence/routing tests are host-owned; this document does not claim they ran.
