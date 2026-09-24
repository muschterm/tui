---
status: accepted (prototype; live kitty/Ghostty check pending)
---

# Place kitty graphics through Unicode placeholders

The Go TUI renders through Bubble Tea v2's cell renderer, which diffs a cell
buffer and owns cursor movement. Image previews (attachment viewer, draft
thumbnails) need pixels that stay aligned with that buffer across scrolling,
resize, pane changes and redraws.

## Decision

Use kitty graphics **Unicode placeholder** placements: transmit PNG data with
`a=t` via `tea.Raw`, create a virtual placement (`a=p,U=1,c,r`), and render the
image area as ordinary cells of U+10EEEE plus row/column diacritics whose
256-colour foreground encodes a client-owned image id (1–255). Do not use direct
placements.

Enable graphics only after an XTVERSION-gated `a=q` query reply (DA1 terminator
plus timeout), with a colour profile of at least ANSI256 and no `NO_COLOR`.
Otherwise draw a styled metadata block of identical geometry. Delete owned
images with `a=d,d=I,i=ID` on close/removal, before suspend and on exit.

## Consequences

- Placeholders are text to the renderer: the pinned `ansi`, `uniseg`, `lipgloss`
  and `ultraviolet` measure each as one cell (scratch probe,
  [research note](../research/go-image-clipboard-2026-09-24.md)), so layout,
  clipping and diffing need no graphics special cases.
- They pass through tmux as text; only transmits need passthrough.
- Colour downsampling would destroy the id, so graphics are off below ANSI256.
- Terminals without placeholder support (foot, likely WezTerm, older kitty) get
  the fallback even when they support other image protocols. Sixel is not
  pursued.
- Live rendering in kitty/Ghostty has not been verified in this project. The
  user asked for image previews on 2026-09-24, so this is accepted for the
  prototype behind capability detection; revisit if a recorded terminal check
  fails.
