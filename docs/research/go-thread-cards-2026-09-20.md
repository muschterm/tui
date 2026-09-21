# Thread cards and rounded controls — 2026-09-20

The following captures record the initial filled-pill pass. The user's subsequent
request for subtler rounding and inset card backgrounds is recorded in the
[rejected corner experiment](go-subtle-corners-2026-09-20.md). Both cap treatments
were followed by the [prompt-corner experiments](go-prompt-corners-2026-09-20.md).
The accepted [component rule](../design/components.md) and
[current Go validation](go-components-2026-09-20.md) supersede these experiments.

The user requested an icon-only New thread action, contained sidebar threads,
then rounded corners matching the prompt and rounded buttons/tabs where useful.
The [pinned T3 inspection](t3-code-design.md#compose-action-and-thread-surfaces--2026-09-20)
supplies the square-and-pencil action and shared thread-surface reference.

The header now contains a centered compose icon with New thread focus help and
Tab/Enter/F4 access. Each thread has a rounded four-row card containing its title
and metadata, with a blank row between cards. Whole-card hover/focus includes
metadata, padding and borders. Those areas select/Reopen; the status quick action
and vertical menu retain their existing targets and title gutter. Shared viewport
measurement keeps navigation scrolling and background-thread animation aligned.
Cards cost more vertical space than the previous two-line rows; all threads remain
reachable through scrolling. No server, schema or lifecycle behavior changed.

Question/approval cards now share the prompt's rounded outline. Question tabs,
arrows, request actions and surface tabs use single-row pills. The surface-tab
close icon retains its three-cell target; rounded caps select rather than close.
Gaps and caps participate in overflow measurement. Question arrow slots, answered
marker widths and explicit submission remain stable. Pane toggles stay unboxed.

The rich caps use Nerd Fonts v3.4.0 `ple-left_half_circle_thick` (U+E0B6) and
`ple-right_half_circle_thick` (U+E0B4); New thread uses `fa-pen_to_square` (U+F044).
These values were checked against the already-downloaded pinned glyph metadata.
Plain/limited-color pills use equal-width parentheses, also used when fill and
surrounding colors match. Plain thread outlines use ASCII borders. No dependency
or terminal capability was added; the configured font still determines glyph shape.

## Rendering correction

During development, extra cap painting exposed exponential growth of invisible
SGR codes when joining both outputs of `x/ansi.Cut`. Inspection of pinned
`github.com/charmbracelet/x/ansi@v0.11.8/truncate.go` confirmed that its cuts retain
escape sequences belonging to discarded text. The initial rendering tests/captures
became slow and one capture run timed out; this implementation was corrected before
the final native checks. Superseded large temporary captures were removed.

Pills are composed before insertion, and frame composition now discards earlier
SGR codes superseded by a full reset within a consecutive SGR run. Text, partial
attribute changes, RGB zero components and other escape sequences retain their
meaning. A regression test repeatedly paints cells and bounds retained row size.
The largest of the final 44 fixture captures is 30,632 bytes of ANSI. Rendering
still has no application effects or persistence writes.

## Validation

Run from `apps/go` on macOS darwin/arm64:

- `GOCACHE=/tmp/tui-go-build make check` — PASS: gofmt, vet, all race tests, build.
- Focused checks cover mouse/keyboard icon creation, selected project and draft
  preservation, card metadata/border routing, close/menu isolation, rounded tab
  end selection, overflow/reserved slots, fallback text geometry and SGR retention.
- `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-rounded-controls-pty`
  — PASS, 38 checks: question delivery, radio/checkbox/text answers, pane/tab/terminal
  fixture lifecycle, resize, suspend/resume, multi-client drafts and restart recovery.
- `python3 scripts/pty_navigation.py --artifacts /private/tmp/tui-rounded-navigation-pty`
  — PASS, 18 checks: compose icon, project scope, metadata selection, border Reopen,
  preserved drafts, explicit deletion and unchanged project files.
- `GOCACHE=/tmp/tui-go-build go test ./internal/tui -run '^$' -bench 'BenchmarkViewWide|BenchmarkWheelBurst' -benchtime=100x -timeout 30s`
  — PASS. Wide View: 3.014 ms/op, 1,284,468 B/op; wheel-burst routing: 0.138 ms/op.
  These are local microbenchmarks, not GUI terminal latency claims.
- `python3 scripts/scroll_benchmark.py --events 2000 --output /private/tmp/tui-rounded-scroll-benchmark`
  — PASS: 2,000 wheel events plus subsequent input/detach completed in 0.196 seconds
  to alternate-screen exit; 0.319 seconds total child CPU, theme persisted and
  server survived detach. This uses synthetic activity in an OS PTY.
- `git diff --check` — PASS. No Git pull, push, staging or commit was performed.

## Visual evidence

Generated 44 deterministic Go View captures with `TestNavigationCaptures`,
`TestThreadIndicatorCaptures`, `TestPolishedViewCaptures` and
`TestQuestionReviewCaptures`. Retained and reviewed the seven views below, rendered
using `scripts/render-capture.py` and JetBrainsMono Nerd Font Mono. These are raster
views of actual render output, not GUI terminal screenshots. ANSI and SVG sources
are compressed beside each PNG.

| View | Capture |
| --- | --- |
| Dark cards, pill tabs and question controls | [160×50 dark](go-thread-cards-captures/160x50-lightfalse-tabs.png) |
| Light equivalent | [160×50 light](go-thread-cards-captures/160x50-lighttrue-tabs.png) |
| Working, attention, error, finished and Closed cards | [Status cards](go-thread-cards-captures/160x50-lightfalse-thread-status-0.png) |
| Short navigation viewport and long titles | [110×22 scrolling](go-thread-cards-captures/110x22-lightfalse-navigation-scroll.png) |
| 16-color outline/parenthesis fallback | [ANSI fallback](go-thread-cards-captures/160x50-lightfalse-navigation-ansi.png) |
| Minimum layout and question overflow | [48×22 narrow](go-thread-cards-captures/48x22-lightfalse-navigation-narrow.png) |
| Opened-surface overflow menu | [Surface menu](go-thread-cards-captures/160x50-lightfalse-menu.png) |

The capture font lacks some CJK characters, visible as missing glyphs in the
long-title sample; Go cell-width and grapheme handling are separately exercised.
No new Ghostty, iTerm2, Omarchy, SSH or tmux GUI compatibility claim is made.
Provider behavior and embedded terminal activity remain fixtures.

## Run and next slice

The binary is rebuilt. Detach the old client with Ctrl+Q, then run:

```sh
cd /Users/muschterm/Developer/git/github.com/muschterm/tui/apps/go
./bin/tui-go
```

No server restart is required for this presentation change. The next review can
evaluate sidebar card density and rounded controls in the user's terminal before
continuing the already-documented integration slices.
