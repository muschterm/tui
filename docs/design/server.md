# Background application server

Status: accepted architecture and lifecycle; remaining policies are identified below. Recorded **2026-09-19**. This specifies intended behavior. The [first Go slice](go-slice.md) implements and tests a bounded server/client subset with synthetic execution; real agent, editor, Git and PTY integrations remain unverified.

## Applications and server identity

There will be three complete reference applications, each with a native server and TUI: Go first, then Rust/Ratatui and React/Ink+Bun. They share a common client/server contract. Its versioning, messages and compatibility tests remain implementation work. This decision does not require identical internals or database schemas.

The default is one server per user/application, managing multiple projects and threads. Server identity includes the resolved application home and user. A home directory such as `~/.myapp/`, overridden through an application-specific variable such as `MYAPP_HOME`, illustrates the accepted pattern. These are not selected names: each consuming application owns its identity and prefix, with no required shared `.myapp` directory. Different configured homes represent separate state and server identities. Discovery must not accidentally connect one home to another. See [Storage and recovery](storage.md).

Clients connect through authenticated local HTTP/WebSocket on loopback. Remote access uses SSH forwarding; direct network exposure is not enabled by default. Authentication and discovery must respect the selected home. The [Go slice](go-slice.md#application-protocol-v1) selects bearer credentials, ephemeral loopback ports and application protocol v1; cross-language interoperability and remote attachment remain unverified. A future Web UI may use the same server, but browser implementation and broader exposure are future scope.

## Command and shutdown semantics

| Command | Accepted behavior |
| --- | --- |
| `my-app` | Ensure the applicable server is running, then connect the TUI |
| `my-app server start` | Start the background server without opening a TUI |
| `my-app server status` | Report whether the applicable server is running |
| `my-app server stop` | Gracefully cancel active work, save state, stop owned processes, and report completion |

`my-app` is a placeholder executable name. Quitting the TUI disconnects that client and leaves the server and agents running. Reconnecting later must catch up with work performed while disconnected.

Concurrent startup attempts must establish one execution owner for the same identity. A PID or endpoint file alone does not establish liveness: startup and status must distinguish the intended reachable server from stale discovery information. The coordination mechanism, status categories, exit codes and remediation remain unselected.

Explicit shutdown cancels active work gracefully, records outcomes and queues, saves preserved state, and terminates owned processes. It does not roll back modified workspace files. Completion must reflect actual shutdown progress; incomplete cancellation or surviving owned processes cannot silently become success. Cancellation timeouts, escalation, partial-failure reporting and when to reject new submissions still need specification. Startup at login and operating-system service installation are not implied.

## Execution and presentation ownership

The server owns agent lifecycles, threads, upstream sessions, turns, prompt queues, checkout scheduling, pending requests and outcomes. These cannot depend on a connected TUI's event loop or stdin. Disconnecting must not silently cancel, duplicate, approve or redirect work.

Each thread has one chosen agent. ACP v1 remains the upstream boundary through native agents or selected adapters. The HTTP/WebSocket application contract is separate from ACP. Provider concepts belong in the ADE layer rather than becoming requirements of every reusable shell.

Multiple TUIs and a future web frontend may attach simultaneously. Drafts, editor buffers and remembered responsive layouts are preserved application state. Active focus, hover, selection, scroll and inspection position remain client-local; remembering preferences does not synchronize another client's current focus. Clients submit durable changes through the server rather than writing its database.

Preserve each thread's opened-surface associations and prompt draft so a frontend can restore that thread's view and reading position when switching back. Files/Git follow its checkout. Existing terminal associations retain their process identities and working directories across navigation; selecting a thread never starts, restarts or redirects a shell. Restoring a frontend view does not change another client's navigation or terminal controller.

Simultaneous live file editing, prompt autosave and labelled peer cursors are accepted. Per-client undo must preserve other clients' work. Presence communicates current participation, not durable execution authority. The collaboration algorithm remains unselected. A durable document revision and its on-disk saved status must be distinguishable: preserving a buffer does not imply that its contents reached the workspace file.

Clean external changes merge automatically. Overlapping edits, deletion or replacement preserve relevant versions, pause autosave and require review. After restart, recovered buffers must be revalidated against current disk contents before saving; restoring state is not blanket permission to overwrite external changes.

Each interactive terminal has one explicit input/resize controller at a time. Other clients can inspect it without injecting input or changing the PTY's dimensions. The server owns the PTY and subprocess throughout; a frontend disconnect does not terminate them. Controller transfer and disconnect rules must prevent stale clients from continuing to send input after ownership changes. Exact transfer controls are prototype details.

Terminal surfaces can occupy the right panel or the center-bottom panel, and multiple independent terminals are supported. Terminal is the only repeatable right-host surface type; other types reuse their existing tab in that frontend's host. New terminal instances have separate process/session identities, retained output and controller ownership. Switching tabs, hiding or maximizing a panel must not create or restart a shell. Restoring a view attaches to its existing terminal where available; server restart does not imply a surviving PTY.

Explicit right-tab close or the bottom terminal's own close action sends a server command to end that terminal session and stop its PTY/shell work. Panel toggles only hide/show and preserve sessions. Close affects the shared session: observers receive its ended state and stale input/resize actions must fail. Keep close acceptance distinct from confirmed shutdown, reconcile retries by terminal identity, and report incomplete termination. A newly opened terminal is a new session; a stale restored tab must not recreate a closed one. Close authorization, confirmation and escalation details remain implementation work. See [close versus hide](layout.md#terminal-close-and-panel-hide).

## Reconnect correctness

A returning client must obtain coherent state and retained history: completed and active work, queued prompts, unresolved requests, failures and available inspection details. It must never restart a turn to regenerate missing history.

The handoff between catch-up and live updates must have no gaps. If a tool completes while history loads, its completion must eventually appear without duplicate cards. Updates must identify their operation; delivery retries must not repeat execution. Snapshots plus sequenced events and cursors are a possible mechanism; full event sourcing is not required. Cursor expiry, server restart and unavailable artifacts need defined responses.

Lost acknowledgments require equivalent care. A prompt, approval, interrupt or Git action accepted just before disconnection must be reconciled without blind resubmission. Optimistic client display is not proof of server acceptance. Cross-process effects may have uncertain outcomes; surface and reconcile uncertainty rather than promise universal exactly-once execution. Durable state and artifact readiness are addressed in [storage](storage.md).

Attaching to a live server simply catches up with continuing work. After a server restart, restore history and queues, mark unfinished runs interrupted, and require explicit **Resume** before saved execution continues. Restoration must not automatically dispatch queued work or replay side effects.

## Scheduling and unattended requests

One active root writing thread or job holds the checkout writer lease; competing writers queue. Its delegated subagents share that lease rather than queueing behind their parent. Existing checkouts remain the default, with optional worktrees for independent work. Client disconnection cannot release writer ownership. External writers remain outside this scheduler, so stale-change detection is necessary.

Prompts submitted during active work enter a visible per-thread queue; interruption is explicit. Catch-up preserves queue order and distinguishes accepted waiting prompts from drafts and failed submissions. Checkout waiting, active execution and requests for user input must remain distinguishable. Users can edit, remove and reorder queued prompts until execution starts, through controls in that visible queue. Scheduling fairness and advancement after interruption remain open.

Queue changes are server commands addressed to a prompt identity and current revision. Coordinate them with dispatch: an edit or removal accepted before dispatch must take effect, while an action that loses the race to execution must report that the prompt has started and preserve any unsent edit. Reconcile competing clients and lost acknowledgments without duplicate queue entries. Removing a queued prompt does not interrupt the active turn, and reordering affects that thread's prompt queue without bypassing checkout writer eligibility. Editing or reordering text preserves unchanged attachment captures. Restored queues can be managed while awaiting Resume; changing them must not automatically resume execution.

For agent-assisted merge/rebase conflict resolution, the agent edits and runs relevant checks, then **stops for review before staging resolutions or continuing the operation**. This applies while no client is attached. Pending approvals/questions remain explicit; disconnection does not authorize automatic approval or invented answers. Resolved or withdrawn requests must invalidate stale controls.

Each queued prompt also owns the [settings captured at Send](thread-configuration.md#settings-captured-per-prompt). Composer changes apply to future submissions. Only an explicit settings edit to that queue item changes its recorded selection. Before dispatch, reconcile the stored selection with supported and confirmed upstream configuration; missing or rejected options require resolution instead of substitution. Reordering or reconnecting must not change the configuration a queued prompt will request.

## Question requests

Pending [question requests](questions.md) belong to the server, including their originating run/connection, revision, blocking or asynchronous mode, accepted answer and delivery outcome. Answer commands are separate from queued prompts. Reconcile competing submissions and uncertain delivery before resolving a request; persisting an answer does not prove the agent received it. Keep per-client answer drafts distinct from the shared accepted response. Reattachment catches up; restart requires explicit Resume and upstream request revalidation before old controls can become actionable. Track provider withdrawal, expiration or automatic resolution without inventing a user answer.

## Remaining decisions and acceptance

Open implementation choices include the collaboration algorithm, retention and storage budgets, detailed shutdown failure handling, and request-conflict reconciliation mechanics between clients. Multiple attached clients, one terminal input/resize controller, live collaboration, external-change review and explicit Resume after restart are accepted.

The [Go backend tests](../../apps/go/internal/server/server_test.go) exercise same-home lock contention, authentication, detached fixture progress, initial WebSocket catch-up, restart awaiting Resume, competing fixture answers and confirmed graceful stop. These checks do not establish real agent/process lifecycle behavior. Remaining scenarios include subprocess startup races, configured-home isolation, unattended provider approvals, real child-agent activity and stop-for-review conflicts. Exercise lost-acknowledgment reconciliation, shutdown with modified files, interrupted runs awaiting Resume, simultaneous editing, per-client undo, external-change review and recovered-buffer revalidation. Verify isolated focus and rejection of stale terminal-controller input/resize.
