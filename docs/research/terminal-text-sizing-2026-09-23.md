# Terminal text sizing and sub-cell rendering — 2026-09-23

Research only; no decision. Question: how close can the TUI get to a Qt/QML
panel (mixed font sizes, letter-spaced caps, padded buttons, toggle switches)
in this user's terminal, and can the Go app's Bubble Tea v2 renderer carry
the enhancements? Extends [terminal capabilities](terminal-capabilities.md)
and [visual enhancements](visual-enhancements.md) (kitty graphics).

## Environment (verified)

- Omarchy, Hyprland (Lua config), kernel `7.2.5-3-omarchy`.
- `$TERMINAL=xdg-terminal-exec`; no `~/.config/xdg-terminals.list`, and the
  only terminal desktop entries are foot's, so the default resolves to foot.
- Installed terminals: **`foot 1.28.0`** only (checked `foot kitty ghostty
  alacritty wezterm konsole xterm` on PATH). Ghostty/kitty are not installed,
  so every kitty/Ghostty claim below remains documented-only.

## Probes

Scripts: [`go-feasibility-probes/text-sizing/`](go-feasibility-probes/text-sizing/).

1. `foot -T tsprobe -e bash probe-replies.sh <out>` — raw mode, one query per
   step, reply captured with a 0.5 s read. Replies (`%q`-escaped):

   | Query | Reply | Reading |
   | --- | --- | --- |
   | DA1 `CSI c` | `ESC[?62;4;22;28;52c` | `4` = sixel advertised |
   | XTVERSION `CSI >0q` | `ESC P>|foot(1.28.0) ESC\` | identifies foot |
   | kitty graphics `a=q` + DA1 | only the DA1 reply | kitty graphics not supported |
   | DECRQM `?2026` | `ESC[?2026;2$y` | synchronized output supported (reset) |
   | OSC 66 `w=2; ` between CPRs | `ESC[1;1R ESC[1;3R` | **`w=` supported**: cursor advanced 2 |
   | OSC 66 `s=2; ` between CPRs | one CPR; stderr `OSC-66: unsupported: 's' parameter, ignoring` | **scale `s` not supported** |
   | DECDWL `ESC#6` then `X` between CPRs | column advanced 1 | DECDWL not honoured |

2. `foot -T tsvisual -e bash visual.sh 5`; geometry from
   `hyprctl clients -j` (title `tsvisual`), captured with
   `grim -g "<x>,<y> <w>x480"`. Result:
   [foot-visual.png](terminal-text-sizing-captures/foot-visual.png).
   - OSC 66 `s=2` heading and `n=1:d=2` line: rendered at normal size (payload
     text still shown).
   - DECDHL top/bottom and DECDWL: rendered as ordinary single-size text.
   - Half-block padded button (`▄` row, filled label row, `▀` row, fg = button
     colour): renders as a solid, vertically padded button; clean edges.
   - Eighth-block border (`▁▔▕▏` hugging a fill): renders a thin-bordered
     box; the probe used `▕` on the left, which leaves a visible notch — use
     `▏`/`▕` on the correct sides and match fill colour exactly.
   - 4-cell background-fill toggle (track + light knob cell): reads clearly as
     on/off; knob is square (no rounded ends).
   - Sextants/quadrants (Legacy Computing): render as crisp built-in blocks.
   - Letter-spaced dim caps: fine; costs 2× width.
   - Sixel (DCS `q`, 24×18 red): renders (small red block above
     "sixel above"). Text written onto its cells later erases it.

## Bubble Tea v2 carriage (verified)

Pinned versions from `apps/go/go.mod`, `GOTOOLCHAIN=go1.27.1`:
`bubbletea/v2 v2.0.9`, `lipgloss/v2 v2.0.6`, `ultraviolet
v0.0.0-20260811164956-006e29f97886`. Probe:
[`text-sizing/bubbletea-carriage/`](go-feasibility-probes/text-sizing/bubbletea-carriage/)
(own `go.mod` copied from the app's requires; `go run .`). The view contains
`ESC]66;s=2;Wi-Fi BEL after`, `ESC]66;w=2;AB BEL|`, `ESC#3Heading`, then a
neighbour row that changes each frame. Output to a buffer at 40×6:

```
lipgloss.Width(osc66+" after") = 6
lipgloss.Width(osc66w+"|") = 1
lipgloss.Width(dhlTop) = 7
"...\r\x1b[J after\n|\nHeading\nneighbour frame 0\x1b[D1\x1b[D2\x1b[D3\n..."
```

- **Stripped.** OSC 66 (with its payload) and `ESC#3` never reach the
  terminal. Cause: ultraviolet `styled.go` `printString` only interprets SGR
  and OSC 8 (`styled.go:198`, `TODO: Handle cursor movement and other
  sequences` at `:193`); other sequences are appended to the pending cell
  (`:236`) and then overwritten when the next printable grapheme sets
  `cell.Content = string(seq)` (`:150`).
- **Mis-measured.** lipgloss counts OSC 66 as zero width (`w=2;AB` → 1 for
  the `|` alone), so layout would under-reserve the cells the terminal uses.
- **No multi-row/multi-cell model.** `uv.Cell` has `Content`, `Style`,
  `Link`, `Width` only (`cell.go:15-29`): no height, scale or line attribute.
  The diff renderer rewrites individual cells (`\x1b[D1…` above), so any
  sized glyph spanning rows would be clobbered by neighbour-row diffs.
- **Escape hatch.** `tea.Raw` (`bubbletea raw.go:33`, executed at
  `tea.go:866`) writes bytes outside the cell buffer. It could place an
  OSC 66 run or sixel image after a frame, but the renderer does not know those
  cells are occupied, so the next diff can overwrite them; `tea.Println`
  (`renderer.go:70`) only inserts above the inline view.

## Documented vs verified

- Verified here: every table row and screenshot above, foot 1.28.0 only.
- Documented only (not installed): kitty implements OSC 66 including `s`/`n`/`d`
  (kitty text-sizing protocol spec; kitty ≥ 0.40) and the kitty graphics
  protocol; Ghostty implements kitty graphics. OSC 66 support in Ghostty is
  not established here.
- Not tested: tmux/SSH passthrough, other font configurations.

## Recommendation tiers (proposal, not accepted)

**(a) Portable cell techniques, usable now in the current renderer.** Half-block
padded buttons, eighth-block hairline borders hugging fills, background-fill
toggle switches, Legacy Computing blocks for small glyph art, letter-spaced
dim caps for section headers, bold/colour hierarchy for "size". All are plain
UTF-8 + SGR, pass through ultraviolet, and rendered correctly in foot. They
depend on the font's block-element coverage (foot draws them natively); keep
the existing plain fallback for `NO_COLOR`/16-colour.

**(b) Negotiated enhancements.** OSC 66 `s`/`n:d` (kitty only, documented),
OSC 66 `w=` (foot verified), sixel (foot verified), kitty graphics
(kitty/Ghostty, documented). To use any of them the renderer needs: detection
by query (as in `probe-replies.sh`, never by `$TERM`); a cell model with
reserved/occupied spans (e.g. placeholder cells owned by an overlay with
`Width`×height) so diffs skip them and layout measures them; emission of the
raw sequence after the frame inside synchronized output (2026), re-emitted
whenever any covered cell is redrawn or on resize; and fallback to tier (a).
In the current stack that means either an upstream ultraviolet change or an
application-side overlay pass using `tea.Raw` with invalidation — a
consequential design needing an ADR before implementation.

**(c) Infeasible.** Proportional fonts, sub-cell letter spacing and true
rounded pill toggles in cells; DECDHL/DECDWL in foot (ignored); OSC 66 scaling
in foot; any of (b) passing through the current Bubble Tea renderer
unmodified; matching Qt anti-aliased geometry without images.
