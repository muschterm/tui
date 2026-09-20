# Rich terminal visuals

Source-checked **2026-09-19**. Runtime rendering, installed-version compatibility and framework integration are **NOT RUN**. The user's requirement is a richer default: more color, recognizable icons, image previews, and clear user/agent/tool separation, followed by a correction to reduce clutter and remove invented avatars. Capability fallbacks preserve that hierarchy rather than becoming the design target.

## Images and icons are different choices

**Revised design recommendation:** use small role accents, author labels, spacing and compact operational groups with full inspection. Use a coherent SVG source family for controls and verified official assets for agent identity; do not invent avatars or vendor-like symbols. Small navigation/status icons can also be validated font glyphs. Kitty graphics is not required for every icon. Keep measured cell geometry: glyph availability and width depend on the selected font and terminal. Kitty's [font FAQ](https://sw.kovidgoyal.net/kitty/faq/#some-special-symbols-are-rendered-small-truncated-in-kitty) documents private-use symbol sizing issues; arbitrary icon fonts need not have identical metrics.

## SVG sources and agent identity

The [kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/) accepts RGB, RGBA and PNG data, not raw SVG. **Recommendation:** rasterize vetted SVG artwork to transparent PNG/RGBA at the actual cell-pixel size, cache by source revision, permitted theme variant and dimensions, then reuse transmitted image data across placements. A future web frontend could use the same vetted SVG sources directly. This is a design path, not a renderer already integrated with Bubble Tea, Ratatui or Ink.

| Identity | Official starting point | Scope of verification |
| --- | --- | --- |
| OpenAI / ChatGPT / Codex | [OpenAI brand resources](https://openai.com/brand/) | Official artwork and usage guidance; do not assume a company mark is a verified Codex-specific product mark. |
| Claude / Anthropic | [Anthropic Brandfolder](https://brandfolder.com/anthropic/) | Official collection entry point; individual Claude SVG availability was not checked. |
| GitHub Copilot | [Primer Copilot Octicon](https://primer.style/octicons/icon/copilot-16/) | Offers SVG and several pixel-size variants; identifies GitHub Copilot, not Microsoft Copilot. |
| Grok | [xAI brand guidelines](https://x.ai/legal/brand-guidelines) | Official downloadable-logo guidance; verify the selected product mark and supplied variants. |

These are asset sources, not declarations that every mark has been downloaded, licensed for all uses, or packaged here. Maintain an asset registry with product identity, source, version and usage terms. Keep connection/product, provider, model and artwork identities separate. A model change must not accidentally replace the agent logo. Unknown agents use their text name; the default user label needs no avatar. User-supplied avatars and optional custom application icons are separate from official provider artwork.

Package vetted local assets for offline use. Do not fetch arbitrary SVGs in the render loop or allow SVG external resources/scripts to become network or code execution. Rasterization and cell hit testing are independent: an icon does not itself supply a mouse button. Brand asset colors/proportions follow the supplied variants; theme adaptation is not permission to redraw or recolor a mark.

## Documented image paths

[Ghostty explicitly supports kitty graphics](https://ghostty.org/docs/features). This establishes a viable rich-image target, not proof of every protocol extension or our renderer's behavior in a particular release.

Current [iTerm2 image documentation](https://iterm2.com/documentation-images.html) explicitly supports **both kitty graphics and its own inline-image protocol**. Do not label iTerm2 categorically incompatible with kitty images. Its separate OSC 1337 protocol accepts base64 image data, cell/pixel/percentage dimensions and aspect-ratio control. Use inline display explicitly: omitting that option instead invokes file download. Feature Reporting is its documented detection route. Multipart transfer, introduced in iTerm2 3.5, is documented for tmux integration mode; that does not establish equivalent behavior in every ordinary tmux session.

**Backend recommendation:** prefer tested kitty graphics capabilities; use a separately verified iTerm2 inline backend when appropriate. Do not assume inline images offer the same placement lifecycle as kitty. If neither works, retain colored role areas, glyphs/initials, image labels and inspectable metadata. A fallback must still look intentionally designed.

## Placement, scrolling and transport

The [kitty graphics specification](https://sw.kovidgoyal.net/kitty/graphics-protocol/) supports reusable image data with identified placements, source cropping, cell-sized display rectangles, pixel offsets, stacking and deletion. It defines a graphics query rather than relying solely on terminal names. Remote clients must send chunked image data through the stream when they cannot share filesystem or memory with the terminal.

Its Unicode placeholders let images follow text movement through a host such as tmux, provided graphics data can reach the terminal. Placeholder cells control visibility and movement; shortened coordinate encoding has horizontal-scroll/overlap limitations. These mechanisms do not supply an application's pane hierarchy or clipping policy.

**Renderer implications:** maintain visible image placements alongside the cell frame. Recompute cropping on pane resize, remove hidden placements, and ensure popovers, scrolling transcripts and pane switches cannot leave stale avatars over unrelated content. Reserve image geometry before painting; do not let terminal cursor movement accidentally alter the layout. Coordinate IDs and cleanup with embedded-terminal rendering rather than globally deleting another surface's images.

The [tmux manual](https://man.openbsd.org/tmux.1) documents escape passthrough: `on` permits visible panes, while `all` also permits invisible panes. Passthrough enables transport, not a guarantee of correct clipping, redraw or placement after switching panes. Kitty's [icat documentation](https://sw.kovidgoyal.net/kitty/kittens/icat/) confirms SSH use while warning that multiplexers may impede graphics. For a remotely running TUI, transmit image bytes rather than assuming remote paths exist locally; SSH forwarding to a local TUI is a different rendering topology.

## Verification before claiming support

All cases are **NOT RUN**: Ghostty and iTerm2 directly, over SSH, through ordinary tmux, and iTerm2 tmux integration separately. Record versions, detection results and selected backend. Exercise avatar reuse, rapid transcript scroll, horizontal clipping, narrow panes, resize, overlays, light/dark contrast, alternate-screen exit, reconnect, absent fonts and unavailable image capability. Verify bounded image memory/transfers and that fallback preserves user/agent/tool distinction. Rich visuals remain required; minimum versions and exact extension support must follow these checks.
