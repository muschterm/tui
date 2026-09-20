# Language stack research

Date: 2026-09-19. Evidence: upstream documentation, repositories, and published releases; **not runtime verified in this project**. Version observations describe this date. Revalidate compatibility and pin toolchains and dependencies when implementation begins; do not depend on rolling `latest` documentation.

The accepted deliverable is a shared behavior specification with three idiomatic reference apps. macOS and Linux are required; Windows Terminal is best effort. Go/Charm and Rust/Ratatui are selected; the user subsequently chose React + public Ink for Bun, with Bun.Terminal for interactive subprocesses. The earlier OpenTUI recommendation is retained below as comparison research, not the implementation direction. None of these choices establishes runtime or terminal compatibility.

## Selected directions and remaining work

| Language | Recommended direction | Benefit | Work remaining |
| --- | --- | --- | --- |
| Go | Bubble Tea v2, Bubbles v2, Lip Gloss v2 | Direction established by the user; typed update/view/effect composition | Assemble layout, focus, hit testing, editor/Git services, and terminal capability behavior. |
| Rust | Ratatui + Crossterm | Explicit state and rendering control for a custom workbench | Assemble focus routing, hit testing, splitters, editor integration, and effects. |
| Bun | TypeScript + React + public Ink; Bun.Terminal for PTYs | Public React/Ink lineage matching the user's Claude Code preference; native Bun subprocess terminal I/O | Validate Bun runtime/packaging; add mouse routing, splitters, graphics, editor/diff surfaces, and embedded VT rendering. |

All three apps should implement identical observable commands and interaction scenarios without copying one language's internal architecture into the others. Widgets do not supply file-save safety, Git semantics, turn attribution, cancellation, or provider integration.

## Go

The source-checked candidate pins are Bubble Tea 2.0.9, Bubbles 2.2.1, and Lip Gloss 2.0.6 with the `charm.land/.../v2` import paths and Go 1.25 or newer. [Terminal research](terminal-capabilities.md) records primary release/manifest sources and the v2 mouse and keyboard APIs. These versions have not been installed or validated together in this project. The design should use framework-managed terminal I/O, explicit update messages, and cancellable effects; components still need a shared focus/input/layout contract.

## Rust

[Ratatui](https://github.com/ratatui/ratatui) is the recommended foundation. Its observed release is [0.30.2](https://github.com/ratatui/ratatui/releases); version 0.30 modularized core, widgets, and backends. [Crossterm is the default backend](https://ratatui.rs/concepts/backends/), with termion, termwiz, and termina alternatives. The application owns its event loop and state rather than receiving a complete interaction framework. This supports a deliberate command/state/effect design, at the cost of additional interaction code. [Architecture comparison](https://ratatui.rs/faq/).

[ratatui-textarea](https://github.com/ratatui/ratatui-textarea), an independently maintained fork of tui-textarea, supplies multiline editing, undo/redo, wrapping, regex search, selection, and mouse scrolling. Its split and modal-editor examples are useful starting points. It remains an editor widget: persistence, external-change detection, encoding policy, and the desired editing depth belong to the app. [tui-markdown 0.3.9](https://docs.rs/crate/tui-markdown/latest), released July 23, 2026, converts Markdown to Ratatui text with optional syntax highlighting, but describes itself as experimental. Validate fidelity before selecting it.

[Cursive](https://github.com/gyscos/cursive) offers focused-view event handling and links movable-divider and multiplexer views. [tui-realm](https://github.com/veeso/tui-realm) adds React/Elm-inspired components over Ratatui. [iocraft](https://github.com/ccbrown/iocraft) offers declarative hooks, Taffy flexbox, and fullscreen rendering. These are credible alternatives; this research did not establish their editor, mouse, and enhanced-keyboard parity for the proposed workbench.

Crossterm exposes [Kitty keyboard enhancement flags](https://docs.rs/crossterm/latest/crossterm/event/struct.KeyboardEnhancementFlags.html). That is protocol support evidence, not proof of every key combination through tmux. Rust offers target-specific native executables without a JavaScript runtime; cross-target builds, linked dependencies, packaging, and terminal cleanup still require release checks.

## Bun

The selected direction is **React + public Ink on Bun**, with `Bun.Terminal` for interactive subprocesses. [Claude Code UI research](claude-code-ui.md) distinguishes the public React/Ink lineage from Claude Code's customized renderer; it also records Ink's documented Node runtime and the need to validate Bun explicitly. [Bun terminal research](bun-terminal.md) covers native PTY process I/O, which still needs an emulator and pane renderer. These choices do not imply that stock Ink inherits Claude Code's mouse, rendering, or editing features.

### Earlier OpenTUI comparison, not selected

[OpenTUI](https://github.com/anomalyco/opentui) combines a native Zig renderer with TypeScript bindings and powers OpenCode. Core is imperative; React uses components/hooks; Solid uses signals/effects. The earlier recommendation was its React binding; the user instead chose the public React/Ink direction. This comparison remains useful if a future explicit decision revisits the renderer. [Framework overview](https://opentui.com/docs/).

OpenTUI documents mouse hit testing, bubbling, wheel events, drag handling, and automatic left-drag capture. It tracks one focused renderable and one global selection. **Core has no automatic Tab traversal**: the app chooses focus order. Splitter constraints and keyboard resizing also remain application behavior. [Interaction](https://opentui.com/docs/core-concepts/interaction/).

Built-in [Textarea](https://opentui.com/docs/components/textarea/) includes undo/redo; [Markdown](https://opentui.com/docs/components/markdown/) supports streaming updates. [Diff](https://opentui.com/docs/components/diff/) provides unified/split views but displays only the first supplied file patch. Create one per file. Solid's Diff component currently has a documented props-typing gap; React avoids that particular limitation. These components present content; they do not perform repository operations.

The [runtime matrix](https://opentui.com/docs/getting-started/runtime-support/) specifies Bun ≥1.3, or ≥1.4 on native Windows arm64; React ≥19.2; Solid exactly 1.9.12. Eight native artifacts cover macOS/Linux/Windows and Linux libc variants, while Bun Core CI covers macOS arm64, Linux x64, and Windows x64. Source development currently requires Bun ≥1.4.1 and Zig 0.16.0; ordinary consumers use prebuilt packages. [Development prerequisites](https://github.com/anomalyco/opentui).

[Bun executables](https://opentui.com/docs/ship/deploy/) can embed native and parser assets. Builds remain specific to OS, architecture, and libc; optional native packages and runtime-loaded assets must be handled deliberately. Validate artifacts on each required target. An [embedded terminal](https://opentui.com/docs/components/embedded-terminal/) is available if a later ADE decision requires an interactive child CLI; process lifetime and resize wiring remain app responsibilities.

[Ink](https://github.com/vadimdemedes/ink/releases), now selected, includes alternate-screen, paste, resize, and Kitty keyboard improvements in version 7. Its documented runtime is Node, and this research found no established Bun support lane or comparable first-class mouse API. The old [Bun support issue](https://github.com/vadimdemedes/ink/issues/636) is closed as not planned; this does not prove current Bun incompatibility. The user's choice makes those gaps validation and implementation work rather than grounds for silently reverting to OpenTUI.
