# First Go slice: bounded feasibility evidence

Date: **2026-09-19**. These are candidate-library probes and versioned source findings, not acceptance of a collaboration algorithm, embedded-terminal implementation, or provider parity. No paid agent execution or user-file mutation was performed. The probes use temporary dependency/storage directories. No production dependency was added by these probes.

## Environment and observed results

Local **darwin/arm64**, Go **1.27.1**, Bun **1.4.2**, Node **26.8.2**, Git **2.54.0 (Apple Git-157)**. Cargo was not on PATH. Initial sandboxed package retrieval failed DNS; approved public downloads succeeded. Mise printed a cache-write warning while version checks and probes still succeeded.

| Boundary | Executed evidence | Remaining gap |
| --- | --- | --- |
| Collaborative text | Yjs 13.6.32 under Bun: concurrent inserts converge, duplicate delivery does not duplicate text, one client's undo preserves the peer's insertion; subsequent concurrent delete/emoji append converges | Native Go/Rust interoperability, durable undo, cursor anchoring, reconnect/restart, randomized schedules and larger documents **NOT RUN** |
| External writes | `git merge-file -p` in temporary files combines disjoint line edits; overlap returns exit 1 with conflict content | This is a merge primitive only. Autosave ordering, pre-write revalidation, file replacement/deletion, watcher races, failed saves and restart recovery **NOT RUN** |
| Embedded terminal | Go x/vt parses byte-by-byte UTF-8 and escape sequences, erase/home, alternate-screen restoration, and out-of-grid access after resize | Real PTY-to-emulator integration, query replies, mouse/paste/keyboard negotiation, scrollback bounds, shell/full-screen apps, process ownership and observer control **NOT RUN** |
| Bun presentation | Ink 7.1.1 + React 19.3.0 renders text and unmounts on Bun 1.4.2 using a non-TTY stream | Interactive raw input, mouse, resize, terminal cleanup, graphics, SSH/tmux and production packaging **NOT RUN** |
| Bun PTY | Initial 42×9 dimensions, resize to 51×12, input/output, process completion, explicit PTY close pass | Process-tree teardown, background persistence and hidden-pane lifecycle **NOT RUN** |
| ACP adapters | Versioned source inspection below | Initialization, replay fixtures against installed adapters and live provider runs **NOT RUN** |

Scripts: [Bun/Yjs/Ink/merge/PTY probe](go-feasibility-probes/bun-feasibility.mjs), [Go emulator probe](go-feasibility-probes/vt-feasibility.go). Both moved from `apps/go/internal/probe` to this directory on 2026-09-22 so the Go module holds only application code; the Go probe keeps its build-ignore constraint, so candidate dependencies never enter the application module.

## Reproduction

The successful execution used `/tmp/tui-feasibility-20260919`; substitute a fresh temporary directory to avoid reusing another run. These commands intentionally install probe dependencies outside the application. Direct versions are exact; the temporary generated lockfiles were not promoted to production pins.

```sh
mkdir -p /tmp/tui-feasibility-20260919
BUN_INSTALL_CACHE_DIR=/tmp/tui-feasibility-20260919/cache bun add --cwd /tmp/tui-feasibility-20260919 --ignore-scripts --exact ink@7.1.1 react@19.3.0 yjs@13.6.32
bun docs/research/go-feasibility-probes/bun-feasibility.mjs /tmp/tui-feasibility-20260919
```

All four grouped assertions passed. Git only reads/writes the probe's temporary files; it does not operate on the checkout or invoke a remote.

From the temporary directory, initialize a separate module and run the Go probe by absolute path:

```sh
go mod init feasibility-vt
GOMODCACHE=/tmp/tui-feasibility-20260919/gomod GOCACHE=/tmp/tui-feasibility-20260919/gocache go get github.com/charmbracelet/x/vt@v0.0.0-20260913004009-c615ff2f7805
GOMODCACHE=/tmp/tui-feasibility-20260919/gomod GOCACHE=/tmp/tui-feasibility-20260919/gocache go run /path/to/tui/docs/research/go-feasibility-probes/vt-feasibility.go
```

Result: `PASS x/vt: split UTF-8/escape parsing, erase/home, alternate-screen restoration, bounded resize`. `gofmt` was applied to the Go probe. None of these checks establishes an interactive terminal compatibility matrix.

## Collaboration candidates and costs

[Yjs selective undo](https://docs.yjs.dev/api/undo-manager) supports tracking transaction origins; the probe uses a distinct local origin and leaves remote updates outside that client's undo scope. The [13.6.32 implementation](https://github.com/yjs/yjs/blob/v13.6.32/src/utils/UndoManager.js) is the pinned source corresponding to the executed package. This demonstrates a useful primitive, not durable client identity or a recovered undo history.

[Yrs](https://github.com/y-crdt/y-crdt) provides Rust, C FFI and Wasm implementations targeting Yjs compatibility. Go binding/FFI packaging, Unicode index conventions and cross-language fixtures still require investigation; no Yrs version was selected or executed. [Automerge Go](https://github.com/automerge/automerge-go) is another candidate using cgo over Rust, with native build/distribution costs. It was not installed or probed; client-specific undo remains unestablished for that binding. Neither source justifies selecting an editor algorithm now.

External disk reconciliation remains an application responsibility regardless of convergent in-memory text. Preserve baseline/shared/external versions, invalidate stale saves and separate durable document acknowledgement from disk-save success. The tested merge primitive does not close those requirements in [the editor contract](../design/editor.md).

## Go emulator and Bun boundary

The Go proxy resolved x/vt to **v0.0.0-20260913004009-c615ff2f7805**, commit **c615ff2f780526ed45ede2c894200e4a584c361c**. [Versioned package API](https://pkg.go.dev/github.com/charmbracelet/x/vt@v0.0.0-20260913004009-c615ff2f7805) exposes an emulator, grid access and resize. This remains a pre-v1 experimental candidate with a dependency tree; its benefit is avoiding a custom VT parser. Passing a few byte-stream assertions does not establish emulation completeness or containment of every control sequence.

[Bun's PTY guide](https://bun.com/docs/runtime/child-process#terminal-pty-support) distinguishes terminal I/O from subprocess lifecycle. The runtime probe checks both process completion and explicit terminal closure. Its bytes are captured in memory, never forwarded to the outer terminal. Ink remains the public renderer; these results establish no equivalence with Claude Code's private renderer. Exact npm candidates were obtained from the public registry: [Ink 7.1.1](https://www.npmjs.com/package/ink/v/7.1.1), [React 19.3.0](https://www.npmjs.com/package/react/v/19.3.0), [Yjs 13.6.32](https://www.npmjs.com/package/yjs/v/13.6.32).

## ACP release candidates and unsupported claims

| Candidate | Pinned release dependency evidence | Capability finding |
| --- | --- | --- |
| Codex ACP **1.12.0** | Release [lockfile](https://github.com/agentclientprotocol/codex-acp/blob/v1.12.0/package-lock.json): ACP SDK **1.4.0**, Codex **0.154.0** | [Versioned child-session documentation](https://github.com/agentclientprotocol/codex-acp/blob/v1.12.0/docs/subagent-sessions.md) describes bilateral negotiation, `subagent_spawned` / `subagent_state_update`, child-scoped activity and reconstruction on load. Targeted child cancellation/close is absent. Missing historical outcomes are not successful completion. |
| Claude Agent ACP **0.79.0** | Release [manifest](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.79.0/package.json): ACP SDK **1.4.0**, Claude Agent SDK **0.3.274** | [Versioned source](https://github.com/agentclientprotocol/claude-agent-acp/blob/v0.79.0/src/acp-agent.ts) awaits form elicitation for AskUserQuestion and returns `updatedInput`; replay reconstructs available sidechain activity with disconnected state for unproven lineage/outcomes. |

Codex's latest-release endpoint resolved to 1.12.0. Claude's cached latest-release page resolved to 0.78.0 while the directly fetched 0.79.0 tag manifest exists; use the explicitly inspected candidate, not an unsupported claim that all registries agree about latest.

The old child-event dialect above must not be silently mixed with newer subagent proposals. Source-backed replay reconstruction does not prove full live child transcript/tool-history delivery. No adapter process, authentication flow or model run was started here. Configuration application, plans, tool payload fidelity, usage freshness, approval choices, parent/child correlation and replay require actual captured streams plus later live checks.

**Continued-work asynchronous question answers remain an integration gap.** Awaiting an elicitation callback establishes a pending response path; it does not establish that the same requesting execution continues useful work and later consumes the correlated answer. Both adapters need a test showing progress before submission, receipt by the original request, and stale/cancel/reconnect rejection behavior. Ordinary prompt queuing and steering must not stand in for that proof. See [question research](agent-questions.md) and [activity research](acp-activity.md).

## Consequence for the first slice

These probes support proceeding with the small Go server/attach and shell work while keeping editor, terminal and provider capabilities explicitly incomplete. The native collaboration choice, autosave safety and live async/child-history evidence remain gates before claiming those product features implemented. No research candidate in this note is an accepted ADR.
