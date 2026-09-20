# Interaction precedents

Research date: 2026-09-19. These findings describe upstream documentation, repository READMEs, and the official screenshots/plugin previews inspected in a browser during the visual revision. The terminal products were not run, and their behavior has not been verified in this project's terminal matrix. Proposed details remain subject to the [design interview](../design/interview.md).

## Omarchy and plugin visual references

The user explicitly added Omarchy and [its plugin gallery](https://plugins.omarchy.org/) after rejecting the first mockups. The gallery loads its catalogue dynamically; an initial empty static response did not mean there were no plugins. Browser review exposed the published previews for Omagram, Torrents, and omatodolist. No plugin was installed or executed.

[Omarchy's official theme page](https://omarchy.org/manual/themes/) presents coordinated palettes across its desktop and terminal tools. The inspected Tokyo Night preview includes highlighted source, colored system-monitor sections, and a consistent palette. **Design inference:** use a family of role colors, syntax/status accents, and considered panel boundaries rather than one accent over gray surfaces. Desktop wallpaper and file-manager rendering in that screenshot are not evidence of terminal graphics support.

The expanded [Omagram preview](https://plugins.omarchy.org/plugin.html?id=reidenxerx.omagram) shows distinct message authors, inline media, compact media controls, and colored panel boundaries. Gallery previews of [Torrents](https://plugins.omarchy.org/plugin.html?id=widget.torrents) and [omatodolist](https://plugins.omarchy.org/plugin.html?id=io.github.darksurferza.omatodolist) illustrate tab/control grouping and dense multi-pane content. These are useful composition references, not new application features to import.

The [plugin authoring guide](https://plugins.omarchy.org/develop.html) defines Quickshell/QML bar widgets, panels, overlays, and menus. The inspected plugin UI is graphical shell content; its appearance does not establish that the same renderer runs inside an SSH terminal. **Translation:** terminal-native color, symbols, grouping, and cell geometry carry structure; negotiated raster placements carry avatars, attachments, and image previews. See [rich-visual capability research](visual-enhancements.md). Rounded controls, multiple font sizes, desktop blur, and shell-level popouts must not be silently promised by the TUI reference.

The initial result was revision 2, later superseded by the [calmer revision 3 proposal](../design/visual-design.md) after feedback about clutter and invented avatars. The subsequent [local T3 Code inspection](t3-code-design.md) adds concrete pane-toggle, surface-lifecycle, composer and SVG-icon evidence. Source screenshots remain upstream; the repository's image artwork is original and its controls are illustrative.

## Herdr and its community

The user's `herdr` reference is identifiable: [herdr.dev](https://herdr.dev/) links to the canonical [herdrdev/herdr repository](https://github.com/herdrdev/herdr). Search results also contain older forks; those should not establish current upstream behavior. Herdr owns terminals running coding agents, presents workspace and agent status, and preserves running sessions independently of an attached client. This establishes a terminal-hosting precedent, not a structured conversation or turn-data contract.

Herdr documents mouse capture, mouse selection/copy, interactive scrollbars, keyboard pane focus and resizing, and pane zoom. Its sidebar can collapse into compact or fully hidden presentations. Configuration separates sidebar, panel, active-row, and selection colors; borders and pane gaps are configurable. A single-column layout activates at a configurable width, defaulting to 64 columns. These are useful references for explicit layout states and visually restrained surfaces. [Herdr configuration reference](https://herdr.dev/docs/config-reference/)

Two community plugins provide closer file and Git precedents; their features must not be attributed to Herdr core:

- [herdr-file-viewer](https://github.com/smarzban/herdr-file-viewer) combines a tree with automatic diff, rendered Markdown, or highlighted source views. It offers changed-file filtering, branch merge-base versus HEAD baselines, pinned-file comparison, path/line navigation, and annotations copied back to an agent. It is read-only; editing hands off to an external editor.
- [herdr-sidebar](https://github.com/alexarthurs/herdr-sidebar) combines Explorer, Search, and Source Control activities. Clicking reuses a preview tab; double-clicking pins it. Its experimental editor advertises explicit save and external-change protection. Source Control includes staging, committing, history, and branch/worktree/stash navigation. These demonstrate useful scope options without establishing the editor or Git scope this project should accept.

## Git review precedents

Sublime Merge is the Git reference used in the accepted Q6 direction. Its documented review features include side-by-side, syntax-highlighted, character-level diffs; draggable additional hunk context; and file, hunk, and line staging. Its adaptable layout is a useful reference for letting detailed review occupy more space. [Sublime Merge](https://www.sublimemerge.com/)

Its selection model connects a commit or current pending changes to metadata, changed files, and diffs. Working-directory and index changes remain distinguishable, and selecting a commit changes the file list accordingly. This suggests making both the selected object and comparison scope visible. [Sublime Merge getting started](https://www.sublimemerge.com/docs/getting_started)

[Lazygit](https://github.com/jesseduffield/lazygit) documents file-to-diff drill-down, line/range/hunk staging, enlarged views, and a comparison mode with a marked baseline, selectable counterpart, reverse direction, and explicit exit. Its undo uses the reflog and excludes working-tree and stash changes. A future interface should state what an undo action can restore rather than imply universal recovery.

## Proposed patterns and pending decisions

Keep global navigation distinct from task-specific Files, Changes, and Terminal surfaces. The user subsequently accepted three primary columns with collapse/resize/expansion and added a bottom panel inside the center column. Arbitrary splits are outside the initial scope; see [the accepted layout direction](../design/layout.md).

For files, consider explicit Preview, Source, and Diff modes. Q5 accepted a lightweight editor; Q28/Q29 subsequently selected live collaboration, autosave, and external-change reconciliation. Detailed mode/tab behavior remains open. Rendered Markdown does not imply rich-text editing.

For Git, consider a visible comparison selector and changed-file navigation beside the diff. Q6 subsequently expanded the scope to a graph with context menus, soft reset, fast-forward-only pull, rebase, and manual/agent-assisted conflicts alongside the everyday client. See [Git design](../design/git-client.md). Repository instructions still prohibit remote pulls and pushes during this work; the future application's features do not authorize contributor remote operations.

Q4 subsequently selected a native structured ADE using headless agents over ACP. Turn-related changes still require provenance: the current Git diff cannot establish which turn authored changes. Concurrent user edits or agents make attribution especially ambiguous. Q26 subsequently selected a recorded start/end comparison labelled changes observed during this turn, alongside available provider-reported edits. Neither establishes exclusive authorship. Keep historical observations, staged changes, and current working-tree changes distinct; automatic rollback is deferred.

## Candidate mouse acceptance coverage

These are proposed future checks, not accepted requirements or tested claims:

- Activate every visible navigation item, tab, disclosure, button, and menu.
- Resize dividers within minimum sizes; collapse and expand panes without losing state.
- Scroll the intended hovered surface, including horizontal diff overflow.
- Select and copy text without accidentally activating rows or changing panes.
- Preserve predictable focus when opening previews, menus, editors, and dialogs.
- Exercise drag completion, cancellation, terminal resize, and narrow layouts.
- Verify keyboard equivalents and reduced-capability behavior over the agreed terminal, SSH, and tmux matrix.
