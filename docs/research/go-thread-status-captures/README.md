# Thread status review captures

Actual Go View output rasterized with the installed JetBrainsMono Nerd Font Mono,
15 px font in 10×20 px cells. These are deterministic synthetic status examples,
not native terminal screenshots or evidence of an actual API failure. Source ANSI
(compressed), SVG and PNG are retained; no font is bundled.

- [Dark, dim pulse](160x50-lightfalse-thread-status-0.png)
- [Dark, bright pulse](160x50-lightfalse-thread-status-6.png)
- [Light, dim pulse](160x50-lighttrue-thread-status-0.png)
- [Light, bright pulse](160x50-lighttrue-thread-status-6.png)

The selected finished row is hovered to show its Close check. The closed row
retains its green circle until hovered. Yellow denotes attention and red denotes
a reported error. Vertical ellipses remain independently selectable.

From `apps/go`:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-thread-status go test ./internal/tui -run TestThreadIndicatorCaptures -count=1
python3 scripts/render-capture.py /tmp/tui-thread-status ../../docs/research/go-thread-status-captures \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf
```

[Navigation PTY report](navigation-pty-report.json) verifies the 18 exercised
lifecycle/navigation paths; [wheel report](scroll-report.json) records one
500-event sample. The state/animation behavior is covered by Go tests and static
visual review, distinct from these native PTY interaction checks.
