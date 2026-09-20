# Activity and composer review captures

Generated from actual Go `View` output. These are deterministic raster renders,
not native terminal screenshots. Installed font: JetBrainsMono Nerd Font Mono
(`JetBrainsMono NFM`), 15 px in fixed 10×20 px cells. No font is bundled.

- [Working, dark](160x50-lightfalse-working.png)
- [Working, light](160x50-lighttrue-working.png)
- [Completed summaries, hovered Agents close](160x50-lightfalse-finished.png)
- [Maximized empty chooser](160x50-lightfalse-empty-max.png)
- [Narrow layout](48x22-lightfalse-narrow.png)

Each capture retains matching SVG and compressed source ANSI. Regenerate from
`apps/go` using an installed font:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-activity-captures go test ./internal/tui -run TestActivityReviewCaptures -count=1
python3 scripts/render-capture.py /tmp/tui-activity-captures ../../docs/research/go-activity-captures \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --name 160x50-lightfalse-working --name 160x50-lighttrue-working \
  --name 160x50-lightfalse-finished --name 160x50-lightfalse-empty-max \
  --name 48x22-lightfalse-narrow
```

Animation, input and lifecycle evidence is recorded separately in the
[validation report](../go-activity-review-2026-09-19.md).
