# Cell-native component implementation — 2026-09-20

The user approved the [component construction rule](../design/components.md)
and [ADR 0008](../adr/0008-cell-native-component-state.md). This Go pass replaces
the earlier pill/stepped-fill experiments with three explicit variants:
rounded outline, square outline and compact square fill.

Prompt, thread, queue, question/approval and dialog containers keep rounded outlines.
Their interior backgrounds remain stable; corner cells use the surrounding
surface background. Question tabs, surface tabs and compact request actions use
one-row square fills, reclaiming space for content. Square outlines are available
through the same helper when an explicit straight boundary is appropriate.

A pure visual resolver accepts selection, hover, keyboard focus, disabled state
and action role independently. Rest and hover use different neutral treatments;
selection keeps accent plus bold when another control is hovered. Keyboard focus
adds an underline to control labels. The prompt retains its insertion caret and
focused border. Primary Submit emphasis does not falsely mark it selected.
Execution status colors retain their meaning. Limited-color/plain controls keep
brackets in reserved end cells, so fallback cues do not move hit targets.

The same measured rectangles supply painting and hit testing. The tab icon/close
slot stays separate from its name and end cells. Hidden question arrows retain
their space; overflow keeps the current question visible. The rounded menu's
scroll calculation reserves its bottom border, keeping the last action reachable.
Rendering remains free of persistence and dispatch effects, and retained ANSI
styling remains bounded by the existing frame compaction fix.

The user's subsequent queue refinement groups the count, message previews and
Edit/Remove/reorder buttons in one rounded container. Each row shares an inset
and alignment; multiline text is flattened only for its bounded preview. Two
items remain visible at normal heights, one below 28 rows, with hidden-item
access in the header. First/last move arrows are visibly disabled without a hit
target. The height budget includes the outline, adding one row normally and two
in short layouts. The original queued text, settings, captures and draft are
preserved; commands retain their existing target identities and queue revisions.

## Validation

Run on macOS darwin/arm64, from `apps/go`:

- `GOCACHE=/tmp/tui-go-build make check` — PASS: formatting, vet, all race tests
  and build. Tests cover state precedence across both themes and color profiles,
  selected-tab identity during pointer movement, fixed hit geometry, keyboard
  activation, monochrome cues, question overflow and existing lifecycle behavior.
  Queue checks cover matching mouse/keyboard edit, remove and reorder targets,
  original draft/text preservation, disabled boundary arrows and full queue
  access without covering requests or the composer at 48 × 22.
- `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-components-queue-pty`
  — PASS, 38 checks, rerun after adding the queue container. Includes pointer/keyboard question navigation and explicit
  structured Submit, prompt draft retention, narrow overflow, resize,
  suspend/resume, detach, two clients and explicit recovery after server restart.
  [Retained report](go-components-captures/pty-smoke-report.json).
- `python3 scripts/pty_navigation.py --artifacts /private/tmp/tui-components-navigation-pty`
  — PASS, 18 checks. Includes project selection, card metadata/edge activation,
  Close/Reopen, confirmed Delete across clients and preserved project files.
  [Retained report](go-components-captures/pty-navigation-report.json).
- Generated fresh Go View captures with `TestPolishedViewCaptures`,
  `TestQuestionReviewCaptures`, `TestColorReviewCaptures`, `TestNavigationCaptures`,
  `TestComponentStateCaptures` and `TestQueueCardCaptures`. The capture renderer
  now preserves bold and underline, allowing inspection of the non-color cues.
- `git diff --check` passed. Reviewed the presentation/input geometry changes:
  no server, schema, authoritative storage or command dispatch changes were
  needed for this component pass; hovering cannot submit or duplicate work.

Inspected and retained the actual renderer output:

- Full application: [dark](go-components-captures/160x50-lightfalse-tabs.png),
  [light](go-components-captures/160x50-lighttrue-tabs.png),
  [narrow question](go-components-captures/48x22-lightfalse-question-narrow.png),
  [rounded dialog](go-components-captures/160x50-lightfalse-menu.png).
- Three-variant state matrix: [dark](go-components-captures/160x20-lightfalse-components-TrueColor.png),
  [light](go-components-captures/160x20-lighttrue-components-TrueColor.png),
  [16-color dark](go-components-captures/160x20-lightfalse-components-ANSI.png),
  [16-color light](go-components-captures/160x20-lighttrue-components-ANSI.png),
  [monochrome](go-components-captures/160x20-lightfalse-components-Ascii.png).
- Queue grouping: [dark](go-components-captures/160x50-lightfalse-queue.png),
  [light](go-components-captures/160x50-lighttrue-queue.png),
  [48 × 22](go-components-captures/48x22-lightfalse-queue.png).

Compressed ANSI and SVG sources accompany the PNGs. These are deterministic
render captures using JetBrainsMono Nerd Font Mono, not native GUI screenshots.
The native PTY harness uses isolated temporary server homes. Native Ghostty
visual review remains unverified because computer-use access was blocked by the
tool's app safety restriction during this work. Omarchy/SSH/tmux and iTerm2 were
not rerun; local PTY results do not establish those combinations' compatibility.
Font-dependent glyph shape and limited-color palette differences remain subject
to interactive review. No new dependency or graphics protocol is required.

From the repository root, detach the old TUI and run `./apps/go/bin/tui-go`.
The existing server can remain running. Rebuild with `make -C apps/go build`.
Further visual adjustments should use the shared component rule and resolver;
provider and real embedded-terminal integration gaps remain unchanged.
