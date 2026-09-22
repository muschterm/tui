# tui

A documented foundation for polished terminal applications in Go, Rust, and Bun. The visual target combines Codex’s workspace composition with the richer color, icons, images, and activity hierarchy of Omarchy and herdr, adapted to terminal cells: hideable navigation, a central workspace and prompt, a bottom panel inside the center column, and a right sidebar for files, Git, and activity inspectors.

The deliverable is a shared behavior specification and three complete native server/TUI reference apps. **Go comes first**, using Bubble Tea and the Charm ecosystem; Rust uses Ratatui, and Bun uses TypeScript + React + public Ink with Bun.Terminal. The Go slice supplies a persistent background server, interactive shell and built-in Go ACP bridges around installed Claude/Codex CLIs, alongside fixture activity; [2026-09-22 validation](docs/research/go-acp-2026-09-22.md) distinguishes live HTTP results from terminal evidence. Broader agent, file, Git and terminal integrations remain incomplete; the root Bun entry point remains a bootstrap placeholder. See [current scope and launch instructions](docs/design/go-slice.md).

## Start here

**Next agent-integration task:** [implementation handoff](docs/implementation/agent-integration-handoff.md)
records the accepted ACP/official-runtime direction, unified question contract,
UCF reference findings, remaining adapter decisions and validation checklist.
**Latest direction:** build our own Go ACP adapters, shipped with the application,
around installed Claude/Codex CLIs. The user rejects separate npm/Node adapter
installations. The [built-in bridge checkpoint](docs/implementation/go-adapter-checkpoint.md)
records the Go replacement, validation and remaining gaps; the historical
prototype and its fixtures remain reference evidence. Broader question and
provider parity remain incomplete. The
[request-delivery continuation](docs/implementation/request-delivery-checkpoint.md)
adds exact approval option IDs, truthful acceptance/recovery states and live
connection guards. The bounded [Claude native-question slice](docs/implementation/native-question-checkpoint.md)
uses the same answer contract. The [latest personal-prototype continuation](docs/implementation/agent-recovery-checkpoint.md)
adds durable dispatch, approval capacity guards and recovery without prompt
replay. [Live evidence](docs/research/agent-recovery-live-2026-09-22.md) covers
Claude native questions and Codex prompt/cancel/restart; authoritative answer
receipts, other question dialects and fallback remain unavailable.
The versioned [Claude](docs/research/claude-integration-matrix-2026-09-22.md) and
[Codex](docs/research/codex-integration-matrix-2026-09-22.md) comparisons favor
adapter reuse with targeted fixes as their earlier recommendation; the user's
subsequent no-extra-install requirement selects our own Go adapters instead.
Existing official-runtime login is used for
the selected personal prototype; distributed Claude SDK product eligibility
remains open.

Claude and Codex use your installed CLIs, discovered automatically on the
server's PATH. ACP adapters provide protocol translation; bundled provider
runtimes are never used as a fallback. See [runtime setup](docs/design/go-slice.md#acp-agents--2026-09-22).

1. Read [the design brief](docs/design/brief.md) for accepted requirements and [the glossary](CONTEXT.md) for terminology.
2. Follow [AGENTS.md](AGENTS.md) and [the coding and verification contract](docs/design/quality.md).
3. Use [the Go-first implementation sequence](docs/design/implementation.md) to build small, verifiable slices.
4. Consult [the interview](docs/design/interview.md) when distinguishing accepted decisions from proposals. Exact key bindings are explicitly deferred to an interactive prototype.

## Visual target

[Revised dark reference](docs/design/visuals/workspace-dark.png) · [Multiple terminals](docs/design/visuals/workspace-terminal-dark.png) · [Maximized surface](docs/design/visuals/workspace-maximized-dark.png) · [Add surface chooser](docs/design/visuals/workspace-chooser-dark.png) · [Empty host hidden](docs/design/visuals/workspace-none-dark.png) · [Bottom panel](docs/design/visuals/workspace-bottom-dark.png) · [Image/file view](docs/design/visuals/workspace-files-dark.png) · [Light reference](docs/design/visuals/workspace-light.png) · [Visual principles and SVG icon policy](docs/design/visual-design.md)

Revision 3 reduces clutter, adds persistent pane controls and closable surface tabs (including multiple terminals), and removes the rejected invented avatars. Top-right controls are ordered maximize/restore, center-bottom, right panel. It retains the richer colors requested after the first subdued mockups. The user approved this visual direction. These are design illustrations; terminal geometry and interaction still need prototype validation. Sample settings and telemetry do not establish provider support.

Running subagents and the current plan stay above the prompt and open the [Agents](docs/design/visuals/workspace-agents-dark.png) and [Plan](docs/design/visuals/workspace-plan-dark.png) surfaces. Questions occupy the same fixed area without replacing the composer: [blocking questions](docs/design/visuals/workspace-questions-blocking-dark.png), [asynchronous questions](docs/design/visuals/workspace-questions-async-dark.png), and [questions with a maximized surface](docs/design/visuals/workspace-maximized-questions-dark.png).

## Behavior specification

The subsequent [gap review](docs/design/interview.md#gap-review-background-attention) records accepted attention indicators, approval cards, captured prompt context/settings, queue controls, thread restoration, terminal close/hide behavior, Git/autosave coordination and compact activity overflow.

| Area | Documents |
| --- | --- |
| Shell and presentation | [Layout](docs/design/layout.md), [themes and visual hierarchy](docs/design/visual-design.md), [activity and subagent inspectors](docs/design/activity.md) |
| Background work | [Server lifecycle](docs/design/server.md), [shared client contract](docs/design/client-protocol.md), [application-home SQLite storage](docs/design/storage.md) |
| ADE | [Projects and filtering](docs/design/projects.md), [Close/Reopen/Delete threads](docs/design/threads.md), [Agent boundary](docs/design/ade.md), [agent/model/effort/permission/context/speed configuration](docs/design/thread-configuration.md), [blocking and asynchronous questions](docs/design/questions.md), [stream-supplied usage and cost](docs/design/usage.md) |
| Workspace surfaces | [Collaborative editor and autosave](docs/design/editor.md), [Git and stop-for-review conflict resolution](docs/design/git-client.md), [checkouts and worktrees](docs/design/workspaces.md) |
| Delivery | [Implementation sequence](docs/design/implementation.md), [shared coding and verification requirements](docs/design/quality.md) |

Each application has its own identity. Executable names, home directories, and environment prefixes such as `my-app`, `~/.myapp/`, and `MYAPP_HOME` are placeholders. A server survives TUI exit, supports multiple clients, and preserves state under the application home; server restart recovery waits for Resume by default, with [explicit opt-in continuation](docs/design/settings.md) for verified integrations. The ADE is the example domain, while other applications supply their own navigation, workflow, and surfaces.

## Architecture decisions

- [Shared specification and idiomatic complete apps](docs/adr/0001-shared-specification-idiomatic-reference-apps.md)
- [ACP v1 agent boundary](docs/adr/0002-acp-agent-boundary.md)
- [Background server and attachable clients](docs/adr/0003-background-server-attachable-clients.md)
- [Application home and SQLite](docs/adr/0004-application-home-sqlite.md)
- [Live collaborative documents and autosave](docs/adr/0005-collaborative-documents-autosave.md)
- [Go snapshot and command contract](docs/adr/0006-go-snapshot-command-contract.md)
- [Thread deletion and saved-view projection](docs/adr/0007-thread-deletion-and-view-projection.md)

- [Cell-native component state](docs/adr/0008-cell-native-component-state.md)
- [Turn-bound queued-message steering](docs/adr/0009-turn-bound-steering.md)
- [Environment settings and recovery](docs/adr/0010-environment-settings-and-recovery.md)
- [First-Send thread creation](docs/adr/0011-first-send-thread-creation.md)
- [Go CLI framework](docs/adr/0012-go-cli-framework.md)
- [Server-owned ACP agent processes](docs/adr/0013-server-owned-acp-agent-processes.md)
- [ACP boundary and official agent runtimes](docs/adr/0014-acp-boundary-official-agent-runtimes.md)
- [Application-owned question contract](docs/adr/0015-app-owned-question-contract.md)

The accepted [component construction and interaction states](docs/design/components.md)
specify variants, selection, hover and keyboard focus.

[First-slice validation](docs/research/go-slice-validation-2026-09-19.md) · [Actual Go render captures](docs/research/go-captures/README.md) · [Notification/question review](docs/research/go-question-review-2026-09-20.md) · [Latest project/thread navigation review](docs/research/go-navigation-review-2026-09-20.md)

[Code review and fixes, 2026-09-21](docs/research/go-code-review-2026-09-21.md) · [Sidebar and settings behavior](docs/design/settings.md) · [Sidebar implementation review](docs/research/go-sidebar-settings-2026-09-20.md) · [Draft/composer and modal review](docs/research/go-draft-composer-2026-09-20.md)

## Research and evidence

Research is dated and source-linked. [Bounded feasibility probes](docs/research/go-feasibility-2026-09-19.md) record executed library checks separately from pending interactive terminal and provider validation.

| Topic | Research |
| --- | --- |
| Terminals and libraries | [Terminal capabilities](docs/research/terminal-capabilities.md), [language stacks](docs/research/language-stacks.md), [Claude Code’s UI lineage](docs/research/claude-code-ui.md), [Bun.Terminal](docs/research/bun-terminal.md) |
| Interaction and Git | [Omarchy plugins, herdr, and desktop precedents](docs/research/interaction-precedents.md), [local T3 Code design inspection](docs/research/t3-code-design.md), [rich visuals and graphics support](docs/research/visual-enhancements.md), [Git operation semantics](docs/research/git-operations.md) |
| Agents | [ACP protocol](docs/research/acp-protocol.md), [existing agents/adapters](docs/research/acp-agents.md), [configuration](docs/research/acp-configuration.md), [activity and child history](docs/research/acp-activity.md), [question and answer delivery](docs/research/agent-questions.md), [Codex presentation inventory](docs/research/codex-presentation.md) |

The documentation uses the local [grill-with-docs skill](.agents/skills/grill-with-docs/SKILL.md), combining the interview with a glossary and concise ADRs. Research recommendations are not automatically product decisions.

## Existing tooling

From `apps/go`, run `make build`, `make check` (gofmt, vet, staticcheck, race tests, build) and `make test`; `make vuln` runs govulncheck and `make pty` runs the OS-PTY harnesses in `apps/go/scripts/`. Launch `./bin/tui-go`; `./bin/tui-go --help` lists the `server`, `snapshot`, `probe`, `version` and `completion` commands. Use `./bin/tui-go --client desk` to restore that named client’s view across launches. The Go reference uses `~/.tui-go`, overridable with `--home` or `TUI_GO_HOME`. See [the first-slice guide](docs/design/go-slice.md) for lifecycle commands, prototype bindings and limitations.

`bun run update-skills` updates the installed project skills. Rust and Bun application tooling has not been introduced.

Repository development is local only: no remote pulls or pushes.
