---
status: accepted
---

# Use cell-native variants for core component state

The Go visual review exposed a mismatch between smooth rounded background shapes
and the current text renderer: backgrounds color whole terminal cells, while
box-drawing glyphs supply a rounded line. Pill caps, inset rectangular fills and
stepped block corners produced different silhouettes. Background colors themselves
work; the missing feature is a smooth clipping mask for the filled shape. These
observations do not establish an intrinsic limitation of every terminal or graphics
renderer. See the [recorded experiments](../research/go-prompt-corners-2026-09-20.md).

We adopt a shared component contract with rounded-outline, square-outline and
square-fill variants. Outlined components express rest/hover/selection through
border shades over stable backgrounds; compact square-filled controls use fill
shades. Selection and keyboard focus also have text cues. Core interaction must
not require a graphics-backed rounded mask.
Selection and focus stay distinct; semantic execution/status colors remain separate.
The [component construction rule](../design/components.md) defines behavior and
verification; exact shades and dimensions remain adjustable implementation details.

This chooses predictable text geometry and a common fallback across three native
reference apps over smooth rounded filled control silhouettes. Compact rectangular
backgrounds remain a first-class option because they fit terminal cells directly.
Graphics-backed component
masks would add cell-to-pixel geometry, placement, clipping, overlay and cleanup
coordination to every affected component. That dependency would become expensive
to remove after adoption across the component set and language implementations.
Conversely, the accepted boundary gives up exact web-style filled corners. Existing
negotiated image previews and identity assets remain in scope; this is not a ban
on graphics or ordinary background colors.

The user accepted this boundary and the component rule on **2026-09-20**:
rounded outlines with stable interior backgrounds for prompt, thread, request and
dialog containers; single-row square fills for question/surface tabs and compact
actions; square outlines remain available. Rest is neutral, hover stronger neutral,
selection accent plus bold, and keyboard focus independently underlined. Selected
state survives pointer movement to neighboring controls, and semantic status
indicators retain their meaning. Implementation validation is recorded separately;
acceptance alone establishes no terminal compatibility claim.
