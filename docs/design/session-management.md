# Session management

Updated: 2026-10-24. Status: implemented session behavior, with the explicitly marked #31 consumption recommendation below. Local slash commands are implemented; see [Local command system](command-system.md). Current service behavior and delivery evidence are documented in [Runtime API](runtime-api.md).

## Behavior

SAKI supports multiple persistent conversations with **one accepted or running task per session**, within the host's configured Runtime capacity. A host can reuse a session ID for continuity or select different IDs for independent contexts. Both use the same Agent loop. The current service permits cross-session concurrency; terminal presentation policy is not a service-wide single-task restriction.

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

While a job runs, listing and renaming remain available. The service permits loading/viewing other sessions and rejects another send to the occupied session; frontend draft and navigation behavior is described in the terminal consumer section of [Runtime API](runtime-api.md). The TUI preserves a rejected draft. Esc in the TUI (or `SessionCommand::Cancel { session_id, task_id }`) requests cancellation for the identified task. Cancellation is cooperative: the Agent stops at await boundaries, persists already-streamed assistant partials in order, closes pending tool pairs as interrupted without replaying them, writes `TaskFinished { Interrupted }`, and exposes one Runtime terminal (`task_finished` with status `interrupted` over stdio). Stale, repeated, or other-session cancels are harmless no-ops. Bash cancellation kills the whole process group, including descendants. Cancellation is not transactional: a partially started filesystem write from an in-flight tool may have finished or may remain half-written; inspect state before retrying. For an interrupted or unfinished turn, streamed public reasoning remains UI-only and is never persisted as a provider payload; normal completed turns keep their existing reasoning persistence. Only assistant text is made durable on cancellation (final messages always, plus the ordered streamed assistant partials). Closing the input connection drains accepted tasks before the Runtime exits; background title jobs are aborted when the manager stops.

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

Remaining scope: persistent background scheduling, session deletion/export/fork, old-directory migration, full-text history search (the picker only filters list metadata), cross-restart drafts, library packaging, and shared long-term identity. Cross-session task concurrency is already implemented; it is distinct from durable scheduling or a same-session follow-up queue.

## Local command system

The command layer should parse local user intent and dispatch existing typed operations. It should not introduce a second session implementation or send local commands as ordinary model messages.

The implemented vocabulary is `/new`, `/sessions`, `/resume <id>`, `/rename <title>`, and `/help`. TUI picker buttons/keys and slash commands call the same session handlers. The command implementation documents completion, argument errors, literal-slash escaping, draft preservation, and Busy feedback. TUI Esc cancellation and `SessionCommand::Cancel` are implemented and accepted (see Behavior). Future commands such as model switching or manual compact should be added only alongside their own supported core operations.

Implemented local input routing: [Local command system](command-system.md).

## Machine host

The `--stdio` entry exposes the [runtime API](runtime-api.md) through `src/session/runtime.rs`. It starts without selecting or creating a session, retains loaded sessions by ID, and permits loading/viewing B while A executes. Different sessions can execute concurrently up to `runtime.capabilities.max_active_tasks`; the legacy environment-based startup defaults to one and accepts `SAKI_MAX_ACTIVE_TASKS`. A second send to the same occupied session returns `busy(reason=session_busy)`; exhausted Runtime capacity returns `busy(reason=capacity_exceeded)`. Neither rejection queues work. It reuses the same Agent builder, tool loop and journal reader as the terminal host. New TaskStarted records optionally include the caller-visible message_id; legacy records remain readable without invented IDs. Machine history pages include record offsets and Unix millisecond timestamps (0 when absent in old records).

## Follow-up submission and input evidence (#31)

Checked against `dev@fd58ec2500690d877a0406cdedf92aa2605bf133`. **Implementation facts** below describe that version. **Consumption recommendation**: a host can durably retain additional input while a task runs, then explicitly submit it in the next task using existing `task.send` and `context`. This MVP path needs no new Runtime protocol, FIFO queue, steering API, or execution framework. It does not implement mid-task adoption or certify that a model understood or fulfilled a requirement.

### Existing boundary and old proposal

The machine API from #43/#60, cross-session concurrency from #61, and caller context from #108 are already integrated at this baseline. They are available foundations, not pending dependencies. The [Runtime API](runtime-api.md) remains the authoritative method/configuration reference.

[PR #65 at 32debaf](https://github.com/HeimaoLST/Saki/pull/65) preserves the earlier explicit FIFO proposal: an opt-in enqueue mode, bounded pending tasks, targeted queued cancellation, and shutdown/restart rules. It remains historical, unimplemented design, not the MVP prerequisite. This section replaces its recommendation for the next-input use case; it neither changes that PR's branch nor declares its queue implemented or approved. Current `task.send` has no `enqueue` mode; the DTO rejects unknown fields. Steering remains a separate future decision supported by a concrete unmet need.

### What each observation proves

These observations are separate evidence, not a single monotonic success ladder: a task may fail or be cancelled after input is recorded, and input evidence can arrive after a terminal event. The host retains both submission history and task outcome.

| Observation | Safe interpretation | Does not prove |
| --- | --- | --- |
| Host durably saves an input revision | Received by the host; pending delivery | Runtime acceptance or a model request |
| Correlated `task.send` result with task_id/message_id | Runtime accepted this submission for the addressed session; bind those IDs to the saved immutable input snapshot | Durable input, task start, model understanding, or completion |
| Explicit send error such as busy/invalid_request | This attempt was rejected; retain pending input and show the reason | A queued task or automatic later delivery |
| Missing response, timeout, broken output, or application crash around send | Acceptance is unknown until reconciled | Rejection; it must not trigger automatic resubmission |
| Matching `task_started`, or active `task.status` phase=running | Runtime wrote the task-start boundary and entered task execution | That the user message/context was already saved or a model received it |
| Matching start boundary plus its complete user input in `session.history` | The named input snapshot was recorded for this task | Exact current model request after compaction, provider receipt, understanding, or fulfillment |
| `model_request_started` with purpose=generation | The execution loop is about to attempt generation | HTTP acceptance, a token produced, or per-source adoption; purpose=compaction is a different operation |
| `task_finished` / `task.status` terminal | This Runtime execution completed, failed, or was interrupted; keep any persistence warning | Every requested result was produced or every tool operation succeeded |

The ordering distinction is observable in [Agent::run_started_task](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/agent/mod.rs#L338): write TaskStarted, publish the started signal/event, add the UserMessage, save it, then enter the model loop. A failure saving that user message can therefore follow Started without any model request. [Generation dispatch](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/agent/mod.rs#L428) emits ModelRequestStarted before the client call. Neither event carries an authoritative per-source adoption receipt.

To verify input provenance, consume the service's paginated `session.history` records, not the JSONL file. Locate the TaskStarted matching session_id/task_id/message_id, then its user-message record before the next task boundary. Compare the saved text and ordered context parts with the immutable host snapshot; an identical string elsewhere in history is insufficient. TaskStarted has the association; individual message records do not independently carry message_id. If pagination, a missing record, persistence failure, or legacy missing IDs prevents exact association, retain an unconfirmed input state rather than guessing. Historical input remains historical input even if later compaction changes the model's working context.

### Minimal host flow

1. Persist each additional input's stable local ID, target session, revision and text before showing it as received. Keep a bounded, visible pending collection; saving a revision does not call the model. Separate interactive conversation from a long-running execution with different session IDs when independent model turns are needed. Configure sufficient task capacity and inspect capabilities; two sessions alone do not overcome a capacity of one. Session separation does not isolate a shared working directory or copy contexts between sessions.
2. Keep the active task unchanged. After its actual terminal is observed and reconciled, offer an explicit next execution using the selected pending revisions. This minimal flow requires no automatic retry or continuation scheduler. A status read is advisory: send can still reject due to a concurrent caller or terminal-delivery cleanup. A busy rejection leaves the revisions pending.
3. Before writing to the transport, persist a local submission attempt with its immutable text/context, target session and request correlation ID. Mark an unresolved attempt as delivery-unknown. On success, persist the returned task_id/message_id against that exact snapshot. Later edits create a new revision; they do not retroactively change the accepted attempt. Request IDs and context names are correlation/provenance data, not server idempotency keys.
4. Submit instructions in `message.text` and selected supporting material in the existing ordered `context:[{name,text}]`. The host may include its revision identifier in a context name for exact later comparison; Runtime treats it as user data. This does not add a product entity to Runtime. Named context remains in history and participates in token budgeting/compaction; it is not temporary system authority. Explicitly select revisions and explain replacements rather than appending all saved conversation repeatedly or assuming old requirements were removed.
5. Route responses and events by the saved session/task/message association. Acknowledge delivery only after real acceptance; label a verified historical match as input recorded for that task. Treat fulfillment as a separate assessment of outputs and remaining work. Duplicate terminal events must be idempotent; a late event for a previous task must not change a newer task or another target.

Existing wire shape, with a host-owned revision label rather than a new Runtime field:

```json
{"version":1,"id":"send-next-7","method":"task.send","params":{"session_id":"session-a","message":{"text":"Continue with the selected revision. Explain which requested results remain unfinished."},"context":[{"name":"input-revision-7","text":"Save the output as report.md and include the verification result."}]}}
```

`ContextInput::as_input_text` encodes each name/text pair in a caller_context JSON text part. [Input assembly](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/agent/conversation.rs#L385) stores text, ordered context, then images in one UserMessage. [Admission validation](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/agent/message.rs#L103) bounds serialized context at 1 MiB; the whole Runtime command has its separate size limit. Invalid/oversized input is rejected, not silently truncated. Preserving these bytes is an execution-input guarantee, not a semantic guarantee about the model.

### Cancellation, uncertainty and restart

`task.cancel(session_id,task_id)` returns requested/already_requested/not_running. It addresses an accepted task, not the host's pending input list. A request acknowledgment is not a terminal, and cancellation racing with completion may still observe completed. Do not start the follow-up merely because cancel returned. Keep the terminal outcome and existing filesystem side-effect uncertainty; stopping execution does not withdraw already-recorded context or undo tools. Pending revisions remain available for explicit later delivery.

If task IDs are known, query `task.status` and `session.history` after disconnect/restart. A durable start without a finish reports status=unknown, phase=recovery, persistence=unconfirmed; an absent ID reports phase=unknown, not rejected. [Recovery lookup](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/session/runtime.rs#L2705) never infers completion from assistant text. Input may have been recorded even when the final outcome is unknown.

If acceptance IDs were lost, uniquely correlated events/history may recover the association; otherwise keep delivery unknown and let the user decide after inspection. Similar text, current session status, or a reused request ID is not sufficient proof. The protocol has no submission-idempotency key or request-ID-to-task lookup. Persisting the local attempt reduces loss of intent but does not close the crash interval between Runtime acceptance and saving its reply.

Closing stdin/normal shutdown drains accepted work; it does not submit host-side pending revisions. Hard termination does not automatically replay old tasks. Opening/history inspection makes no model or tool call. Reconciliation must finish before any explicit new submission; repeated restart or reattachment must not silently create duplicate executions. A task error with persistence=unconfirmed requires inspecting/reloading the affected session before continuing, not retrying the same send on a timer.

### Actual gaps and scope of the recommendation

**No Runtime addition is required** for visible, persistent pending input plus explicit next-task delivery with honest received/accepted/input-recorded labels. The host must implement its own persistence and routing; this document does not claim that any current frontend already provides the complete flow. Existing #62 context retrieval and independent analysis are not prerequisites for this path.

The current API cannot certify that a particular source was present in a particular encoded model request, durably accept an idempotent pending submission across a crash, or change an already-running task's instructions. If a future consumer truly requires those guarantees, record the exact gap and observable evidence before proposing the smallest affected Runtime change. They are not silently approximated by Started, natural-language acknowledgment, or a local queue. A claim of live adoption remains unavailable; even a future request-level receipt would not prove understanding or fulfillment.

### Deterministic verification for a consuming host

Use controlled HTTP/tool gates, barriers/oneshots and explicit crash points. Existing source tests below establish reusable behavior boundaries; new host rows are acceptance requirements, not assertions that this document implemented or ran a new feature. A real Runtime process must receive the host's actual serialized input in the integration case; mocked DTOs alone are insufficient.

| Case | Required observation | Existing boundary to reuse / additional consumer evidence |
| --- | --- | --- |
| Save during execution | Hold task A; persist revision r2. A's request/history remains unchanged and no extra HTTP occurs. After A terminates and the user submits, exactly one new task contains r2; another session does not | [Concurrent admission](../../tests/session/concurrent.rs#L424); host persistence/routing plus captured HTTP body |
| Busy or invalid input | Same-session busy, capacity full and oversized context leave input pending; no accepted IDs, journal mutation or later automatic execution | [Busy/capacity](../../tests/session/concurrent.rs#L424), [context rejection](../../tests/session/context.rs#L513) |
| Accepted is not input recorded | Inject task-start persistence failure; then separately user-message persistence failure. Neither yields a valid input receipt; the latter may emit Started while making zero HTTP requests | [Start failure](../../tests/session/service.rs#L784), [user-input failure](../../tests/session/service.rs#L839); consumer label assertions |
| Exact source and revision | Captured model request contains the selected ordered context once. History lookup follows the matching TaskStarted across page boundaries; later edits, duplicate text elsewhere and another target cannot satisfy that comparison | [Context round trip](../../tests/session/context.rs#L194), [stdio forwarding](../../tests/session/context.rs#L468); host snapshot and paginated association assertions |
| Compaction and recovery | Original input provenance remains inspectable after compact/reopen; reopening adds zero requests and does not inject the source again. Do not label the retained original as an exact current request | [Compaction/restart](../../tests/session/context.rs#L541) |
| Lost acknowledgment / crash | Drop an acceptance reply after admission; execution can still complete once. Crash the host before saving IDs, restart with the pending attempt intact, and verify no automatic resend or false rejection | [Lost accepted reply](../../tests/session/service.rs#L1002), [unknown recovery](../../tests/session/concurrent.rs#L1048); consumer crash fixture |
| Cancel/finish/late-event race | Cancel only the intended task; wait for its true terminal. Preserve pending input; completion winning the race stays completed, and repeated/late events do not affect another task | [Directed cancellation](../../tests/session/concurrent.rs#L473), [completion race](../../tests/session/runtime.rs#L352), [terminal acknowledgment](../../tests/session/service.rs#L396) |
| Incomplete business result | A normal model completion or tool business error must not mark every requested result satisfied; surface missing evidence separately from execution status | [Tool failure versus task failure](../../tests/session/runtime.rs#L458); consumer result assessment fixture |

Delivery evidence names the Runtime SHA, consumer SHA, captured request/association, persistence boundaries, and remaining unknowns. An implementation can pass the next-input slice while live adoption remains unsupported; neither a documentation merge nor a model's “understood” response changes that scope.
