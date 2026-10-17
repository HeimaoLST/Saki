# Session management

Updated: 2026-10-15. Status: implemented. Local slash commands are implemented; see [Local command system](command-system.md).

## Behavior

SAKI supports multiple persistent conversations with **one active job at a time**. A personal-assistant host can always select the same session ID; a development host can select different IDs for different jobs. Both use the same Agent loop.

Plain startup always creates a fresh session; it never restores stored history implicitly. Existing history is restored only through an explicit `SAKI_SESSION=<id>`, `/resume <id>`, or the session picker. `SAKI_SESSION=new` explicitly requests the same fresh-session behavior as plain startup. Opening a session reads history; it does not call the model, resume an unfinished job, or execute tools. Opening also takes an exclusive journal lock; because `fork(2)` copies the descriptor table, a child that has not `exec`'d yet can briefly keep a released lock alive, so lock acquisition retries for a bounded window before reporting the session as locked.

A session preserves its model route, original system message, working directory, and compaction configuration. Current environment defaults apply to new sessions. This prevents opening an old session under an unrelated model's context budget. API credentials and the current endpoint remain host configuration and are never written into session metadata. A saved custom model route must still be supported by that endpoint.

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

## Replay and compact

One journal pass returns two outputs:

1. **Model context**: append original protocol items; on a compact record, replace the working prefix with the original system message and summary, retaining `keep_from..` unchanged.
2. **Display history**: retain all original message batches, compact markers and execution records. The frontend renders these without interpreting a compact summary as a new assistant reply.

The restore API installs the rebuilt working context and opens the journal for append. `persisted_items` starts at the rebuilt context length, preventing restored items from being written again. The summary flag is preserved so later compact operations use the same legal boundary rules.

Usage baselines are not restored. Conversation uses its existing estimate until the next response provides usage. Restoring does not invent measured token counts. Streaming deltas remain display-only: only completed model output is saved, so unfinished text shown before a crash may disappear after reopening.

Existing version-1 message/compact journals can be replayed by Conversation. Old session directories without metadata appear as unavailable in the picker and are not silently upgraded. Unsupported schema versions, invalid complete records, and broken tool/checkpoint structure are reported with a path and line number.

An incomplete JSON record at EOF without a newline is a torn tail. Under the write lock, restore truncates to the last complete record before appending. A valid final record without a newline is retained and separated before append. Complete JSON with an unsupported schema is never treated as an incomplete write.

## Interrupted execution

A task-start record without its matching task finish is Interrupted. Older histories without task boundaries have Unknown completion status. Opening only reports that state; it does not execute anything or fabricate a model answer.

When the user next sends a message, Conversation records the previous task's interruption and closes any unmatched tool calls with explicit interrupted results. These state that the tool may already have run and its external side effects must be inspected before retrying. Recorded results are retained. No earlier call is automatically replayed.

Each completed tool result is saved before the next tool begins. A crash during a later tool therefore does not discard earlier results from the same batch. There is still an unavoidable interval between a tool causing an external effect and saving its result; this version deliberately treats that outcome as uncertain rather than promising exactly-once execution.

If a target session fails to load, the existing active Agent and its frontend draft remain available. If journal persistence fails, the task reports failure rather than a reliable saved success.

## Validation and limits

Behavior tests are grouped in [session tests](../../tests/session/mod.rs), [conversation tests](../../tests/conversation/mod.rs), and [TUI session tests](../../tests/tui/sessions.rs). They cover A/B/A context and model separation, session working directories, save/reopen, Busy handling, failed-open preservation, file locking, invalid versions, torn-tail recovery, interrupted tools, draft/attachment/view preservation, session ID filtering, and picker search (metadata matching, empty state, key precedence, grapheme-safe editing, and selection stability across rename/title updates). Existing tests now exercise production restore after repeated compact checkpoints and compare native requests containing images and reasoning before/after restore.

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

## Proposed follow-up submission contract (#31)

Proposal updated: 2026-10-17. **Not implemented or approved.** This section selects and details a candidate for [#31](https://github.com/HeimaoLST/Saki/issues/31); it does not change the Busy behavior described above. The machine boundary and initial task association are proposed in [#43 / PR #59](https://github.com/HeimaoLST/Saki/pull/59).

### Recommendation and scope

Choose candidate B: explicit FIFO follow-ups first. Candidate A also needs a safe point for steering, interaction with pending model/tool work, and a distinct user contract; it should not be implemented by injecting text into an in-flight HTTP request. Candidate C preserves Busy but does not provide accepted follow-ups. Steering remains a separate decision after actual use; it is not silently approximated by a follow-up queue.

Existing `SessionCommand::Send`, TUI draft handling, and the initial machine API keep rejection while busy. An explicit `task.send` mode `enqueue` opts into a per-session queue; omitted mode or `reject_if_busy` retains rejection. A frontend must not display a queued acceptance before the runtime acknowledges it. A TUI shortcut or queue view requires its own frontend delivery, not a runtime-only claim that the current UI supports queues.

No second Agent loop, journal parser, compaction path, or global current-session queue is introduced. Each accepted message uses the shared task/message association from the initial machine API. The queue is part of session execution management and can later be used with independent concurrent sessions from #61.

### Observable behavior

| Operation / state | Proposed result |
| --- | --- |
| Send while idle | Accept task/message IDs, then explicitly start it; acceptance precedes start events |
| `reject_if_busy` while active or already queued | Return busy; do not allocate an accepted task or mutate history |
| `enqueue` while active or already queued | Accept into the tail; return task/message IDs and `queued:true`; do not start or add the input to model context yet |
| Queue capacity reached | Return `queue_full` before acceptance; preserve the input for the caller |
| Current task terminates | Publish its unique terminal event and persist its execution boundary before starting the FIFO head |
| Targeted cancellation of active task | Preserve cooperative cancellation; acknowledgment is not a terminal event; start the next accepted follow-up only after actual cleanup |
| Targeted cancellation of queued task | Remove only that entry, acknowledge `cancelled_queued`, and emit one interrupted terminal with `started:false`; no model/tool request |
| Wrong/stale task cancellation | Return not_running; never cancel a different active or queued task |
| Query session status | Include active task and ordered pending task/message IDs; a displayed queue is runtime state, not a local guess |
| EOF / explicit shutdown | Stop accepting; drain accepted tasks in order before releasing locks and exiting |
| Hard process termination | Queued, unstarted entries are not durable and are not automatically replayed after restart |

The proposed default is at most 16 pending messages per session, excluding its active task; the host may select a smaller finite capacity. This is a bounded admission limit, not a scheduling framework. Global multi-session concurrency and resource limits remain #61's contract. Capacity is validated before accepting IDs so a rejected input never looks queued.

FIFO ordering is determined by runtime acceptance order within a session, not request IDs or client timestamps. Requests rejected for invalid input/session/configuration do not occupy a slot. The first queued task is removed only when it starts or is explicitly cancelled, and exactly one task writes a session's conversation at a time.

A normal model/task failure or explicit interruption does not implicitly cancel later independent follow-ups: the next accepted task can continue using the existing reconciliation path. A session that cannot persist or restore execution safely stops accepting/starting work; its accepted pending tasks receive explicit failed outcomes rather than being silently dropped or run without reliable storage. Such never-started outcomes are distinguishable with `started:false` and do not fabricate `task_started` journal records.

Queued input is not appended to the model context or ordinary journal message history until that task begins. It cannot alter the current model request or be compacted before its own turn. Acceptance and pending status are process-local, not a durable queue/exactly-once promise. After an uncertain disconnect or restart, callers inspect saved task/history state and do not automatically resubmit missing acknowledgments or vanished pending tasks.

Metadata-only rename can remain available. Operations that change a session's model, profile, compaction, or release its execution state return busy while any task is active or pending. This prevents an accepted message from silently starting with a later configuration; #32 must preserve this rule or explicitly introduce a different, reviewed binding contract. Viewing another session neither changes the queue nor cancels its active task.

### Deterministic acceptance and implementation split

The implementation issue is created after TUI/GUI/PM acknowledgment of this candidate, and depends on #43's agreed task boundary and #60's initial machine API. It covers session admission/queue state, targeted cancellation, status projection and module tests, plus this document's implementation facts. Frontend queue interactions stay in frontend-owned follow-ups; multiple session overlap stays #61. This decision is not considered implementation completion merely because this document merges.

Required behavior tests use controlled model/tool gates or oneshot coordination, never sleeps:

1. Hold A at an await boundary; accept B and C, reject default-mode D, and prove B/C have not made model/tool requests or entered conversation history. After releasing A, start B then C exactly once in acceptance order.
2. Cancel queued B while A runs: B never starts, A remains active, C keeps its position, and B gets one never-started interrupted terminal. Cancel A: C starts only after A's cleanup and terminal; stale cancellation cannot interrupt C.
3. Release A with a normal failure, then run B using preserved context/reconciliation; test storage-unavailable handling separately so pending acceptance is never silently lost in a live process.
4. Exercise capacity, invalid input, multiple client request IDs, and simultaneous submission/cancel/finish using controlled schedules. Rejection must not create misleading accepted IDs or journal records.
5. Close input while A runs with B queued: drain both, preserve per-task IDs and terminal ordering, and release locks. Hard-kill/restart reports interrupted durable work but does not rerun B; caller resubmission is explicit.
6. Queues for two sessions do not share ordering or cancellation state. A/B overlap is independently verified under #61; this test does not claim cross-session concurrency from a FIFO-only implementation.
7. Existing Busy/TUI draft behavior and cancellation regressions remain green; model switching/release while pending follow the explicit conflict rule. Record final implementation SHA, applicable gates and independent review.

Review requirements: runtime checks state transitions and persistence; TUI/GUI check accepted-versus-started, pending projection and targeted cancellation; PM checks follow-up-first versus steering scope. ACKs and independent review must cite the actual candidate. Existing user authorization covers compatible implementation and dev integration after these gates; an actual breaking change or product-scope dispute is reported separately.
