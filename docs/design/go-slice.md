# First Go server and shell slice

Status: implementation in `apps/go`, updated 2026-09-22. The server, SQLite persistence and attachable client are real; a first [ACP slice](#acp-agents--2026-09-22) connects built-in Go Claude/Codex bridges alongside the fixture runner. Live HTTP checks are recorded separately from terminal validation. Children, general questions and terminal sessions remain fixtures; collaborative files, Git workflows and embedded shells remain incomplete. This does not change the accepted product scope or settle later integration decisions.

The [latest UI/bridge fixes](../research/ui-bugs-2026-09-22.md) add native
permission selectors, Codex question delivery, system clipboard copying,
actionable Send errors and App Settings / Agents defaults. This supersedes older
Codex-form limitations below; native answer receipts remain unavailable.
[Claude effort](../research/claude-effort-2026-09-23.md) was added on 2026-09-23:
the bridge offers only catalogue-reported levels per model, applies them through
`apply_flag_settings` and accepts a change only after `get_settings` reports it.
Model rows keep Claude's own names, and neither pinned resolved IDs nor legacy
entries the runtime already lists are repeated.

The [built-in Go bridge checkpoint](../implementation/go-adapter-checkpoint.md)
records the replacement of external adapters with Go implementations around
installed official CLIs. ACP and the app-owned question contract remain intact.
General native/fallback question delivery and confirmed Answered history remain
work to do; historical adapter evidence does not establish bridge parity.

## Run and preserve a view

From the repository root:

```sh
cd apps/go
make build
make check
make test
./bin/tui-go
```

`make check` runs gofmt, `go vet`, staticcheck, race tests and the build; `make lint`,
`make vuln` and `make pty` are available separately (see
[the command line](#command-line-interface--2026-09-22)).

Launch starts or attaches to the background server. The application home is `~/.tui-go`; set `TUI_GO_HOME` to an absolute temporary directory for isolated experiments. It contains `state.sqlite`, discovery, lock and server-log files, plus retained migration backups and per-incarnation shutdown outcomes when created. Exiting the TUI leaves the server running. Use:

```sh
./bin/tui-go --help
./bin/tui-go server start
./bin/tui-go server status
./bin/tui-go --client desk
./bin/tui-go snapshot
./bin/tui-go probe
./bin/tui-go version
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
| Ctrl+S | Alternate submit binding; inside a question card it submits that answer, never the prompt |
| Enter / Shift+Enter / Esc in a question answer | Next question or validated Submit / newline / back to the question's controls |
| 1–9 on a question option, tab or header | Select or toggle that choice |
| Delete in a tab menu row | Close that surface |
| Ctrl+Q / Ctrl+C without a selection | Detach TUI |
| Ctrl+Z | Suspend |
| Alt+Left / Alt+Right | Resize right panel |
| Alt+Up / Alt+Down | Resize bottom panel |
| Ctrl+C / Ctrl+Shift+C | Copy selected text to the local system clipboard; SSH uses OSC 52 with unconfirmed terminal acceptance |
| Ctrl+V / Ctrl+Shift+V / forwarded Super+V / Shift+Insert | Paste into the focused prompt through the guarded local clipboard reader; SSH/herdr use the outer terminal's Paste |

Pointer paths include visible controls, divider dragging, wheel scrolling and text selection. Terminal bracketed paste is supported. Forwarded paste shortcuts use the same guarded reader as context-menu Paste; the widget's separate asynchronous paste command remains disabled to preserve grapheme handling and stale-target checks. Herdr panes use OSC 52 for Copy even without SSH environment markers, allowing herdr to route the clipboard to its client; host-local Paste is unavailable there because the viewer can be on another machine. Complex emoji pointer positioning and shortcut remapping remain prototype limitations. Clipboard and enhanced key delivery depend on the host terminal; their presence in the prototype is not evidence of every terminal/SSH/tmux path. The command menu exposes left-pane resizing as well. The shell supplies opened-surface tabs, singleton non-terminal surfaces, repeatable fixture terminals, attention, inspectors and fixed composer-adjacent activity. Fixture usage remains unavailable; ACP context usage appears only when reported. Rich graphics are not claimed.

Controls use Nerd Font Codicons by default; configure a patched font in your terminal. Settings › Appearance › Symbols switches this client to the ASCII fallback and back without restarting; `TUI_GO_ICONS=ascii ./bin/tui-go` is the environment default for a client that has not saved a choice (2026-09-22). Pane glyphs reflect visible open/closed state. Tabs contain an icon and name; hovering a tab or focusing its icon reveals the close action. Only the icon slot closes it. The overflow control appears only when some tabs are hidden, with one row per surface and Delete as a keyboard close path.

The composer starts at two editable rows inside its outline, below one blank tinted padding row, with the placeholder "Ask to do anything", and grows with wrapped text and newlines to eight rows where space permits. Short layouts reduce the cap while keeping fixed controls visible. Additional text scrolls and remains intact. Scrollbars appear for overflowing transcript, inspector, request text, composer/answer, navigation, bottom output and menus. Click the track to page or drag the thumb; one-cell tracks have no drag travel, so use wheel/keyboard. Reading older input lines does not move the insertion cursor; typing returns to it. See [controls validation and captures](../research/go-controls-2026-09-19.md).

The transcript follows its end while the reader is there: new activity keeps the last line visible. Scrolling up holds the reading position and paints a centered compact control on the transcript's last row, `Jump to bottom` or `N new messages` counting user/agent messages received since scrolling away (tool and thought rows are not counted); activating it, End, or scrolling back to the end re-pins. Saved views record this as optional `Pinned`/`SeenActivity` fields; older views pin only when their offset is at the end, and their existing messages count as read. Because blocks were two rows apart before 2026-09-23, an older saved middle offset lands one row further per earlier block once; no migration is applied. Consecutive transcript blocks are separated by one blank row (2026-09-23). User messages render as right-aligned tinted bubbles ending at the prompt outline's right extent, at most 80% of that width (short messages hug their text), with wrapped lines left-aligned inside; agent replies and reported thinking stay left-aligned and unboxed but wrap at the mirrored 80% cap, and tool rows stay left-aligned (2026-09-23). T3 Code itself caps only the user bubble; its assistant text fills a narrow centered column, so the mirrored cap is this project's cell translation of that shared right edge. Answered question cards are drawn like the user bubble: right-aligned at that extent, hugging their content up to the same cap, since T3 end-aligns every user-originated row (2026-09-23).

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

Shutdown closes HTTP admission, releases unfinished input and drains accepted handlers alongside owned-agent shutdown. It then persists interrupted state, closes SQLite, and atomically publishes and syncs `shutdown-<instance>.json` with the combined outcome before discovery is removed. Commands and view writes share the shutdown gate; failed HTTP drain closes remaining sockets and reports its stage. `server stop` requires a matching successful record; missing, mismatched or failed outcomes report uncertainty or failure rather than treating a vanished endpoint as success. See the [HTTP shutdown regression](../research/go-shutdown-2026-09-22.md) and the separate ACP lifecycle evidence below.

Prompt submission captures the supplied settings and attachment content in its queued item. Queue text/settings edits preserve existing attachment captures. Fixture settings admit only the fixture model and permissions, low/medium/high effort, unavailable context and standard speed. Synthetic dispatch copies captured settings into its fixture effective state; it does not enforce a real model or permission mode. Request resolution reports fixture confirmation, not provider receipt or asynchronous-answer integration.

Current bounds: 768 KiB command bodies; 16,384-byte prompts; 32 queued prompts per thread; eight attachments with at most 64 KiB content each; 4 MiB command-result snapshots; 128 KiB view bodies; 4,096-byte individual answers; 64 retained terminal fixtures; 32 children per thread, with older full child detail archived into activity; 128 activity items per thread, with a truncation notice. These are implementation limits, not approved product budgets. Commands/receipts, named views, migration backups and shutdown outcome records do not yet have retention policies; prolonged fixture use can grow storage.

## Dependencies and implementation assumptions

[go.mod](../../apps/go/go.mod) pins Go 1.27.1, Bubble Tea 2.0.9, Bubbles 2.2.1, Lip Gloss 2.0.6, modernc SQLite 1.59.0 and coder/websocket 1.8.15, with checked-in module sums. Tea/Bubbles/Lip Gloss supply input/rendering, composer widgets and styling but add terminal compatibility work. These maintained releases were verified through the public Go module proxy and their downloaded versioned source on 2026-09-19. The existing transitive `uniseg` 0.4.7 dependency is now direct for grapheme-safe editing; `x/ansi` 0.11.8 provides cell-aware clipping/sanitization, and `x/sys` 0.48.0 supplies Unix locks and terminal queries. Their cost is a pinned Unicode/terminal implementation surface that still needs compatibility checks. Pure-Go SQLite avoids a cgo deployment dependency at the cost of a larger dependency/binary footprint. WebSocket support supplies transport framing and connection handling; application authentication, versioning and catch-up remain this repository's responsibility. The POSIX lock/process implementation currently targets macOS/Linux; Windows remains outstanding. cobra 1.10.2 (with pflag 1.0.9 and the Windows-only mousetrap 1.1.0) supplies subcommand parsing, per-command help and shell completion for the command line; staticcheck 0.8.1 and govulncheck 1.8.0 are pinned as go.mod `tool` dependencies for `make lint` and `make vuln`, so they are reproducible without entering the binary. See [ADR 0012](../adr/0012-go-cli-framework.md).

The fixture runner is intentionally bounded to synthetic work. Workspace file context capture is limited to the source-backed UTF-8 attachment path described below. The fixture runner performs no workspace file writes, Git mutations, real agent jobs or interactive terminal subprocesses; selected ACP agents can perform their own direct operations. Application persistence is real. A displayed terminal's controller field is fixture state, not a verified PTY controller contract. Similarly, populated question/approval/child views exercise presentation and state transitions without proving adapter feature parity.

## Evidence and next slice

The [second interaction review](../research/go-activity-review-2026-09-19.md)
records the grouped activity strip, finite demo turns, composer controls and
empty-host maximize changes with their tests and capture provenance. Reopening
loads the new UI; an already running server must be restarted to load the finite
demo lifecycle, after which unfinished work still requires explicit Resume.

[Feasibility evidence](../research/go-feasibility-2026-09-19.md) records isolated Yjs, merge, Go VT, Bun/Ink and Bun PTY probes, including exact versions and limits. The [validation report](../research/go-slice-validation-2026-09-19.md) records passed build/race checks, cross-build scope, UI regression tests, render captures and practical PTY checks. Required terminal/SSH/tmux coverage remains incomplete; the dated ACP section below records the later live-provider evidence.

Backend validation on 2026-09-19: `GOCACHE=/tmp/tui-go-build go test -race ./internal/server ./internal/storage ./internal/lifecycle` passed with loopback-listener permission. Checks cover authentication, lock contention, detached fixture progress, initial WebSocket catch-up, deduplication and stale revisions, restart Resume gating, view CAS and migration backup, and confirmed graceful stop. Storage/lifecycle tests were rerun after adding backup-directory syncing and passed. This evidence is limited to the tested Go fixture implementation.

The ACP work proposed in the [original validation report](../research/go-slice-validation-2026-09-19.md#next-implementation-slice) now has a first implementation, described below. Use interactive review to refine geometry, focus and controls alongside that slice. Resolve native shared-document convergence/own-edit undo and autosave reconciliation before expanding the editor; validate pinned ACP capabilities, continued-work answers and full available child history before claiming integrations; connect server-owned PTYs to a bounded emulator before calling terminal surfaces functional. Rust and Bun reference applications remain required later work.

A final view-save failure exports a private JSON recovery file under the application home and reports its path; automatic import is deferred. Pending commands retain their identity for explicit Retry. Queued edits preserve the original composer draft; use Commands → Rebase queued edit after conflict before explicitly saving against a newer queue revision.


## Activity and composer refinement

The current UI contract uses grouped Agents and Plan summaries in a shared tinted band, blue working circles, solid green all-completed circles, and explicit hover/focus dismissal only for completed summaries. Individual child inspection remains in Agents. Ordinary message roles use alignment and tint without repeated author headers. Thinking/Waiting follows reported execution state; Stop names the existing interruption action, including while waiting. Send, Stop, paperclip and the unknown context gauge follow the [footer contract](layout.md#controls-and-composer-refinement-2026-09-19). A visible empty right chooser supports maximize; a hidden host has no maximize control.

Fixture footer display names are Demo / Reference / Medium / Simulated; these do not change captured settings or claim provider support. Fixture context telemetry is absent. ACP menus, acknowledged effective settings and reported context usage are described below.

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
The maximum card is 13 rows with its interior header (12 before 2026-09-23),
reduced in short windows; answer text grows to three rows. Structured answers preserve selected choices separately from free text.
Legacy question and command JSON remains readable, including persisted retry
identities. Old server processes must be restarted to accept the new structured
answer field. Existing saved requests retain their original question shapes;
only a fresh application home seeds the new mixed-question fixture.

Drafts for prior question schemas remain in named-client storage while their
request is pending; automatic import into changed questions is deliberately
avoided. A revision-only bump keeps the draft (2026-09-23). An interactive recovery
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
a complete outline, with one padding row above it and the settings/actions row below it. Hidden settings and
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
focus independently paints a mark in the cell before the control (2026-09-22,
replacing the underline; see below). Status colors remain semantic.
Question content remains scrollable inside its 13-row maximum (12 before the
[2026-09-23 card refinement](questions.md#question-card-refinement--2026-09-23)). Tab edges select;
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

## Closed row refinement — 2026-09-21

Closed cards are three rows (border, title, border) instead of four; the state
and project line is omitted and stays in the row's help text. The Closed
heading, closed titles, finished/working circles and the trailing icon use a
dimmed ink (`#67738f` dark, `#8790a7` light, 242/244 in 256-color, bright black
in ANSI); selection, hover, focus, red error and yellow attention feedback are
unchanged. The trailing slot reveals Reopen only on card hover or keyboard focus, like
trash (`md-arrow_u_left_top` U+F17B3, the turning-left
arrow closest to T3's lucide `Undo2` row action; `<` in plain mode)
instead of the vertical ellipsis and sends the existing `thread-reopen` action.
Trash keeps the reserved slot to its left. Glyph appearance in a real Nerd Font
terminal has not been visually reviewed here.

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

New thread retains one draft per project in the attached client's saved view, including prompt, settings and attachments. It creates no authoritative thread until `thread.start` accepts the initial prompt and explicit agent/settings together. Invalid input leaves the draft intact. `thread.create` remains a legacy API and is not used by this UI. The fixture presets Demo Agent and offers only Reference model; selecting it fills supported defaults. The ACP slice below begins the Codex and Claude integration work in step 4.

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

## Code review hardening — 2026-09-21

A three-part review of the backend, TUI state/input and rendering/hit testing
found and fixed 24 confirmed defects, each with a regression test. User-visible
changes: request menus and answer typing stay bound to the request they were
opened for and close when it changes; thread switches are refused during a
queued edit; a failed pre-send view save reports “Not sent” instead of leaving
a pending command; below the minimum size the Commands menu offers only Detach,
Suspend and Theme, and paste is ignored behind menus; opening a surface that
cannot fit presents it full width without storing a maximize preference, with
the maximize control absent while that presentation is forced; pane resizing is
clamped to the effective layout; selection is dropped when its basis changes.
Dispatched prompts keep one copy of their capture (`Activity.Prompt`), with a
summary in `Detail`. Rebuild and relaunch the TUI; restart an older server when
ready to load the backend fixes (restart also compacts duplicated legacy
details). See the [review record](../research/go-code-review-2026-09-21.md) for
findings, evidence, the repaired PTY harnesses and what remains open.

## Icon control feedback and header glyphs — 2026-09-21

Glyph-only controls now render through `iconButton`: the glyph is centered in
its former slot, but the hit rectangle is only the glyph's cells plus one
trailing cell (`hit.Slot` records the slot for layout checks). Hover and keyboard focus set bold and lift
muted ink to text; there is no hover fill. Measured in the installed
JetBrainsMono Nerd Font, icon outlines are byte-identical between the Regular
and Bold faces, so the weight change is expected to be invisible there and the
ink lift carries the feedback: accent inks blend halfway toward the text color
(`lift`), which the user confirmed after bold alone showed nothing on blue icons.
Since 2026-09-22 focus paints the mark in the padding cell before the glyph
instead of underlining; only a slot with no leading padding (the thread status
circle, the activity summary circle) keeps the underline. That same font is the
wide (non-Mono) variant: icons advance one cell but draw up to 1.56 cells wide
from the cell's left edge; a glyph-only target missed that spill in the user's
terminal, so the trailing cell is part of every icon target. The sidebar header now uses Font Awesome glyphs
(`fa-folder_o`, `fa-folder_plus`, `fa-pen_to_square`): the folder pair shares an
identical 923×808 box and the pen square is 916×916 with the same center, close
to the earlier scale. A Material Design trio tried first aligned but read as
smaller (668 tall). The previous `cod-new_folder` was 20% taller than
`cod-folder`, which read as misalignment. Measured in Maple Mono NF v7.9
(release `MapleMono-NF-unhinted.zip`): every glyph used here is present with the
same 600 advance and identical outline boxes as JetBrainsMono Nerd Font, and its
Bold face also carries identical icon outlines, so the same conclusions apply.
`fa-folder_plus` (U+EEC7) requires Nerd Fonts ≥ 3.2.1 (March 2024); the other
glyphs predate it. Visual review in a real terminal
is still pending for both changes.

## Top bar composition — 2026-09-21

`renderChrome` now lays out: left toggle, bold application title (`tui-go`,
key `app-title`, action `thread-create` with the project filter as its value,
so no filter opens the project chooser), the breadcrumb (`breadcrumb-project`
hit over the project icon and name with the no-fill icon hover, then `  /  `
and the thread title in text ink), the attention bell, and the pane controls.
The bell's right edge is the center pane's right edge whenever the right host
is visible and not maximized; the controls are right-aligned to the terminal
edge, which is inside the host's span because its minimum width (24) exceeds
the 19-cell control group. Maximize/restore use `fa-up_right_and_down_left_from_center`
and `fa-down_left_and_up_right_to_center` (Nerd Fonts ≥ 3.2.1), the diagonal
arrows of T3's `Maximize2`/`Minimize2`. The breadcrumb is left-justified one cell into the
center pane (or directly after the toggle when navigation is hidden, which also
hides the title) and truncates the title first while the project keeps at least
a third of the space. The top row takes each pane's background and continues
the dividers, whose glyph is drawn on row 0 and whose drag hit area includes
that row (`Geometry.DividerAt` folds the top bar row onto the divider). The bell sits two slot cells left of the first control so its
glyph pitch matches the six-cell pitch between controls; earlier it was five.
A draft thread (`Active == ""` with a draft project) previously fell into the
empty-state geometry that hides the right and bottom panes, so its toggles
changed state without effect; `workspaceGeometry` now keys on `hasComposer`. A default project is not modeled, so the title falls back
to the project chooser rather than a stored default. Visual review in a real
terminal is pending.

## Focus mark, tab insets and bottom terminal tabs — 2026-09-22

The user asked for narrower right-host tabs, a focus cue that does not move the
focused control, and a bottom panel that opens straight into a terminal tab.

- **Tabs** are now end cap, glyph, its spill cell, one gap cell, the title and
  end cap (`title + 5` cells, capped at 24), matching T3's tight `pl-1.5`/`pr-2`
  tab insets instead of centering the glyph in its slot. The close target is
  still the glyph plus spill cell; caps and the gap select the tab.
- **Keyboard focus** paints `•` (`>` with `TUI_GO_ICONS=ascii`) in accent ink in
  the cell before the control: a compact control's or tab's leading cap, an icon
  control's padding cell inside its slot, or the blank gutter before a text row
  (dialog rows, chooser rows, thread titles, settings rows). `styledButton` checks
  that the gutter cell is blank before using it and otherwise underlines, so a
  transcript row flush against the divider keeps the old cue. The mark is painted
  from `componentVisual`, so frames built directly in tests carry it too.
- **Bottom panel** holds a per-thread `shell.Host` of terminal tabs with the same
  `tab` renderer, overflow menu and trailing add control as the right host and no
  title row. Toggling the wide panel on while the thread has no bottom session
  sends `terminal.open` at once; the tab appears from the receipt. Tab close sends
  `terminal.close` for that identity; the last close hides the panel. Compact
  column selection stays presentation-only, showing the empty tab row and add
  control. Saved views that recorded the earlier single `BottomID` are migrated
  into one tab on load. Terminal tabs number past the highest existing number in
  their host, so a reopened instance never repeats a live tab's name.

Validation: `make check` (build, vet, tests) passed; `python3 scripts/pty_small_screen.py`
passed with the Terminal column now showing “No terminal sessions” and its add
control. A PTY capture at 140×40 (`F5`, Tab to New terminal, Enter) showed the
panel opening as `Terminal 1`, adding `Terminal 2`, and the mark before the
focused close slot without shifting the tab; right-host tabs rendered as
`  Files   Git ` with the mark replacing the cap on focus. Nerd Font rendering
of the glyphs beside the mark was not re-measured in a real terminal font.

## Command-line interface — 2026-09-22

The binary is a conventional CLI built on cobra. `tui-go` alone opens the TUI;
everything else is a subcommand with its own `--help`:

| Command | Behavior |
| --- | --- |
| `tui-go [--client NAME]` | Ensure the home's server is running, then attach the TUI. Refuses with a hint when stdin or stdout is not a terminal. |
| `tui-go server start` | Start the server if needed and print `state`, `pid`, `url`, `home` and `instance_id` as JSON. The bearer token is never printed. |
| `tui-go server status` | Print the same JSON for a reachable server. Otherwise exit 1 with "no server is running for HOME" or "not reachable (stale discovery or shutting down)". |
| `tui-go server stop` | Graceful stop with the confirmed shutdown outcome; failure or an unconfirmed outcome exits 1. |
| `tui-go server run` | Run the server in the foreground; `start` and the TUI launch this in the background with output in `server.log`. |
| `tui-go snapshot` | Authoritative state as JSON. |
| `tui-go probe` | Build, runtime and terminal environment diagnostics as JSON; not capability evidence. |
| `tui-go version`, `--version` | Build version: a linker `-X` value, else the module version plus the VCS revision and `-dirty` marker that the Go toolchain stamps. |
| `tui-go completion bash\|zsh\|fish\|powershell` | Shell completion script; `tui-go completion <shell> --help` shows how to install it. |
| `tui-go help [command]` | The same text as `--help`. |

`--home DIR` on any command overrides `TUI_GO_HOME`; the flag, the variable and
`~/.tui-go` resolve in that order, and a TUI launch passes its home to the server
it starts. Exit codes are 0 for success, 1 for failure and 2 for a usage error
(unknown command or flag, invalid `--client` name). Errors print as `tui-go: …`
on stderr; usage errors add `Run 'tui-go <command> --help' for usage.` Server
status categories and richer exit codes remain unselected in the
[server design](server.md#command-and-shutdown-semantics).

Enable completion for the current shell, for example:

```sh
source <(./bin/tui-go completion zsh)   # bash: source <(./bin/tui-go completion bash)
./bin/tui-go completion fish | source   # fish
```

`internal/cli` owns parsing, help, output and exit codes and is unit-tested
without a process; `cmd/tui-go` is a signal-aware `main`. The OS-PTY harnesses in
`apps/go/scripts/` ([README](../../apps/go/scripts/README.md)) drive this CLI and
run through `make pty`. See the [CLI and standards review](../research/go-cli-review-2026-09-22.md).


## ACP agents — 2026-09-22

**Default setup:** Go ACP bridges ship inside the application. Put the installed
official `claude` and `codex` executables on the **server process's PATH**; no
npm adapter or Agent SDK sidecar is required. The server keeps ACP v1 through
`github.com/coder/acp-go-sdk` v0.13.5, with serialized in-memory ACP pipes to the
bridges. The fixture remains available. See [ADR 0016](../adr/0016-built-in-go-acp-bridges.md)
and the [bridge checkpoint](../implementation/go-adapter-checkpoint.md).

`TUI_GO_AGENT_CLAUDE_COMMAND` and `TUI_GO_AGENT_CODEX_COMMAND` still select an
explicit external ACP executable when desired; unset old overrides to use the
shipped bridges. There is no command-settings form or login flow: the official
runtime inherits its existing login/configuration. Missing CLIs report
unavailable. No installation, download or billing change is performed.

The server automatically discovers the user's installed `claude` and `codex`
on its PATH and passes their absolute paths to the adapters for both probes and
thread sessions. It never selects bundled provider runtimes. No runtime-path
variables are required for normal use. `CLAUDE_CODE_EXECUTABLE` / `CODEX_PATH`
remain optional explicit local executable overrides; invalid overrides fail
without fallback. Missing CLIs report unavailable with setup guidance. Probe
details and server logs identify the selected local path. Shell aliases are not
executables, and an already-running server keeps its inherited PATH/environment;
restart the intended server after changing them. Native ACP agents are unaffected.

After publishing discovery, startup probes run concurrently with a 30-second
limit. Each initializes an adapter and creates then closes a provisional session
in the project checkout (or Project starting folder). The composer agent menu
lists Fixture agent, Claude and Codex with unprobed/probing/ready/unauthenticated/
unavailable states and details. Select an agent in a New thread draft, then
select a model from its reported options. Probe or Refresh options sends
`agent.probe`; concurrent requests coalesce. Readiness is a probe result, not a
guarantee that a later dispatch or account quota will succeed.

Model, effort, permissions, context and speed choices come from reported option
values. Missing fields retain agent defaults; unmapped options are displayed
read-only under Agent defaults. Live session options supersede probe results and
can change when a model changes. Send captures settings; dispatch applies mapped
values with `session/set_config_option` before `session/prompt` and records the
acknowledged state as effective settings. Active/waiting settings are read-only.
Rejection leaves the prompt queued with a visible failure and requires explicit
retry: the live HTTP check saw a model change reject the captured effort value.

Streamed messages, reported thoughts, tools and plans feed the existing activity
views. Unknown update kinds remain visible as activity; no hidden reasoning is
inferred. Stream persistence/publication is coalesced to at most ten times per
second, with commands and turn outcomes flushed immediately. Stop sends
`session/cancel` only for active/waiting work. Confirmed cancellation preserves
partial output and queued input, sets interrupted/NeedsResume, and holds the
queue. Resume releases queued work; it does not resend the cancelled prompt.
Reattaching a client catches up without resubmission.

ACP permission calls become blocking approval cards with the agent's choices
and supplied details. Answers target request identity/revision and the exact
`ApprovalChoiceID` under the `approval-choice-ids` capability; legacy labels are
accepted only if unambiguous. The server commits `submitted` / `acp-accepted`,
then records response preparation as `closed` / `acp-unconfirmed`. Neither is
upstream receipt evidence. The original connection generation, session, turn and
pending callback must still match. The bounded Claude route below is supported;
other ACP question dialects remain rejected. The Activity inspector distinguishes acceptance,
uncertainty and cancellation, and legacy `acp-delivered` is never confirmed.
See the [delivery checkpoint](../implementation/request-delivery-checkpoint.md).
In the earlier [adapter probe](../research/acp-live-probe-2026-09-22.md),
Claude requested approval for a file write while Codex wrote within its checkout
without a request in its nominally cautious mode. Permission names do not prove
sandboxing or matching approval boundaries. No `fs` or `terminal` client
capabilities are advertised; adapters may still access files and processes
directly. Per-checkout writer scheduling now serializes this server's own
turns (see "Checkout writer scheduling" below); it still does nothing to
prevent an external editor, shell, or unrelated process from writing the same
checkout concurrently, so this slice does not establish safe concurrent
write-capable turns in the same checkout in general.

`usage_update` supplies context occupancy/capacity, source and freshness to the
gauge and Usage inspector. Missing telemetry, subscription windows and cost are
unavailable; the application fetches no external pricing/quota data and invents
no measurements. ACP child-agent dialects and real-agent steering are not
negotiated in this slice, so the Agents surface has no structured ACP children
and Steer remains unavailable for these threads. MCP, images and general
continued-work questions are not validated integrations.

Processes belong to the background server, one per ACP thread session. TUI exit
leaves them running; server stop closes them. A restarted server cannot reattach
the old process: interrupted work requires explicit Resume. The next queued
dispatch starts a new process and attempts `session/load` when advertised,
otherwise starts a new
session with a visible context-restoration notice. Pending approvals from a lost
process become `closed` / `acp-undeliverable`; submitted answers and old
`acp-delivered` records become `closed` / `acp-uncertain`. The accepted snapshot
is preserved and never replayed. Stale answers cannot reach a new process.
Real `session/load` recovery has not been exercised live, and ACP work is not
eligible for automatic restart continuation.

The [personal-prototype recovery continuation](../implementation/agent-recovery-checkpoint.md)
commits dispatch before provider I/O. A post-dispatch failure keeps the full
capture in history and requires Resume; it never puts that prompt back in the
queue. Send preserves this failure warning/gate while adding new queued input.
Pre-dispatch setup/settings failures retain unsent work. Current-turn captures
survive long streams within the retention bound. Approval admission now saves
before publication and reserves accepted-choice capacity, like native questions.
[Fresh live checks](../research/agent-recovery-live-2026-09-22.md) cover the
bounded Claude native question route, unconfirmed answer/restart states, and
both providers' Stop/Resume. They do not establish authoritative Answered
receipts, general question support or live session loading.

The additive server capabilities are `acp-agents`, `agent-probe`,
`acp-permissions`, `acp-cancel` and `approval-choice-ids`. Rebuild and restart the intended server to load
them; relaunching only the client retains the old backend and its environment.
From `apps/go`, after detaching the old client:

```sh
make build
./bin/tui-go server stop
./bin/tui-go --client desk
```

Use the same `TUI_GO_HOME` for all three commands and set adapter PATH/overrides
before launch. For isolated checks, choose a separate absolute temporary home.
Stopping interrupts owned work; restart follows the recovery rules above.

The [handoff](../implementation/acp-live-validation-handoff.md) records live
HTTP checks of both adapters' pong turns, startup/refresh probes, settings and
usage, plus Claude cancel/resume and permission delivery. These are distinct
from OS-PTY evidence in the [validation report](../research/go-acp-2026-09-22.md).
The handoff also records shutdown defects and one unreproduced shutdown deadline
failure; consult that report for subsequent fixes and checks. Evidence is limited
to one macOS machine and operator accounts, with no SSH/tmux, live session-load,
load-testing or multi-client ACP claim. See the [checkpoint](../implementation/acp-checkpoint.md)
and [ADR 0013](../adr/0013-server-owned-acp-agent-processes.md) for the implementation
contract and remaining limits.

## Bounded native questions — 2026-09-22

Pinned Claude adapter 0.80.0 now has a bounded blocking AskUserQuestion route
through `elicitation/create` and the existing app `request.answer` command.
It supports 1–4 questions, single/multiple choice, supported Other text and
optional omissions. It preserves accepted snapshots without claiming delivery
confirmation. Codex forms/steering, general MCP/URL forms, child questions,
true asynchronous questions, fallback scheduling and Answered history remain
unavailable. See the [implementation checkpoint](../implementation/native-question-checkpoint.md)
for exact limits and [validation evidence](../research/native-questions-2026-09-22.md).

### Question actions and option descriptions — 2026-09-23

Contract fields added (all `omitempty`, so older snapshots, commands and retry
fingerprints are unchanged):

| Field | Meaning |
| --- | --- |
| `Question.OptionDescriptions []string` | Supplied option detail, parallel to `Options` (empty entries allowed); any other length fails validation. Populated by the Claude and Codex parsers instead of appending to `Text` |
| `Request.Actions []string` | Non-answer responses the upstream contract supports: `decline`, `cancel`. Claude/ACP elicitation: both; Codex: none; fixture `question-blocking`: both, `question-async`: none |
| `Request.Action string` | The action a client chose; empty for answers and for the server's own withdrawal |
| `Command.RequestAction string` | On `request.answer`, declines or cancels instead of answering; answers must be empty and the action offered |

The fixture resolves a decline or cancel as it resolves an answer
(`resolved`/`fixture-confirmed`). An ACP decline or cancel is answered to the
peer as `{"action":"decline"|"cancel"}` with no content and stays unconfirmed.
See [question actions](questions.md#question-actions-and-option-descriptions--2026-09-23)
and the [implementation note](../implementation/question-actions-2026-09-23.md).

## Read-only attachment viewer — 2026-09-24

First slice of the [read-only preview](activity.md#clipboard-intake-and-read-only-previews)
viewer (`internal/tui/attachment_viewer.go`). It is a centered rounded dialog over
the modal backdrop, holding a copy of one attachment taken when it opens, so later
draft or snapshot changes never alter what it shows.

- **Entry points** (keyboard and pointer through the same `attachment-view`
  action): a composer strip chip's label (below) or **View <name>** beside
  Remove in the composer's attachments menu;
  **View · <name>** in the prompt queue menu, showing the queued capture; and one
  activatable row per captured prompt attachment in Activity/Agents detail
  (glyph, name and source, with `kind · size` at the right). Activity no longer
  dumps captured content inline.
- **Presentation:** kind icon and name, then fixed icon slots for the Markdown
  mode control (eyeball for Preview; an explicit `Raw` text action in preview;
  `Preview`/`Raw` text with ASCII symbols), expand/restore and close. Kind,
  Source (left-truncated), Size (bytes, or KiB plus bytes) and, for Markdown,
  Mode pairs. Raw text has a line-number gutter and cell-aware wrapping;
  Markdown (`.md`/`.markdown` name or source) opens raw and previews through
  `markdownLines`. Default size is at most 96×32, centered; expanded fills the
  area between the top chrome row and the status row.
- **Keys:** Esc closes and returns focus to the originating control; `p` toggles
  Preview/Raw (Markdown only), `f` expands/restores, Tab/Shift+Tab move across
  the body and header controls, Enter/Space activate, and Up/Down/PgUp/PgDn/
  Home/End or the wheel scroll, with the normal scrollbar. All other keys and
  pastes are captured; Ctrl+Q/Ctrl+Z and copy keep their global meaning.
  Clicking outside closes and consumes the click. Dragging over the body
  selects text through the existing transcript/surface selection, and
  Ctrl+C/Ctrl+Shift+C copies it. Right-clicking selected body text (or the
  Menu key/Shift+F10) opens the same "Selected text" Copy menu used elsewhere,
  layered above the still-open viewer; Esc dismisses the menu back to the
  viewer, and the selection survives it. Copying a raw-mode selection that
  spans a soft-wrapped source line reproduces that line exactly, including
  trailing spaces at the wrap point; only hard line ends join with a newline.
- **Honest states and loading:** loads run in a `tea.Cmd` tied to that viewer
  open; a result for a closed or reopened viewer is dropped. Draft workspace
  files load through `PreviewFile` (Send capture rules) and copied files through
  a bounded local read (text up to 64 KiB shown; images and other binaries show
  metadata or "Preview unavailable for <type>"); either records its SHA-256 as
  the draft attachment's `PreviewSHA256`, which Send passes on. Accepted
  artifacts with no inline content fetch text types (1 MiB bound) with
  `FetchArtifact`. Images: see kitty graphics below. Fixture draft kinds
  still show "Captured when you send"; an accepted empty capture shows "Empty
  capture". Accepted captures with `ChangedSincePreview` show a Notice pair in
  the viewer and an attention-marked "Changed since preview" row under the
  Activity attachment row. Content passes through `safe()`; nothing is executed
  or opened in an editor.

### Clipboard image and file intake — 2026-09-24

`internal/tui/clipboard_intake.go`. Only the explicit app Paste (prompt menu
Paste and the forwarded paste shortcuts) reads the clipboard; bracketed paste
stays text-only and nothing polls. SSH/herdr sessions keep the existing notice,
now pointing at `@` file mentions. On Linux with `WAYLAND_DISPLAY` and
`wl-paste` on PATH, Paste runs `wl-paste --list-types` (2 s, 64 KiB) off the
input path and chooses `text/uri-list` or `x-special/gnome-copied-files`, then
`image/png|jpeg|gif`, else the existing text Paste. Elsewhere (macOS, X11) only
text Paste is available; image/file intake is a documented gap.

- **Images** are read (16 MiB bound, 10 s) and uploaded to server staging at
  Paste; the draft keeps `{Kind: artifact, Name: clipboard-<time>.<ext>,
  ArtifactID, MediaType, Size, Width, Height}`.
- **Copied files** are local `file://` URIs (percent-decoded; other hosts,
  non-file schemes, directories and non-regular files are skipped with a
  notice) kept as `{Kind: copied-file, Name, Source: absolute path}`; nothing is
  uploaded at Paste. At Send the client reads (16 MiB) and uploads each one,
  records the artifact id on the draft attachment (a retry reuses it), and only
  then dispatches the command built when Send was pressed. A failed read or
  upload sends nothing, keeps the draft and names the attachment; Send is
  refused while a capture is in flight.
- Each result carries its originating draft (thread or new-thread project) and
  an intake id consumed on first delivery; it applies to that stored draft even
  after navigation, and is dropped with a notice if the draft is gone. The
  8-attachment cap applies at Paste and when results land.

- A pasted image dropped before it lands (draft gone, queued edit, cap) and a
  draft artifact removed from the composer are deleted from staging with
  `DeleteArtifact` off the input path; `accepted`/`not_found` results and other
  failures are ignored (staging expiry remains the backstop). Accepted captures
  are never deleted. Copied files sent as artifacts carry their client-local
  absolute path as display-only `Source`. Upload/preview `busy` and fetch
  digest `unavailable` errors produce honest notices.

### Composer attachment strip — 2026-09-24

`internal/tui/attachment_strip.go`. While the draft has attachments, a strip
inside the rounded prompt outline sits directly above the padding and typing
rows. Each attachment is a single-row square-fill chip (panel fill at rest,
stronger neutral on hover) built like a surface tab: end cap, kind-icon slot,
gap, cell-truncated name (chip width ≤ 24), end cap. Hovering the chip or
focusing its icon slot shows a close glyph there; the glyph and its spill cell
(`attachment-chip-close:<i>`) remove that attachment, and the label
(`attachment-chip:<i>`) opens the viewer with focus restored to that chip.
Chips follow the prompt in Tab order; Enter activates the focused target and
Delete/Backspace removes the focused chip, keeping focus on the strip. Removal
is bound to index and name, so a changed index removes nothing. Chips that do
not fit leave a trailing `+N` chip that opens the attachments menu (View/Remove
for every attachment). In a compact pane (fewer than 28 rows) the strip
collapses to the former single aggregate control above the composer, so the
typing rows and controls row keep their space.

### Kitty graphics — 2026-09-24

`graphics.go`, `graphics_query.go`, wired in `graphics_model.go`
([ADR 0018](../adr/0018-kitty-graphics-placeholders.md)). XTVERSION is now also
requested under true color, only for this gate. A reply naming kitty or
Ghostty (and nothing else) sends one `a=q` probe plus a cell-size request,
terminated by DA1; `OK` enables graphics, anything else or 1 s of silence keeps
the fallback. `NO_COLOR`, tmux/screen/herdr, a profile below 256 colours and
`TUI_GO_GRAPHICS=off|0|false|no|none` disable it. Image ids are 16–255 (a lower
256-colour index can be re-emitted as a basic SGR colour). With only 240 ids and a random
base, this client's ids can collide with another program's images in the same
kitty window; multiplexers are off, so only same-window programs are exposed.

- **Viewer:** image artifacts are fetched (16 MiB) only when graphics are
  enabled; copied-file drafts are read locally. The image is prepared off the
  update path for the body box, keeping aspect ratio and never upscaling past
  its natural cell size, and re-fitted on resize and expand/restore (re-placed,
  not retransmitted). Ready images render as Unicode placeholder rows centred
  on the dialog fill; while loading, and on terminals without graphics, a
  styled metadata block (name, type, pixel size, bytes) of the same size is
  shown, never claiming a preview. The image is deleted when the viewer closes.
- **Thumbnails:** with graphics enabled and an image in the draft, the strip
  grows to three rows: a thumbnail (fitted to 8×2 cells) above each image chip,
  a loading block until it is placed. Thumbnails of removed attachments, or of a
  draft no longer shown, are deleted.
- **Cleanup:** Ctrl+Z/Suspend and every quit/detach path (Ctrl+Q, Ctrl+C,
  command palette) first delete this client's image ids (`a=d,d=I`, never
  `d=A`); `Run` repeats it for exits that bypassed them. Images retransmit
  lazily after resume. A window resize re-requests the cell size, and a changed
  cell size or capability re-prepares what is shown.

Verified by unit tests and deterministic captures only (placeholders render as
missing glyphs there). Live kitty/Ghostty rendering is **not** verified: neither
terminal is installed here. A 2026-09-24 foot review (dark theme, about 144×53
and 144×22) covered `@` mention, the strip's icon-slot close and focus, viewer
Raw/Preview/expand/restore/close, queue and Activity View and the collapsed
strip, with no defects; foot has no kitty graphics, so images showed the
fallback. Not implemented: image/file intake on macOS and X11, a server
capability advertising artifact support, and graphics inside multiplexers.

### Checkout writer scheduling — 2026-09-24

`internal/server/writer.go` derives, per canonical checkout
path, at most one running/waiting thread and a FIFO wait order over the
threads with queued work blocked behind it; see its header comment and the
[workspace contract binding](workspaces.md#writer-coordination) for the exact
rules. The lease covers the claim-to-dispatch window, and a deleted thread's
claim keeps it until that dispatch returns, so a deleted holder's adapter
cannot overlap the next writer. Recovery gates idle threads with queued,
never-started prompts behind explicit Resume. A fixture Resume that would
continue an interrupted turn in place is rejected with `checkout_busy` while
the checkout is held or an earlier thread waits. `protocol.Thread` gained
`WriterWait *WriterWait` (`HolderThreadID`, `Position`, `1` = next), set only
on a thread with eligible queued work and cleared on load — a persisted value
never blocks dispatch.

The TUI (`internal/tui`) makes the wait visible without claiming it is an
active turn: `activeTurn` stays `false` for a writer-waiting thread (its State
is `idle`), so Stop is not offered and queued prompts remain editable/
removable as before. The conversation status line
(`transcriptLines`/`writerWaitLine` in `render.go`) reads "Waiting for
checkout", appended with "· \<holder title\>" when the holder is known in the
current snapshot and "· 2nd in line" (etc.) when `Position > 1`; the line
reuses the existing thread-select action, so activating it selects the holder
thread the same way a navigation card does. The navigation card indicator
(`thread_indicator.go`) gained `threadCheckoutWaiting`, a neutral circle (the
status colors stay reserved; hover/focus help names the wait), placed below the existing
failed/attention precedence so a checkout wait never hides a real problem.
`thread.resume` rejected with `checkout_busy` now shows a notice through the
existing command-error path ("Checkout busy: another thread is writing;
Resume when it finishes") instead of failing silently.

Validated on 2026-09-24 with `make check` (gofmt, vet, staticcheck,
`go test -race ./...`, build) and `go test -race -count=3 ./internal/server/...
./internal/protocol/...`. Server tests in `internal/server/writer_test.go` cover
serialization, different checkouts, FIFO yielding, release on Stop/delete/
failure, waiting holders, reopen-send, restart gating, agent re-probe and
unavailable-agent waiters, and concurrent sends; TUI tests are in
`internal/tui/writer_wait_test.go`. Two independent adversarial reviews found
and then confirmed fixes for missed wakeups and a deleted-holder overlap.
`TestNativeApprovalAdmissionRejectsWithoutMutation` fails intermittently on the
previous commit as well (a test race with asynchronous turn completion); it is
not caused by this change. Only fake ACP agents were used; no live provider or
interactive terminal review covered this scheduler. This does not make concurrent
writes from outside the application (an external editor, shell, or unrelated
process touching the same checkout) safe; see the caution above.

### Read-only Git surface — 2026-09-24

The right-host Git surface (`internal/tui/git_surface.go`,
`git_viewer.go`) replaces the former "Git integration unavailable"
placeholder with read-only observations from `GET /v1/git/{status,diff,log,show}`
(`internal/client/git.go`). It offers no staging, commit, branch or other
mutating control, not even a disabled one.

- **Header and checkout.** The GIT heading carries a glyph-only Refresh icon
  (Nerd Font `cod-refresh`, plain `R`; help "Refresh Git status · read-only";
  reachable by Tab/Enter). The Checkout/Branch/HEAD pairs keep their existing
  vocabulary; once the surface's status read returns, its workspace supplies
  them. `Upstream` shows the tracking ref with `↑n ↓n` from local refs (or
  "up to date"), or a muted "none" on a branch without one. A merge, rebase,
  cherry-pick, revert or bisect adds a bold "! Merge in progress" row with the
  muted line "Read-only here · continue or abort it with Git".
- **Changes.** CONFLICTS, STAGED, CHANGES and UNTRACKED headings carry
  counts and are omitted when empty; a Git checkout with no entries shows
  "Working tree clean". Each entry is one full-row activatable square-fill row
  (hover and keyboard focus per the component rules; the focus mark takes the
  blank cell before the row): a status letter in semantic ink (A green, M/T
  gold, D red, R/C accent, conflicted U red, untracked ? muted) and the path,
  truncated from the start so the file name stays visible; renames read
  `old → new`. A capped status adds "Showing first N changes".
- **Recent commits.** 50 commits from HEAD: muted short hash, subject, ref
  labels (`refs/heads/`, `refs/tags/`, `refs/remotes/` stripped; HEAD bold
  accent, overflow as `+n`) and a compact relative age right-aligned when the
  row has room. No graph lanes.
- **States.** "Reading Git status…" before the first result; "Refreshing…"
  while a later read runs; a failed read keeps the last result with a stale
  "! Showing the last result" row and the error, or shows "✕ Git status ·
  failed" with the error when nothing was loaded. Non-Git, fixture ("Demo
  checkout · no repository") and unavailable workspaces are single status
  blocks without sections or commits.
- **Reads.** Every read is a `tea.Cmd` tagged with its target key (the
  checkout inspection's thread/project key) and a generation; results for
  another displayed target or an older generation are dropped. Results are
  kept per target, so switching back paints the retained result and then
  refreshes. Reads start when the surface becomes visible (active right-host
  tab or compact Surfaces column), when the displayed thread/project changes
  while it is visible, on Refresh, and when the active thread's turn ends
  (running/waiting to anything else) while it is visible. There is no timer or
  polling. `Model.gitReads` injects a fake in tests.
- **Viewer.** Activating an entry or commit opens the existing centered
  read-only viewer with Git content instead of a new dialog: the title is
  `path · Staged|Unstaged|Untracked|Conflicted` or `short-hash subject`; pairs
  are Path/Group/Size or Author/Date/Commit/Refs/Size; a commit body (without
  its repeated subject) precedes the server's stat and patch. Lines are
  sanitized with `safe()` and colored by position in the patch: `diff --git`
  bold muted, other file headers muted, hunk headers accent, additions green
  and removals red (inside a hunk a removed `--x` line is still a removal).
  There is no line-number gutter or Markdown mode. Binary diffs read "Binary
  file; no text diff"; a truncated patch ends with a muted "Diff truncated at
  512 KiB" row that is not part of the source text. Selection Copy, expand and
  Esc work as for attachments, and focus returns to the originating row. On
  the surface, Up/Down move focus between rows and keep the focused row in
  view.
- **Server read policy** (`internal/server/git.go`). Global and system Git
  config is honored. Repository-local config is treated as untrusted:
  - **Local filters.** Any `filter.<name>.*` key in local or worktree scope
    disables that driver entirely, even one defined globally. For example,
    `git lfs install --local` makes LFS files read raw.
  - **Global drivers** still run and may read repository files such as
    `.lfsconfig`.
  - **Redirection.** gitfile, alternates, replace refs and `core.worktree` are
    honored as Git configures them. A checkout can therefore show another
    repository's data, read-only; this is accepted for this slice.

Tests: `internal/tui/git_surface_test.go`. `TestGitSurfaceCaptures` writes
render captures (`TUI_GO_CAPTURE_DIR`), optionally from real reads supplied as
JSON in `TUI_GO_GIT_JSON`; the 2026-09-24 review used reads of this
repository at 144×40 and 44×40/44×30 in dark and light. Not yet verified in a
real terminal (foot) or with `make pty`.
