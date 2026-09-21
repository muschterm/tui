# First Go server and shell slice

Status: implementation in `apps/go`, 2026-09-19. The server, SQLite persistence and attachable client are real; agent activity, children, questions, approvals and terminal sessions are fixtures. This slice makes lifecycle and interaction reviewable without claiming working ACP, collaborative files, Git or embedded shells. It does not change the accepted product scope or settle later integration decisions.

## Run and preserve a view

From the repository root:

```sh
cd apps/go
make build
make check
make test
./bin/tui-go
```

Launch starts or attaches to the background server. The application home is `~/.tui-go`; set `TUI_GO_HOME` to an absolute temporary directory for isolated experiments. It contains `state.sqlite`, discovery, lock and server-log files, plus retained migration backups and per-incarnation shutdown outcomes when created. Exiting the TUI leaves the server running. Use:

```sh
./bin/tui-go server start
./bin/tui-go server status
./bin/tui-go --client desk
./bin/tui-go snapshot
./bin/tui-go probe
./bin/tui-go server stop
```

`--client desk` restores that named client's saved view, including drafts and geometry. The default client identity is unique so simultaneous launches navigate independently; use different explicit names for independent persistent views. View documents are stored through the server, separate from authoritative execution snapshots. `snapshot` prints diagnostic state JSON. `probe` reports environment/runtime information and does not establish terminal capability support. Restart preserves fixture state and requires explicit Resume by default (eligible Demo work may continue under the explicit General setting); reattaching to a still-running server only catches up.

## Prototype interaction

These bindings are first-slice choices for interactive review, not a cross-language compatibility promise.

| Key | Action |
| --- | --- |
| F2 / F3 / F5 | Toggle navigation / right host / bottom panel |
| F4 | Commands menu, including pane resizing |
| F6 | Move between principal focus areas |
| F7 / F8 | Maximize or restore right surface / change theme |
| Tab / Shift+Tab | Traverse visible controls |
| Enter | Send from composer; activate focused control |
| Shift+Enter / Ctrl+J | Insert a composer newline |
| Ctrl+S | Alternate submit binding |
| Delete in a tab menu row | Close that surface |
| Ctrl+Q / Ctrl+C | Detach TUI |
| Ctrl+Z | Suspend |
| Alt+Left / Alt+Right | Resize right panel |
| Alt+Up / Alt+Down | Resize bottom panel |
| Ctrl+Shift+C | Copy selected text through OSC clipboard |

Pointer paths include visible controls, divider dragging, wheel scrolling and text selection. Terminal bracketed paste is supported; native Ctrl+V is disabled because its asynchronous widget path can split a grapheme at the input limit. Complex emoji pointer positioning and shortcut remapping remain prototype limitations. Clipboard and enhanced key delivery depend on the host terminal; their presence in the prototype is not evidence of every terminal/SSH/tmux path. The command menu exposes left-pane resizing as well. The shell supplies opened-surface tabs, singleton non-terminal surfaces, repeatable fixture terminals, attention, inspectors and fixed composer-adjacent activity. Usage remains unavailable, and rich graphics are not claimed.

Controls use Nerd Font Codicons by default; configure a patched font in your terminal. `TUI_GO_ICONS=ascii ./bin/tui-go` selects the explicit plain-symbol fallback. Pane glyphs reflect visible open/closed state. Tabs contain an icon and name; hovering a tab or focusing its icon reveals the close action. Only the icon slot closes it. The overflow control appears only when some tabs are hidden, with one row per surface and Delete as a keyboard close path.

The composer starts at two editable rows inside its outline and grows with wrapped text and newlines to eight rows where space permits. Short layouts reduce the cap while keeping fixed controls visible. Additional text scrolls and remains intact. Scrollbars appear for overflowing transcript, inspector, request text, composer/answer, navigation, bottom output and menus. Click the track to page or drag the thumb; one-cell tracks have no drag travel, so use wheel/keyboard. Reading older input lines does not move the insertion cursor; typing returns to it. See [controls validation and captures](../research/go-controls-2026-09-19.md).

Input routing measures controls without painting. The runtime combines pending
visual updates at up to 60 frames per second while processing every input and
state transition immediately. Scroll offsets stay within current content bounds.
See the [wheel-input regression and measurements](../research/go-scroll-performance-2026-09-19.md).

## Phone-sized terminals

The minimum is now **40×22**, including the reported **47×22** iPhone size.
Below 60 columns, tap the top-left hamburger or press F2 to select Conversation,
Projects & threads, Surfaces or Terminal. The picker also exposes Commands;
F3/F5 directly select Surfaces/Terminal. The prompt, settings/usage overflow and
Send/Stop remain available. The other fixed activity controls live in Conversation;
the bell returns to pending questions. Selecting a column preserves drafts, tabs
and terminal sessions. A wider resize restores the saved pane arrangement.
See the [small-screen behavior](layout.md#single-column-layouts-on-small-screens).

Reopen `./apps/go/bin/tui-go` from the repository root to use the rebuilt client;
this presentation change requires no server restart. The 22-row minimum remains.
Actual iPhone terminal, SSH and mobile keyboard behavior still need user testing;
our Go render and OS-PTY checks do not establish device compatibility.
[Validation and captures](../research/go-small-screen-2026-09-20.md) record the
40 passing PTY checks and remaining device review.

## Application protocol v1

### Fixture queue steering

Each queue row and the full queue menu expose **Steer**, with pointer and
Tab/Enter activation. It adds the saved queued message to the same active Demo
turn, preserving captured settings/attachments and the ordinary draft. At narrow
widths Steer stays labelled while Edit/Remove use icons with descriptive help.
Unavailable activation shows a five-second notice without moving focus; pending
or uncertain delivery remains visible until a receipt or explicit Retry resolves
it. Editing the same queued item requires Save or Cancel before Steer.

The new optional `fixture-steering` capability gates `queue.steer`. It uses
`TargetID` for the queued prompt, `Revision` for the queue revision and
`ExpectedTurnID` for the observed `Thread.TurnID`. Accepted synthetic delivery
returns `fixture-delivered`; `Activity.TurnID` and `Activity.Prompt` retain its
turn and complete captured input. Queue removal, history and receipt commit
together. A stale queue/turn, finished/closed/interrupted turn, unavailable
capability or settings mismatch leaves the prompt queued. A waiting fixture turn
accepts steering without resolving its pending questions or approvals.

Restarting the rebuilt server upgrades legacy fixture turn identities and
advertises this capability; attaching the new TUI to an older running server
does not upgrade it. To enable steering on an existing home, detach the old TUI,
run `./apps/go/bin/tui-go server stop` from the repository root, then launch
`./apps/go/bin/tui-go`. Server restart preserves state and requires explicit
Resume of unfinished work. Do not restart merely to reconcile a lost receipt.

This is a fixture capability, not verified Codex/Claude/ACP delivery. The
[shared steering design](activity.md#steering-a-queued-message) applies to all
three reference apps, while [implementation evidence](../research/go-steering-2026-09-20.md)
records the bounded Go behavior and remaining integration work.

### Snapshot and command transport

The authoritative wire shapes are [protocol types](../../apps/go/internal/protocol/types.go); handlers are in [server.go](../../apps/go/internal/server/server.go), transitions in [commands.go](../../apps/go/internal/server/commands.go), and transaction boundaries in [storage.go](../../apps/go/internal/storage/storage.go). This is an application protocol, distinct from ACP v1. Some nested fields currently use Go's exported field names in JSON; consumers should follow the actual types rather than infer naming from the top-level snapshot tags.

One process holds the selected home's nonblocking file lock and listens on an ephemeral IPv4 loopback port. Discovery records the URL, process/instance identity and bearer token. Requests require `Authorization: Bearer …` and `X-TUI-Protocol: 1`; browser-origin requests are rejected. Current client discovery validates loopback, home and version. Remote SSH forwarding remains an unimplemented client-configuration path, not a tested consequence of using HTTP.

| Endpoint | Behavior |
| --- | --- |
| `GET /v1/snapshot` | Complete current state with revision and server-instance identity |
| `GET /v1/events` | WebSocket sends an initial complete snapshot and later complete snapshots |
| `POST /v1/command` | Validate and apply a typed command; return durable receipt |
| `GET` / `PUT /v1/views/{id}` | Read/write that client identity's view document, with thread ownership projection with expected revision |
| `POST /v1/stop` | Begin explicit shutdown |

Subscription and initial snapshot capture occur under the same state mutex. Revisions advance on accepted transitions; clients reject mismatched instance/version and nonadvancing stream revisions. An eight-snapshot subscriber buffer bounds lag; overflow closes the stream for resynchronization. This is snapshot replacement, not an ordered event-history API. A receipt's `accepted` state means durable fixture acceptance, not successful external execution.

View responses carry `revision` alongside `data`. PUT supplies the previously read revision (zero creates a new view); SQL compare-and-swap rejects stale writes with `stale_view` rather than overwriting a newer draft. Client identities are bounded to 128 bytes and view payloads to 128 KiB. Rejected writes require preserving local content and reconciling the newer view; this is not collaborative merging of two clients sharing a name.

Commands carry identity, kind and applicable thread/target/revision. Identical retries return the original receipt; reuse with changed content returns `identity_conflict`. State plus command/receipt are committed in one SQLite transaction before publication. Queue mutations check the queue revision; request answers check pending status and request revision. This prevents duplicate fixture transitions, without promising exactly-once external effects. Thread deletion purges scoped command payloads while retaining minimal creation/deletion retry fingerprints; other receipts remain indefinitely. General retention and broader migration/recovery tooling remain future work.

SQLite schema version 1 is recorded in `PRAGMA user_version`. Opening a newer schema fails before schema or journal-setting mutations. An existing unversioned database is backed up with SQLite `VACUUM INTO` to a private `recovery-before-v1-*.sqlite` file; the backup and containing directory are synced before transactional migration. Legacy view documents receive revision 1. Fresh databases initialize directly. Tests verify the backup includes live WAL contents and retains the old schema, and that rejecting a future schema leaves its database bytes unchanged. These checks do not establish a general disaster-recovery workflow.

Shutdown first persists interrupted fixture state, drains HTTP handling and closes SQLite. It then atomically publishes and syncs `shutdown-<instance>.json` with the combined outcome before discovery is removed. `server stop` requires a matching successful record; missing, mismatched or failed outcomes report uncertainty or failure rather than treating a vanished endpoint as success. This verifies fixture shutdown, not cancellation of real agents or shell processes.

Prompt submission captures the supplied settings and attachment content in its queued item. Queue text/settings edits preserve existing attachment captures. Current settings admit only the fixture model and permissions, low/medium/high effort, unavailable context and standard speed. Synthetic dispatch copies captured settings into its fixture effective state; it does not enforce a real model or permission mode. Request resolution reports fixture confirmation, not provider receipt or asynchronous-answer integration.

Current bounds: 768 KiB command bodies; 16,384-byte prompts; 32 queued prompts per thread; eight attachments with at most 64 KiB content each; 4 MiB command-result snapshots; 128 KiB view bodies; 4,096-byte individual answers; 64 retained terminal fixtures; 32 children per thread, with older full child detail archived into activity; 128 activity items per thread, with a truncation notice. These are implementation limits, not approved product budgets. Commands/receipts, named views, migration backups and shutdown outcome records do not yet have retention policies; prolonged fixture use can grow storage.

## Dependencies and implementation assumptions

[go.mod](../../apps/go/go.mod) pins Go 1.27.1, Bubble Tea 2.0.9, Bubbles 2.2.1, Lip Gloss 2.0.6, modernc SQLite 1.59.0 and coder/websocket 1.8.15, with checked-in module sums. Tea/Bubbles/Lip Gloss supply input/rendering, composer widgets and styling but add terminal compatibility work. These maintained releases were verified through the public Go module proxy and their downloaded versioned source on 2026-09-19. The existing transitive `uniseg` 0.4.7 dependency is now direct for grapheme-safe editing; `x/ansi` 0.11.8 provides cell-aware clipping/sanitization, and `x/sys` 0.48.0 supplies Unix locks and terminal queries. Their cost is a pinned Unicode/terminal implementation surface that still needs compatibility checks. Pure-Go SQLite avoids a cgo deployment dependency at the cost of a larger dependency/binary footprint. WebSocket support supplies transport framing and connection handling; application authentication, versioning and catch-up remain this repository's responsibility. The POSIX lock/process implementation currently targets macOS/Linux; Windows remains outstanding.

The fixture runner is intentionally bounded to synthetic work. Workspace file context capture is limited to the source-backed UTF-8 attachment path described below. There are no workspace file writes, Git mutations, real agent jobs or interactive terminal subprocesses. Application persistence is real. A displayed terminal's controller field is fixture state, not a verified PTY controller contract. Similarly, populated question/approval/child views exercise presentation and state transitions without proving adapter feature parity.

## Evidence and next slice

The [second interaction review](../research/go-activity-review-2026-09-19.md)
records the grouped activity strip, finite demo turns, composer controls and
empty-host maximize changes with their tests and capture provenance. Reopening
loads the new UI; an already running server must be restarted to load the finite
demo lifecycle, after which unfinished work still requires explicit Resume.

[Feasibility evidence](../research/go-feasibility-2026-09-19.md) records isolated Yjs, merge, Go VT, Bun/Ink and Bun PTY probes, including exact versions and limits. The [validation report](../research/go-slice-validation-2026-09-19.md) records passed build/race checks, cross-build scope, UI regression tests, render captures and practical PTY checks. Required terminal/SSH/tmux coverage and live provider checks remain pending.

Backend validation on 2026-09-19: `GOCACHE=/tmp/tui-go-build go test -race ./internal/server ./internal/storage ./internal/lifecycle` passed with loopback-listener permission. Checks cover authentication, lock contention, detached fixture progress, initial WebSocket catch-up, deduplication and stale revisions, restart Resume gating, view CAS and migration backup, and confirmed graceful stop. Storage/lifecycle tests were rerun after adding backup-directory syncing and passed. This evidence is limited to the tested Go fixture implementation.

The next implementation slice is one pinned ACP adapter with initialization, capability negotiation, a real prompt, confirmed settings, activity streaming, cancellation and reconnect without resubmission; see the [validation report](../research/go-slice-validation-2026-09-19.md#next-implementation-slice). Use interactive review to refine geometry, focus and controls alongside that slice. Resolve native shared-document convergence/own-edit undo and autosave reconciliation before expanding the editor; validate pinned ACP capabilities, continued-work answers and full available child history before claiming integrations; connect server-owned PTYs to a bounded emulator before calling terminal surfaces functional. Rust and Bun reference applications remain required later work.

A final view-save failure exports a private JSON recovery file under the application home and reports its path; automatic import is deferred. Pending commands retain their identity for explicit Retry. Queued edits preserve the original composer draft; use Commands → Rebase queued edit after conflict before explicitly saving against a newer queue revision.


## Activity and composer refinement

The current UI contract uses grouped Agents and Plan summaries in a shared tinted band, blue working circles, solid green all-completed circles, and explicit hover/focus dismissal only for completed summaries. Individual child inspection remains in Agents. Ordinary message roles use alignment and tint without repeated author headers. Thinking/Waiting follows reported execution state; Stop names the existing interruption action, including while waiting. Send, Stop, paperclip and the unknown context gauge follow the [footer contract](layout.md#controls-and-composer-refinement-2026-09-19). A visible empty right chooser supports maximize; a hidden host has no maximize control.

Fixture footer display names are Demo / Reference / Medium / Simulated; these do not change captured settings or claim provider support. Context telemetry is absent. Real provider menus, effective settings enforcement and measured usage remain integration work.

Summary dismissal is frontend-local per thread. The fixture protocol has no run ID: keys use the latest accepted user activity identity and group/plan content, and observed unfinished/new active work resets dismissal. Unrelated ticks and streaming text do not reopen a dismissed summary. An identical run with no new accepted prompt identity or observed lifecycle transition cannot be distinguished; a real integration must supply stable run identity. This is a limitation, not a claim of durable provider-run tracking. Validation outcomes belong in the implementation's research report.

## Notification and question polish — 2026-09-20

The bell uses a pending-count badge and packs against visible pane controls.
Completed Plan/Agents summaries swap their leading dot for X on hover/focus;
only the icon dismisses, while the label opens history. Agents shows “Agents N”
with the total child count, including completed children; state and working count
remain in hover help. Closed (formerly Recents) uses a section heading.

The total-count correction was validated on 2026-09-20 with
`GOCACHE=/tmp/tui-go-build make check` (formatting, vet, race tests and build).
The 13-child regression covers 12 completed/one working, all completed, hover,
both themes and widths 48/80/120. Deterministic View captures were reviewed for
working/completed states and both themes at 48×22. These captures exercise the
renderer; this correction has not been checked in a native terminal session.

Questions use a bounded outlined card, single-row square-filled top tabs, fixed-slot filled
Back/Next arrows, vertical radio/checkbox choices,
optional Other text, and open-ended text. Single-choice selection advances;
checkbox selection does not. Every submission remains explicit. Controls and
scrolling follow the [question contract](questions.md#question-card-refinement--2026-09-20).
The maximum card is 12 rows, reduced in short windows; answer text grows to three
rows. Structured answers preserve selected choices separately from free text.
Legacy question and command JSON remains readable, including persisted retry
identities. Old server processes must be restarted to accept the new structured
answer field. Existing saved requests retain their original question shapes;
only a fresh application home seeds the new mixed-question fixture.

Drafts for prior request revisions remain in named-client storage; automatic
import into changed questions is deliberately avoided. An interactive recovery
picker for these older drafts is still outstanding. No real provider support or
new terminal capability is implied by this presentation work.

[Notification/question validation and captures](../research/go-question-review-2026-09-20.md) record the tests, native PTY checks and remaining limits.

## Projects and thread organization

The navigation now offers All projects or a searchable name/path selector, an
adjacent folder-plus Add project control and a square-and-pencil New thread icon
in its header. Add accepts an existing
folder on the server, deduplicates its canonical path and leaves its files intact.
A project starts empty; New thread opens a local per-project draft. Explicitly
choose Reference model after the preset Demo Agent; first valid Send creates the
thread and accepts its initial prompt atomically. The selected filter only changes that client's navigation.

Thread titles and metadata share a padded card background and rounded outline, with
a blank row between threads. Hover/focus highlights the entire card; metadata and
padding select for reading, while the status and menu buttons keep their own actions.
See [card and compose-icon validation](../research/go-thread-cards-2026-09-20.md).

Open rows show pulsing blue working, yellow/orange attention, red error or green
finished/idle circles. Only finished, closable rows expose a hover/focus checkmark
to Close, and a vertical ellipsis offers Close or
Reopen plus Delete permanently. Closed replaces Recents; selecting a closed row
reads it without reopening, and its hover trash opens a Cancel-first permanent-delete confirmation.
Close currently requires no active/queued work, pending requests or active children.
Reopen restores the draft/surfaces and does not Resume interrupted execution.
F4 exposes Projects, Add project, New thread, Closed threads and selected-thread
options; Tab/Enter reaches visible controls.

Delete atomically removes thread-owned live data and saved per-thread views.
Stale clients cannot restore deleted data. A writer rebases only a verified server
deletion projection of its last acknowledged view, preserving unrelated draft
changes and retaining CAS rejection for genuine same-name client conflicts.
Minimal fingerprint receipts prevent retry resurrection. Workspace folders, files
and Git state are untouched; historical backups are outside logical deletion.
See [projects](projects.md), [thread lifecycle](threads.md), [ADR 0007](../adr/0007-thread-deletion-and-view-projection.md)
and [the navigation review](../research/go-navigation-review-2026-09-20.md).

Rebuild and explicitly restart an older server to load these new capabilities:

```sh
cd apps/go
make build
./bin/tui-go server stop
./bin/tui-go --client desk
```

Use the same `TUI_GO_HOME` for stop and launch. Restart preserves unfinished work
and still requires explicit Resume. The existing default home is not migrated
to a different location, and no remote Git operations are performed.

Question Submit preserves the original Answers wire format for older untyped
requests. Typed questions keep structured responses. Validation, submission and
recovery feedback appear inside the card rather than only in the hover-obscured
status bar. See [Submit compatibility review](../research/go-submit-review-2026-09-20.md).

## Color detection over SSH

Rebuild and relaunch only the TUI for this change; the running server can remain:

```sh
cd apps/go
make build
./bin/tui-go --client desk
```

Keep your usual client name and `TUI_GO_HOME`. Try the normal launch without the
temporary `COLORTERM=truecolor` prefix. Startup detection remains in place; a
low-color client requests terminal identity once, then queries RGB/Tc for known
Ghostty, kitty and iTerm2 responders. A positive capability reply upgrades colors.
Missing, unknown or negative replies keep a neutral 256/16-color palette. Both
themes remain usable. The Usage inspector reports the profile and whether true
color came from startup detection or a confirmed capability reply.

The client honors every nonempty `NO_COLOR` value and avoids changing terminal
default colors in fallback/monochrome modes. No SSH environment, tmux configuration,
server state, or saved terminal capability is modified. tmux relies on the pinned
library's existing detection; probes are not wrapped to bypass it. Alacritty with
generic `TERM` and missing `COLORTERM` may retain the fallback. For a path you have
verified supports true color, the one-launch `COLORTERM=truecolor` workaround
remains available. Exact Omarchy/SSH/tmux combinations still require retesting.

See [color compatibility evidence and reply-fragment limits](../research/go-colors-2026-09-20.md).

Composer spacing now uses shared two-cell gutters for prompt text and footer
controls. Settings align left and the usage/attachment/Stop/Send group aligns
right on the same row. At narrow widths, usage first compacts to its clickable
gauge, then controls progressively move into a vertical ellipsis menu instead
of wrapping. The settings ellipsis stays directly after the left-hand fields.
Usage has a separate ellipsis at the far left of the right-hand group; it opens
context occupancy/capacity/percentage, billing, limits and cost (currently all
unavailable/unknown). Send and active Stop retain priority. The typing area has
a complete outline, with the settings/actions row below it. Hidden settings and
recovery actions remain accessible; differing running settings or recovery
actions tint the settings ellipsis amber. Tab/Enter or pointer
activation opens the menu; resize preserves its selection and the prompt draft.
Close icons remain centered within their existing hit areas. Rebuild/relaunch
only the TUI. See the [spacing review](../research/go-spacing-2026-09-20.md) and
[responsive footer review](../research/go-footer-overflow-2026-09-20.md), followed by
[grouped overflow and prompt outline](../research/go-composer-groups-2026-09-20.md).

Question navigation refinement: filled `◀`/`▶` icons (`<`/`>` in plain mode)
replace Back/Next text. Their slots stay reserved when either end is unavailable.
Question buttons have padding, individual backgrounds and a bold active state;
limited-color and plain-symbol modes add brackets in reserved end cells. The
current question remains visible in a contiguous group as navigation
advances; overflow alone exposes the all-questions menu. Answer checkmarks reserve
space. Submit, Options, Requests and approval choices use matching compact buttons.
See [navigation validation and captures](../research/go-question-tabs-2026-09-20.md).

The accepted [component rule](components.md) supersedes the earlier two-style
corner experiments: prompt, thread, request and dialog containers use rounded
outlines and stable interior backgrounds; question/surface tabs and compact
actions use single-row square fills. Square outlines remain available. Rest and
hover use distinct neutral shades; selection uses accent plus bold, and keyboard
focus independently underlines the label. Status colors remain semantic.
Question content remains scrollable inside its 12-row maximum. Tab edges select;
the separate icon closes. Incremental painting bounds retained ANSI styling.
The [earlier control experiments](../research/go-prompt-corners-2026-09-20.md)
remain historical evidence and do not validate the new component states.
Fresh [component validation](../research/go-components-2026-09-20.md) records
the shared resolver, render captures, race tests and 56 local PTY checks.

The queue now groups its count, previews and Steer/Edit/Remove/reorder actions in a
rounded container. It shows up to two preview rows, or one below 28 terminal rows;
the header reports hidden items and opens the full queue. Actions stay inside
the shared inset and align with their message. Boundary arrows retain their
slots while disabled. The outline adds one row at normal heights, two in the
short layout; request content and prompt growth account for that space.

Answered-question history is an accepted [shared design requirement](questions.md#answered-questions-in-conversation-history) awaiting implementation. The Go slice currently saves accepted answers on resolved requests, but removing the pending form does not yet create a readable Q&A transcript card. No runtime support is claimed by the documentation update.

## Thread quick-action placement — 2026-09-20

Close and trash now occupy the fixed slot directly left of the vertical ellipsis.
The leading circle keeps its status color and selects/reopens the thread. Hover
or keyboard focus reveals the eligible action without shifting the title. The
phone navigation column uses the same placement. Existing inactive-only Close
and permanent-Delete confirmation behavior remain unchanged.

Validation on 2026-09-20: `GOCACHE=/tmp/tui-go-build make check` passed formatting,
vet, race tests and build. The action-slot regressions cover narrow navigation,
47×22 phone navigation, plain glyphs, keyboard activation and unchanged deletion
confirmation. `python3 scripts/pty_navigation.py --artifacts /private/tmp/tui-thread-actions-20260920`
passed all 18 isolated OS-PTY checks using the relocated icons. Deterministic
Close/Trash hover captures were visually reviewed; native phone/SSH hover behavior
was not tested in this pass. Reopen the rebuilt TUI; no server restart is needed.

## Sidebar/settings refinement (2026-09-20)

The header now combines local thread-title search with Project filter, Add project
and New thread icons. The filter displays a stable project badge, and its picker
has a distinct settings gear per row (Tab also reaches gears). Closed is pinned
above app settings, defaults collapsed for new clients and scrolls independently
when expanded. App settings uses General, Appearance, Keybindings and About with
`Settings / <category>`. A project gear enters only that named project with
Project, General and Keybindings and `Settings / <category> / <project name>`.
There is no All projects settings scope or project Appearance; F8 cannot change
theme while project settings is visible. The selected form replaces all main
workspace panes and the composer. Back at bottom-left restores them. Compact
settings uses hamburger/F2 to switch categories and form.

Project name, icon, color and inherited/overridden workspace default persist on the
server. Project contains only Name/Icon/Remove; color is nested inside Icon.
Project General shows the effective workspace default and Use app default reset,
using revisioned `project.update`. Project Keybindings reports inherited app
bindings and unavailable overrides. Confirmed Remove project deletes its threads
but keeps disk files; active work blocks it and stale confirmations are rejected.
App General stores this server's
workspace default and off-by-default restart continuation. Worktree preferences
are saved but creation explicitly fails until provisioning exists. Recovery is
verified only for eligible Demo work; unknown providers, manually stopped work
and pending requests remain gated. Theme stays client-local.

For this settings scope/presentation correction, rebuild with `make build` and
relaunch the TUI; no server restart is needed when its settings capabilities are
already present. To use the backend settings with an already-running
older server, detach the TUI, run `./bin/tui-go server stop`, then reopen
`./bin/tui-go --client desk`. Stop cancels owned fixture work; the default restart
requires Resume. No existing application home was stopped during validation.

See [settings behavior](settings.md), [project navigation](projects.md), and the
[validation report](../research/go-sidebar-settings-2026-09-20.md).
The latest [project scope checks and captures](../research/go-project-settings-scope-2026-09-20.md)
record the separation of project identity, project overrides and app settings.

## Draft-first creation and Closed composer

New thread retains one draft per project in the attached client's saved view, including prompt, settings and attachments. It creates no authoritative thread until `thread.start` accepts the initial prompt and explicit agent/settings together. Invalid input leaves the draft intact. `thread.create` remains a legacy API and is not used by this UI. The fixture presets Demo Agent and offers only Reference model; selecting it fills supported defaults. Real Codex and Claude integrations remain implementation step 4.

During active work, including waiting, composer settings show the effective running values read-only and Send captures those values. The prior idle preference remains stored and becomes editable again when idle. Required options gate Send. Queued items retain their own accepted captures.

Closed thread selection opens history and its composer without changing lifecycle. Above the composer, “This thread is closed · Send a message to reopen” has a far-right Reopen action and a two-line compact layout. Explicit Reopen sends nothing. `prompt.reopen-send` compares lifecycle revision, reopens and accepts a valid captured prompt in one transaction; rejected or stale sends cannot reopen it.

The checkout/branch row below the controls reads server metadata through `GET /v1/workspace` with either `project_id` or `thread_id`, on selection or explicit refresh. Non-Git, detached, fixture and unavailable states are distinct. It performs no Git mutation. The new capabilities are `thread-start`, `closed-thread-send` and `workspace-info`; an already-running older server needs a user-controlled upgrade/restart to advertise them. Relaunching only the TUI does not upgrade that server. No automatic restart of user-owned work is authorized by this refinement.

[Validation for this refinement](../research/go-draft-composer-2026-09-20.md) records Go checks, 96 isolated OS-PTY checks and renderer captures separately from actual terminal/device verification. Earlier navigation captures do not establish the new behavior.

## Project destinations, folders and file mentions

The targeted project/path slice adds an always-shown searchable New thread
project picker, including Add project. Selecting or registering an existing folder
opens/restores that client's per-project draft; the first valid Send still creates
the authoritative thread. Add project uses directory completion and never creates
a folder. App General's revisioned **Project starting folder** defaults to the
server user's home and controls the picker's initial location.

Inline `@` completion browses the draft project or thread checkout. Up/Down
selects; Enter/Tab browses a directory or selects a file; Escape dismisses without
Send. Selection inserts a relative mention and removable attachment. The server
captures regular UTF-8 files at Send, bounded to 64 KiB each and eight attachments,
with root-escape rejection. Accepted prompts retain their bytes through retries,
queues and recovery. See [projects](projects.md), [settings](settings.md#project-starting-folder)
and [file mentions](activity.md#inline-file-mentions).

The added capabilities are `path-completion` and `workspace-file-context`.
Rebuild and relaunch the client, and explicitly restart an older backend when
ready to load them. From `apps/go`, run `make build`, detach the old client, then
`./bin/tui-go server stop` and `./bin/tui-go --client desk` (using your own client
name). Stopping cancels owned fixture work; restart follows the saved recovery
preference and defaults to explicit Resume. The running user server is not
restarted by this implementation task. Older servers show upgrade guidance;
client relaunch alone cannot enable these server capabilities.

Validation and remaining limits are tracked in the
[project-path checkpoint](../implementation/project-path-checkpoint.md). This
slice adds no provider, collaborative-editor or embedded-terminal integration.
