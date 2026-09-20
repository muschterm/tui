# ACP agent integration research

Date: 2026-09-19. Evidence: primary documentation, manifests, registry entries, and source inspection. No agent was installed, authenticated, or executed. Version observations are snapshots, not implementation pins or proof of runtime compatibility.

The requested direction is a native, structured ADE using ACP: connect directly to agents exposing ACP and reuse adapters where needed. Recommend established adapters before building custom translations. A common protocol reduces provider-specific frontend work; it does not guarantee every agent supports every surface.

## Registry and provider paths

The [ACP registry](https://github.com/agentclientprotocol/registry) curates authenticated agents and checks advertised authentication methods in CI. Its hourly version updates make it useful for discovery, not a reproducibility lockfile. [Distribution metadata](https://github.com/agentclientprotocol/registry/blob/main/FORMAT.md) describes platform binaries, npm/`npx`, and Python/`uvx`; preview distributions are explicitly unverified.

The [published index](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) currently lists these versions:

| Agent | Observed version | ACP implementation | Launchable npm package |
| --- | --- | --- | --- |
| GitHub Copilot | 1.0.86 | Vendor CLI implements ACP directly | `@github/copilot`, argument `--acp` |
| Claude Agent | 0.79.0 | Separate adapter using official Claude Agent SDK | `@agentclientprotocol/claude-agent-acp` |
| Codex | 1.12.0 | Separate adapter using Codex App Server | `@agentclientprotocol/codex-acp` |

“Separate adapter” describes architecture, not the absence of vendor participation. The registry credits multiple organizations on the adapters. This research makes no claim that Claude or Codex lacks another native ACP implementation.

## Copilot: direct ACP example

[GitHub's ACP documentation](https://docs.github.com/en/copilot/reference/copilot-cli-reference/acp-server) explicitly documents `copilot --acp --stdio`, with stdio the default and recommended editor integration. The child exits when its input closes. TCP is also available, but introduces separate server lifetime and connection management. ACP support is **public preview**.

Commands are advertised dynamically through `available_commands_update`; use that list instead of copying interactive CLI commands. Some configuration, including initial tool filtering and reasoning effort, is applied when the server starts and inherited by sessions. A session-creation request does not carry every server option. These details belong in provider configuration rather than being silently normalized away.

[Installation](https://docs.github.com/en/copilot/get-started/cli-quickstart) supports npm with Node ≥22, Homebrew, and WinGet. Users normally authenticate with GitHub; organization policy can gate access. The ACP server also documents configured BYOK operation without GitHub login. The [CLI license](https://github.com/github/copilot-cli/blob/main/LICENSE.md) supplies its own terms; registry inclusion does not make it Apache-licensed.

## Claude: reuse the SDK adapter

The former Zed URL now redirects to [agentclientprotocol/claude-agent-acp](https://github.com/agentclientprotocol/claude-agent-acp). Its backend is the official TypeScript Agent SDK, not terminal-screen scraping. The [manifest](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/package.json) requires Node ≥22 and currently pins SDK 0.3.274. The adapter offers images, permission requests, edit review, tool activity, client MCP servers, and negotiated extensions. Subagent sessions require bilateral negotiation; otherwise activity falls back to ordinary tool calls. Prefer released packages over the continuously published preview channel.

Anthropic separately documents [headless `claude -p`](https://code.claude.com/docs/en/headless), JSON/streaming output, and Python/TypeScript SDKs. Those are programmatic surfaces; they are not themselves evidence of ACP compatibility. The adapter already performs the translation this project would otherwise need to maintain.

[Adapter source](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/src/acp-agent.ts) advertises terminal subscription and Console authentication according to client capabilities. Authentication needs a product decision: the [official SDK overview](https://code.claude.com/docs/en/agent-sdk/overview) requires prior approval for third-party products offering claude.ai login/rate limits. A separate [June 16 billing update](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan) pauses announced changes and says existing SDK, headless, and third-party usage still draws subscription limits. Billing behavior does not establish this client's entitlement to offer login.

The adapter's [LICENSE](https://github.com/agentclientprotocol/claude-agent-acp/blob/main/LICENSE) is Apache-2.0; the registry labels the agent proprietary. Preserve the distinction between adapter code and SDK/service terms rather than assuming one license covers the complete product.

## Codex: use the current adapter lineage

[zed-industries/codex-acp](https://github.com/zed-industries/codex-acp) was archived July 22, 2026 and directs new installs to [agentclientprotocol/codex-acp](https://github.com/agentclientprotocol/codex-acp). The successor is a stdio ACP server that launches App Server and maps requests/events. Its npm package includes a compatible Codex dependency; overriding `CODEX_PATH` changes that compatibility assumption. It advertises ChatGPT/API-key authentication and negotiated gateway support. Its adapter license is Apache-2.0. Source and preview activity show ongoing development, not feature parity certification.

OpenAI's [App Server documentation](https://learn.chatgpt.com/docs/app-server) and [platform article](https://developers.openai.com/blog/codex-as-a-platform), separately verified during this research, describe bidirectional JSON-RPC for rich clients, including authentication, history, approvals, and events; stdio is the default. App Server is its own protocol: shared JSON-RPC framing does not make it ACP. The official reference labels the command and WebSocket transport experimental and unsupported for production workloads. The adapter's published availability does not erase that upstream maturity constraint. Recheck both contracts before implementation.

## Decisions and validation still required

Recommend a tested provider set plus generic ACP configuration, with visible unsupported capabilities. Pin adapter/backend pairs; negotiate capabilities and discover commands at runtime. Decide managed installation versus user-owned executables, authentication UX, stable versus opt-in extensions, and acceptable preview risk. Test session restoration, streaming, cancellation, pending approvals, process cleanup, and remote authentication per provider. File-change events can inform review but do not by themselves prove exclusive turn ownership or safe rollback. “Works with every agent” should mean an extensible connection model, not an untested parity promise.
