# Small-screen columns — 2026-09-20

The reported phone viewport is 47×22. The Go client now accepts widths down to
40 columns with 22 rows and uses a hamburger column picker below 60 columns.
This follows the user's suggestion to view one column at a time. See the
[shared behavior](../design/layout.md#single-column-layouts-on-small-screens).

Conversation, Projects & threads, Surfaces and Terminal use the full content
width above the composer. Questions, queue and activity are kept in Conversation;
the bell stays reachable in every column and reveals pending questions explicitly.
The other views cap the prompt viewport at three rows. This tradeoff leaves enough
space for a complete thread card or surface content while preserving the draft,
settings/actions, request identity and terminal sessions. Column selection is
client/thread-local and independent of wide layout preferences. Selecting a thread
or attention item returns to Conversation; an existing compact selection survives
reconnect and a narrow/wide/narrow resize.

The former size guard also stopped activity animation. Both now use the same
40×22 limit. Below that limit, the resize notice preserves work and keeps Commands
and detach available; plain Enter/Ctrl+S cannot submit the invisible editor. F6
visits only visible content, and compact resize commands leave wide pane sizes
unchanged. Opening a terminal explicitly reveals Surfaces, but its later receipt
does not override a subsequent column selection. Closing the last right terminal
returns to Conversation after its accepted close; choosing another column never
closes it. Real embedded terminals remain an integration gap.

## Validation

From `apps/go` on macOS:

- `GOCACHE=/tmp/tui-go-build make check` — PASS: formatting, vet, race tests and
  build. Tests cover 40/47/53/59-column geometry, both themes, long prompt and answer
  drafts, retained wide preferences, compact view restoration, visible F6 targets,
  attention navigation, delayed terminal receipts, and below-minimum input.
  Existing queue, request feedback, activity and measurement tests also cover
  40×22 and 47×22. The original 47×22 layout regression failed on the old guard
  before the change.
- `python3 scripts/pty_small_screen.py --artifacts /private/tmp/tui-small-columns-20260920`
  — PASS, 40 checks. Isolated servers and OS PTYs exercise hamburger pointer
  selection, keyboard menus, every column, draft preservation, wide geometry
  restoration, narrow column restoration, question validation/submission, Send,
  Ctrl+Q cleanup and server survival. [Retained report](go-small-screen-captures/pty-report.json).
- `TUI_GO_CAPTURE_DIR=/private/tmp/tui-small-columns-captures GOCACHE=/tmp/tui-go-build go test ./internal/tui -run TestCompactColumnsPreserveWorkAndWideLayout`
  — PASS. Rendered with `scripts/render-capture.py` and JetBrainsMono Nerd Font
  Mono; reviewed normal and constrained columns in both themes.
- Python syntax, local documentation links and `git diff --check` passed.

Retained deterministic renders (PNG, SVG and compressed ANSI):

- [47×22 Conversation](go-small-screen-captures/47x22-lightfalse-column0.png)
- [47×22 Projects & threads](go-small-screen-captures/47x22-lightfalse-column1.png)
- [40×22 Surfaces](go-small-screen-captures/40x22-lighttrue-column2.png)
- [40×22 Terminal](go-small-screen-captures/40x22-lighttrue-column3.png)

These are fixture-driven renderer/OS-PTY checks, not native iPhone, SSH, tmux,
Ghostty or iTerm2 compatibility evidence. The PTY harness uses a limited ANSI
screen parser. Phone-specific tap/key delivery and an on-screen keyboard reducing
the height below 22 rows still need device review. No dependency, server schema
or provider integration changed. Reopen the rebuilt client; no server restart is
needed for this presentation change.
