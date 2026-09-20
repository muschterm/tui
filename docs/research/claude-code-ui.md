# Claude Code UI lineage and reusable stack

Date: 2026-09-19. Primary-source research only; no packages installed or runtime behavior verified. This note addresses the user's preference for the Bun reference app to use Claude Code's UI technology. It supplements [language stack research](language-stacks.md); the parent design records the accepted stack. Versions are dated observations, to be revalidated and pinned before implementation.

## What the public evidence establishes

**React with public Ink is the closest evidenced reusable choice. Claude Code's current renderer cannot be equated with stock Ink.** Ink's own [project README](https://github.com/vadimdemedes/ink) lists Claude Code among applications using Ink. That establishes public acknowledgment of the relationship; it does not expose Claude Code's current dependency graph or promise equivalent behavior.

Anthropic's [official changelog](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md), version 2.0.10, records a terminal renderer rewrite. More recently observed entries describe fullscreen renderer fixes and fallback to a classic renderer. These are evidence of continuing application-specific rendering work, rather than a reusable library announcement.

In a [firsthand public account](https://news.ycombinator.com/item?id=46701013), Claude Code TUI engineer Chris Lloyd says the rendering system was rewritten. His follow-up describes React producing a scene graph, followed by layout, rasterization to a two-dimensional screen, comparison with the previous screen, and ANSI output. This supports React's role at the time of that account and a customized rendering pipeline. It does not establish the exact September implementation, the boundary of any Ink fork, or a supported public Anthropic renderer API.

No separately documented, supported Anthropic UI-renderer package was located in the inspected official material. Claude Code's [repository license](https://github.com/anthropics/claude-code/blob/main/LICENSE.md) reserves Anthropic's rights and refers use to its commercial terms. The CLI is an application distribution, not evidence that its renderer is available as a general-purpose library. This research uses no leaked source or packages extracted from it.

## Recommended interpretation

Use **TypeScript + React + public Ink**, with **Bun as the selected runtime, with compatibility still requiring validation**. Record this as the public React/Ink lineage choice, without calling it the exact Claude Code renderer. Public [Ink 7.1.1](https://github.com/vadimdemedes/ink/releases/tag/v7.1.1) is the observed release; its [manifest](https://github.com/vadimdemedes/ink/blob/v7.1.1/package.json) declares MIT licensing and Node ≥22. Ink 7 requires React ≥19.2. The sources checked do not establish an official Bun support lane. The historical [Bun issue](https://github.com/vadimdemedes/ink/issues/636) was closed as not planned, which does not prove today's Bun is incompatible.

Bun package installation, successful TypeScript compilation, or a working greeting screen would not validate raw input, resize, subprocess handoff, renderer scheduling, or packaged executables. If Bun fails the required scenarios, the explicit decision is whether to retain Ink with its documented Node runtime or retain Bun with another renderer. Do not silently substitute either. The benefit of this choice is recognizable React composition and a public reusable library; the cost is more workbench interaction work and runtime uncertainty than the previous OpenTUI proposal.

## Gaps to resolve against the shared behavior specification

Ink supplies text/box layout, input hooks, and focus controls. `useFocus` registers components for automatic Tab traversal in render order; application routing must still govern pane focus and editing modes. The [versioned API](https://github.com/vadimdemedes/ink/blob/v7.1.1/readme.md) documents Kitty keyboard negotiation, but keyboard support does not imply Kitty graphics support.

The implementation and validation work remains:

- **Three panes and mouse:** define constrained splitters, drag capture, hit testing, wheel routing, and selection. The inspected public API provides no comparable first-class mouse component event system. Ink 7.1.1 adds element positions; these alone are not a complete interaction layer.
- **Files and Git:** select or build multiline editing, undo, Markdown presentation, and diff surfaces; integrate safe saves, external-change handling, and repository operations. A React renderer does not supply those application services.
- **Graphics and streaming:** coordinate image lifecycle with screen updates and fallback behavior; integrate bounded stream updates and scroll anchoring. Treat these as application work, not inherited Claude Code capabilities.
- **Distribution and cleanup:** verify Bun packaging and dependency assets, terminal restoration, resize, and process handoff on macOS/Linux, directly and through SSH/tmux; Windows Terminal remains best effort. [Ink release notes](https://github.com/vadimdemedes/ink/releases) document alternate-screen, paste, resize, and child-process suspension facilities worth testing.

The acceptance criterion is the shared observable behavior, not visual similarity to Claude Code or an assumed match to its private implementation.
