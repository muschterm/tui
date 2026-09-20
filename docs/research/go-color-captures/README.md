# Color compatibility captures

Six deterministic 100×34 Go View frames: dark and light themes in TrueColor,
ANSI256 and ANSI. The application uses its real question/composer layout and
fixture activity. These are static ANSI-to-PNG renders, **not terminal screenshots**.
Colors use the capture renderer's fixed xterm-style palette; a user's configurable
16-color palette may look different. JetBrains Mono Nerd Font Mono Regular is
rendered at 15 px on a 10×20 cell grid. No terminal font-size behavior is implied.

The compressed `.ansi.gz` files preserve styled frames; `.svg.gz` files preserve
the corresponding vector output. Original streams and temporary files remain
outside the repository. The JSON reports record a native binary under an OS PTY;
color replies are synthetic and do not establish Ghostty/iTerm2/SSH/tmux coverage.

Reproduce from `apps/go`:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-color-captures go test ./internal/tui -run TestColorReviewCaptures
python3 scripts/render-capture.py /tmp/tui-color-captures /tmp/tui-color-rendered --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf
python3 scripts/pty_colors.py --artifacts /tmp/tui-color-pty
```

The renderer requires Pillow. See [the compatibility review](../go-colors-2026-09-20.md)
for executed checks, source evidence and remaining reply-fragment limitations.
