# Draft creation, configuration and closed composer refinement

User direction, 2026-09-20 (implemented): New thread opens a client draft; the initial
Send creates the authoritative thread with valid selected configuration and input.
Settings are read-only only during active work (explicit user clarification),
editable when idle. Codex/Claude remain required future integrations; this slice
must expose Demo/Reference honestly. Closed threads can be read/composed without
reopening; Send reopens and submits atomically, with a separate Reopen control.
Add read-only checkout/branch context below the existing composer controls.

Implementation assumptions: retain one unsent creation draft per project/client,
including prompt/settings/attachments through navigation/relaunch. Missing project
does not silently discard a draft. Explicit model choice selects the only available
Demo model with supported defaults; validate all required fields at Send. Active
work uses confirmed running configuration, retaining any existing idle selection
for later. New capabilities gate first-send creation and closed-thread Send.
Checkout metadata is an explicit server read outside rendering; unavailable and
non-Git checkouts must never show a fabricated branch.

Additional request: centered menus/dialogs dismiss on an outside click without
click-through. Dim the text-grid background; actual web-style blur is unavailable.
Preserve drafts and route all commands through existing effects.

Work is uncommitted, no pull/push. Runtime checks must use isolated temporary homes.
Sidebar refinement: a horizontal rule separates the pinned Closed area from
the bottom app-action shelf. Settings is currently its only action; leave room
for future actions without inventing a Usage integration.

Reconciliation preserves newer text typed before a receipt and views opened from
a snapshot before that receipt. If both target and creation drafts contain newer
input, keep both; New thread restores the retained project draft. Only one initial
creation handoff may await snapshot reconciliation at a time. An explicit queued
text edit preserves its captured configuration while active controls are locked.

Completed validation: `GOCACHE=/tmp/tui-go-build make check` passes, including race
checks and rebuild. Isolated OS-PTY navigation 27, settings 27 and small-screen 42
checks pass (96 total). Renderer geometry covers 40/47/80/160 columns in both themes;
selected dark/light frames were visually reviewed. Modal/fallback, retry/receipt,
server/storage and metadata fixture checks pass. No fresh native terminal, SSH,
tmux or physical-phone verification. [Full evidence](../research/go-draft-composer-2026-09-20.md).

Binary rebuilt. User server/data untouched. A user-controlled server restart is
required for the new capabilities; relaunch the TUI as well. No commits or remote
Git operations. Real Codex/Claude, worktree, editor and embedded-terminal work is
still incomplete; Keybindings overrides remain explicitly unavailable.
