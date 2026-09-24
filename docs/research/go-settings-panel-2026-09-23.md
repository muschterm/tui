# Go settings panel restyle — 2026-09-23

The user asked for the Settings content area to look like a control panel, in the
spirit of an Omarchy Quickshell WiFi panel, using portable terminal-cell
techniques. The left category sidebar is unchanged.

## What changed

Implementation: `apps/go/internal/tui/sidebar_settings.go` (row model) and
`apps/go/internal/tui/settings_layout.go` (painting and hit testing).

- **Sections.** Headings are UPPERCASE, bold and muted. Sections are separated by
  a blank row, a full-width `─` rule in the line color (`-` with ASCII symbols) and
  a blank row. The page title is unchanged.
- **Segmented choices** for app Workspace default (Current checkout | Worktree),
  project Workspace default (App default | Current checkout | Worktree), Theme
  (Dark | Light) and Symbols (Nerd Font | ASCII). Equal square-fill segments span
  the content width with a two-cell gap. Each segment has its own key
  (`sidebar-setting:<setting>:<value>`), hit rectangle and Tab stop, and uses the
  components.md square-fill states; the focus mark takes the gap cell before the
  segment on its label row. With 256 colors or true color and Nerd Font symbols,
  each segment is a three-row band: a `▄` row and a `▀` row in the segment fill over
  the canvas, so it reads as a two-cell-tall button. The hit rectangle covers the
  visible part of the band. With 16 colors, no color or ASCII symbols, a segment is
  one row with reserved `[ Label ]` end cells. When any label cannot fit (for
  example, three workspace choices at 47 columns), the previous single button and
  menu return. The selected segment's action is a no-op, so reselecting the current
  value writes nothing. Other segments dispatch the same `app-workspace-set` and
  `project-set-workspace` commands, bound to the revision current at render time.
  New `theme-set` and `icons-set` actions apply only on change through the existing
  toggles, so project scope still refuses them.
- **Toggle switch** for Continue threads after restart: a full-row control with its
  label at the left, and the On/Off word and a six-cell switch at the right. Off is
  a neutral track with a muted two-cell knob at the left; On is an accent track with
  a bright knob at the right. With no color, it shows `[o   ]` or `[===o]`. The row
  uses square-fill hover and focus states and dispatches `restart-toggle`.
- **Label/value rows** for About (Name, Version, Implementation, server Protocol)
  and Keybindings (action at the left, chord at the right, grouped as Composer,
  Navigation and Window).
- **Buttons** (starting folder, project name and icon, agent defaults, reset and
  Remove project…, still red) are inset by one cell and use the same banded fill
  when bands are available.
- `project-set-*` now also checks the `project-settings` capability, because
  segments reach it without opening the menu first.

## Checks

```sh
cd apps/go
make check
python3 scripts/pty_sidebar_settings.py --binary bin/tui-go --artifacts <scratch>
TUI_GO_CAPTURE_DIR=<scratch> go test ./internal/tui -run TestSettingsPanelCaptures
python3 scripts/render-capture.py <scratch> docs/research/go-settings-panel-captures \
  --font /usr/share/fonts/TTF/JetBrainsMonoNerdFont-Regular.ttf
```

- `make check` (fmt-check, vet, staticcheck, race tests, build) passed.
- New tests in `settings_panel_test.go` cover separate banded segment hits (including
  a click on a band edge), actions, values and revisions; no-op reselection; hover
  never dispatching; Tab reaching every segment and the toggle; Theme and Symbols
  setting a value once; the 47-column menu fallback; single-row bracket fallbacks
  for ANSI, NO_COLOR/ASCII and ASCII symbols; toggle state words and the accent
  track; and stable row byte lengths after repeated hover/focus painting.
- The OS-PTY harness passed 31 checks
  ([report](go-settings-panel-captures/settings-pty-report.json)). Its workspace
  steps now click segments directly and also check the inherited
  "Using app default" note.

## Captures

Deterministic Go View renders (not terminal screenshots):
`160x30-light{false,true}-settings-{general,appearance,about,keybindings,project-general}`
and `47x22-lightfalse-settings-general` in
[go-settings-panel-captures](go-settings-panel-captures/). `render-capture.py`
now draws block elements and light lines (`█▀▄▌▐▔▁▏▕─│`) as cell rectangles, as
foot does, instead of font glyphs; the earlier renders showed dotted band edges.

Real terminal, 2026-09-23: foot 1.28.0 on Hyprland (local, no SSH/tmux), JetBrains
Mono Nerd Font, isolated `TUI_GO_HOME`, driven by `wtype` keys and captured with
`grim` on the probe window only. [General](go-settings-panel-captures/foot/foot-1.28-general.png)
shows solid half-block bands and rules;
[segment focus](go-settings-panel-captures/foot/foot-1.28-segment-focus.png) shows
the `•` mark in the gap before the selected segment after one Tab;
[toggle](go-settings-panel-captures/foot/foot-1.28-toggle-on-focus.png) shows Tab
to the restart row and Enter switching it On, confirmed by
`tui-go snapshot` (`ContinueAfterRestart: true`).

## Limitations

- Real-terminal evidence covers foot only, dark theme, true color. Light theme,
  limited-color fallbacks, kitty, Ghostty, iTerm2, SSH and tmux are NOT RUN.
- `render-capture.py` now draws `─`/`│` as 1-px geometry but corner glyphs from
  the font, so re-rendered captures of outlined surfaces may show joins that differ
  from real terminals. Only this note's captures were re-rendered.
- There are no captures of the limited-color fallbacks. Tests assert that behavior.
- Fonts that draw block elements as glyphs rather than terminal-drawn geometry may
  show gaps at band edges; not checked.
- Hover fill on the toggle row and the 16-color segment fallback follow
  components.md. The banded variant is recorded in components.md as a 2026-09-23
  user-directed refinement.
