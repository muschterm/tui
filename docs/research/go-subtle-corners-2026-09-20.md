# Subtler control ends and inset card backgrounds — 2026-09-20

**Rejected control treatment:** the user rejected the thin outlined ends shown
in this report. The preceding filled buttons/tabs have been restored; the
interior-only thread background correction remains. Captures below are historical
evidence of the rejected experiment, not the approved visual direction.
The subsequent [prompt-corner correction](go-prompt-corners-2026-09-20.md)
supersedes both this experiment and the temporary rollback below.

The user prefers the prompt's restrained border rounding and asks that thread
backgrounds stay inside their outlines. The earlier solid pill caps are replaced
by thin curved ends around a rectangular interior fill. One-row controls retain
their existing sizes, reserved slots, overflow calculation and click targets.
This is a lighter outline treatment, not an arbitrary pixel-radius feature:
terminal glyph shapes remain controlled by the font.

The rich ends use the pinned Nerd Fonts v3.4.0 thin half-circle glyphs U+E0B7 and
U+E0B5. Plain and limited-color modes retain equal-width parentheses. Thread cards
keep the same rounded box-drawing corners as before, but only their two interior
content rows receive the normal/selected fill. Border and corner cells use the
sidebar background. Metadata and border selection still share the existing
thread command; status/menu actions remain independent.

Validation on macOS darwin/arm64:

- `GOCACHE=/tmp/tui-go-build make check` passed formatting, vet, all race tests
  and build. No new tests were added for this styling-only correction.
- Generated fresh Go View captures with the existing navigation, thread-status
  and polished-view capture suites. Reviewed dark, light and minimum-width views.
  The [dark](go-subtle-corners-captures/160x50-lightfalse-tabs.png),
  [light](go-subtle-corners-captures/160x50-lighttrue-tabs.png) and
  [narrow](go-subtle-corners-captures/48x22-lightfalse-navigation-narrow.png)
  images were rasterized from actual render output using JetBrainsMono Nerd Font
  Mono; they are not GUI terminal screenshots. Compressed ANSI/SVG sources are
  retained beside each PNG.

- `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-subtle-corners-pty`
  passed all 38 checks, including pointer/keyboard question submission, resizing,
  tab lifecycle and preserved drafts.
- `git diff --check` passed.

No persistence, execution, protocol, dependency or Git operation changed. Relaunch
the TUI from the repository root with `./apps/go/bin/tui-go`; the server can remain
running. Real provider/terminal integration and terminal-specific compatibility
limits remain as recorded in the earlier reports.

## Rollback validation

Restored the continuous filled control caps, preserving the interior-only thread
background fix and all geometry/actions. `GOCACHE=/tmp/tui-go-build make check`
passed formatting, vet, race tests and build. Fresh [dark](go-subtle-corners-captures/160x50-lightfalse-restored.png)
and [light](go-subtle-corners-captures/160x50-lighttrue-restored.png) Go View captures
were visually reviewed. No new native PTY run was needed for this glyph/color
rollback; the earlier filled-control runtime checks are recorded separately.
