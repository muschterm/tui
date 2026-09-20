# Project and thread navigation captures

Six PNGs are deterministic raster renders of actual Go View output, using the
installed JetBrainsMono Nerd Font Mono at 15 px in 10×20 px cells. They are not
native terminal screenshots. Matching SVG and compressed ANSI source are retained;
no font is bundled.

- [Dark navigation](160x50-lightfalse-navigation-dark.png)
- [Light navigation](160x50-lighttrue-navigation-light.png)
- [Project picker](160x50-lightfalse-navigation-filter.png)
- [Closed hover action](160x50-lightfalse-navigation-closed.png)
- [Permanent-delete confirmation](160x50-lightfalse-navigation-delete.png)
- [48×22 layout](48x22-lightfalse-navigation-narrow.png)

From `apps/go`:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-navigation-pty go test ./internal/tui -run TestNavigationCaptures -count=1
python3 scripts/render-capture.py /tmp/tui-navigation-pty ../../docs/research/go-navigation-captures \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf
```

The numbered `.screen.txt` files reconstruct actual cursor-addressed OS-PTY
output with the test harness's limited ANSI reader. They show folder validation,
project name/path search, Close, Delete and the resulting second-client/empty
views. They establish exercised input paths, not Ghostty/iTerm2 screenshot or
full terminal-emulator parity. Temporary folder paths are test data.

The [18-check navigation report](navigation-pty-report.json),
[28-check regression report](regression-pty-report.json) and
[wheel sample](scroll-report.json) record executed isolated runs. See the
[review](../go-navigation-review-2026-09-20.md) for interpretation and limitations.
