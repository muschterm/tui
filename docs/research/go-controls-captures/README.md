# Controls review captures

Generated from actual Go `View` output on 2026-09-19. These are deterministic
rasterized renders, **not native terminal screenshots**. Font: installed
JetBrainsMono Nerd Font Mono (`JetBrainsMono NFM`), 15 px, fixed 10×20 px cells.
No font file is bundled. SVG display also requires an available matching font;
PNG preserves the inspected raster. Matching `.ansi.gz` retains source output.

- [Dark tabs](160x50-lightfalse-tabs.png)
- [Light tabs](160x50-lighttrue-tabs.png)
- [Eight-row composer and scrollbar](160x50-lightfalse-composer.png)
- [Hidden-tab overflow](160x50-lightfalse-overflow.png)
- [One-row-per-surface menu](160x50-lightfalse-menu.png)
- [Narrow composer](60x24-lightfalse-narrow.png)

Regenerate from `apps/go`, passing your own installed Nerd Font path:

```sh
TUI_GO_CAPTURE_DIR=/tmp/tui-polish-captures go test ./internal/tui -run TestPolishedViewCaptures -count=1
python3 scripts/render-capture.py /tmp/tui-polish-captures ../../docs/research/go-controls-captures \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --name 160x50-lightfalse-tabs --name 160x50-lighttrue-tabs \
  --name 160x50-lightfalse-composer --name 160x50-lightfalse-overflow \
  --name 160x50-lightfalse-menu --name 60x24-lightfalse-narrow
```

The capture utility uses Pillow. This evidence covers cell layout and selected
glyph rendering; [interaction and capability limits](../go-controls-2026-09-19.md)
remain distinct.
