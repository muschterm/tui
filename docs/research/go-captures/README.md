# Deterministic Go View captures

Generated 2026-09-19 from the first Go slice's actual deterministic ANSI `View` output. **These images are rasterized application output, not native terminal screenshots.** They establish visible cell layout and color for the captured state; they do not establish keyboard, mouse, terminal negotiation, clipboard, SSH/tmux or real provider behavior. The approved visual references remain separate and unchanged.

- [120×40 dark](120x40-lightfalse-maxfalse.png)
- [120×40 light](120x40-lighttrue-maxfalse.png)
- [160×50 dark, maximized Agents](160x50-lightfalse-maxtrue.png)

Matching SVG files allow inspection of individual cells and text. Matching `.ansi.gz` files retain the exact source output. The renderer uses Menlo at 15 px on fixed 10×20 px cells. PNG and SVG share the same parsed cells; PNG uses Pillow/FreeType, while SVG font rendering depends on the viewer. It supports static SGR-colored grids, not a general VT terminal emulator. Bold/italic/underline SGR attributes do not alter the current review font. Complex grapheme shaping and font fallback are outside this small capture utility's verification scope.

Regenerate the output after regenerating deterministic test captures:

```sh
python3 apps/go/scripts/render-capture.py /tmp/tui-go-captures docs/research/go-captures \
  --name 120x40-lightfalse-maxfalse \
  --name 120x40-lighttrue-maxfalse \
  --name 160x50-lightfalse-maxtrue
```

Python requires Pillow. On the development host it is available in the bundled runtime Python; the default font is `/System/Library/Fonts/Menlo.ttc`, overridable with `--font`. The script rejects unknown escape/control sequences and cells extending outside filename-declared dimensions. This conversion never sends captured escape sequences to the host terminal or rasterizes the application's live UI.
