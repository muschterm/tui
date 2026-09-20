# Notification and question review captures

These are deterministic raster renders of actual Go View output, not native
terminal screenshots. The installed JetBrainsMono Nerd Font Mono font is rendered
at 15 px in 10×20 px cells. No font is bundled.

- [Radio choices, dark](160x50-lightfalse-question-radio.png)
- [Radio choices, light](160x50-lighttrue-question-light-radio.png)
- [Checkboxes with Other](160x50-lightfalse-question-multi-other.png)
- [48×22 layout](48x22-lightfalse-question-narrow.png)

Matching SVG and compressed ANSI source are retained. From apps/go:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-question-review go test ./internal/tui -run TestQuestionReview -count=1
python3 scripts/render-capture.py /tmp/tui-question-review ../../docs/research/go-question-captures \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --name 160x50-lightfalse-question-radio --name 160x50-lighttrue-question-light-radio \
  --name 160x50-lightfalse-question-multi-other --name 48x22-lightfalse-question-narrow
```

See the [validation report](../go-question-review-2026-09-20.md) for interactive
input checks and scope limitations.
