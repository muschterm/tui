# ACP protocol research

Source-checked: **2026-09-19**. This records documented protocol behavior and implementation proposals; no agents, adapters, SDKs or conformance tests were executed.

The product direction is a native agent development environment (ADE) connecting directly to ACP agents and using adapters for other agents. ACP belongs to the ADE integration layer; the reusable TUI shell need not know about providers or sessions. Provider availability and adapter fidelity require separate verification.

## Stable boundary and transport

**ACP v1 is the stable baseline examined here.** ACP v2 was published as a draft on July 20, 2026. Its prompt lifecycle, message updates, permissions and richer diffs change important semantics; do not mix v2 examples into a v1 implementation. Upstream advises version negotiation and feature flags for draft implementations. [v2 draft announcement](https://agentclientprotocol.com/announcements/acp-v2-draft).

The standard production transport is UTF-8 JSON-RPC over subprocess stdio, with one message per newline. Agent stdout is reserved for protocol messages; stderr may carry logs. Streamable HTTP remains a draft proposal; custom bidirectional transports are allowed but require their own interoperability contract. ACP-over-HTTP must not be inferred from an agent's ability to connect to HTTP MCP servers. [Transports](https://agentclientprotocol.com/protocol/v1/transports).

**Proposal:** start with stdio and explicit agent/adapter invocation configuration. The later application architecture uses authenticated loopback HTTP/WebSocket with SSH forwarding between frontends and the background server. This is separate from the server-to-agent ACP transport; remote ACP transport is not implied.

## Connection and interaction

Before creating sessions, `initialize` negotiates the major protocol version, capabilities and authentication methods. Unsupported negotiated versions should close the connection with a useful error. Optional capabilities govern which operations/content are legal; missing support is not implicit support. Authenticate when required, then create a session with its working directory and MCP configuration. [Initialization](https://agentclientprotocol.com/protocol/v1/initialization).

In v1, `session/prompt` starts a turn; `session/update` delivers message chunks, tool activity and other progress. The prompt response carries a stop reason, including completion, limits, refusal or cancellation. Do not infer completion from a final-looking text chunk. `session/cancel` is a notification: accept trailing updates and wait for the prompt's cancellation response. Pending permission requests must receive a cancelled outcome. Cancellation is not rollback. [Prompt turn](https://agentclientprotocol.com/protocol/v1/prompt-turn).

Generic `$/cancel_request` is also documented, but handling is optional and distinct from prompt cancellation. It cannot justify a promise that arbitrary operations stop immediately. [Cancellation](https://agentclientprotocol.com/protocol/v1/cancellation).

Agents report tool calls and subsequent updates using session-scoped identifiers. Reporting and permission requests are not a universal audit or interception layer: reporting is generally recommended, while requesting permission is optional. Present the agent's supplied choices through `session/request_permission`; do not turn tool names or categories into authorization. [Tool calls](https://agentclientprotocol.com/protocol/v1/tool-calls).

## Files, commands and authority

Client filesystem methods are optional capabilities. Reads can include unsaved editor content; writes update or create text files. Advertise them only after defining how ADE buffers, disk writes and external changes interact. The write request does not provide a universal compare-and-swap/version guard; conflict handling remains client policy. [File system](https://agentclientprotocol.com/protocol/v1/file-system).

Optional client terminal methods cover command creation, retained output, waiting, killing and release. Output can be truncated and terminal references can appear in tool results. This is not a general interactive terminal emulator contract: v1 does not define universal stdin injection or terminal resizing through these methods. [Terminals](https://agentclientprotocol.com/protocol/v1/terminals).

Agents execute tools and may use client facilities; advertising no client filesystem/terminal capability does not prohibit the agent's own direct access. ACP's architecture assumes a trusted agent relationship. **Inference:** capability negotiation and permission UI are not an OS sandbox. Actual filesystem/process restrictions require agent configuration and/or an execution boundary. [Architecture](https://agentclientprotocol.com/get-started/architecture), [tool execution](https://agentclientprotocol.com/protocol/v1/prompt-turn).

## History, concurrency and changes

These stable operations remain optional:

| Operation | Meaning |
| --- | --- |
| `session/load` | Restore context and replay the conversation via updates |
| `session/resume` | Reconnect without history replay |
| `session/list` | Discover the agent's known sessions |
| `session/close` | Cancel active work and release session resources |
| `session/delete` | Remove sessions from the agent's history listing |

Check each advertised capability. Local transcript storage is not equivalent to restorable agent context, and IDs do not establish portable sessions between providers. [Session setup](https://agentclientprotocol.com/protocol/v1/session-setup), [session list](https://agentclientprotocol.com/protocol/v1/session-list), [session delete](https://agentclientprotocol.com/protocol/v1/session-delete).

One connection can support concurrent sessions. This does not guarantee arbitrary overlapping prompts, steering or queueing inside one v1 session, provider capacity, or isolated workspace writes. **Proposal:** serialize prompts per session initially and test cross-session concurrency separately. [Architecture](https://agentclientprotocol.com/get-started/architecture), [v1 turn lifecycle](https://agentclientprotocol.com/protocol/v1/prompt-turn).

V1 tool content can include text diffs and file locations. Neither those reports nor the reviewed stable schema defines a universal workspace checkpoint, atomic undo, or authoritative turn-associated repository diff. **Inference:** before/after snapshots can include unrelated concurrent edits; label attribution honestly. Richer v2 diff structures remain draft. [Tool diffs](https://agentclientprotocol.com/protocol/v1/tool-calls), [stable schema](https://agentclientprotocol.com/protocol/v1/schema).

## Content, implementation and verification

Current v1 also includes capability-gated structured elicitation. This supplies a request/response shape, not a guarantee that an agent continues reasoning while an answer is pending. See the dated [question-delivery research](agent-questions.md) and the required [blocking/asynchronous UX](../design/questions.md).

Text and resource links form the prompt baseline. Image, audio and embedded-context inputs require their respective capabilities. ACP image support is independent of kitty graphics: the ADE can transmit an image without rendering it inline. Define size limits and usable fallback presentation. [Content](https://agentclientprotocol.com/protocol/v1/content).

**Proposal:** isolate protocol decoding, capability checks and lifecycle state from presentation. Evaluate published [Rust](https://agentclientprotocol.com/libraries/rust), [TypeScript](https://agentclientprotocol.com/libraries/typescript) and [community Go](https://agentclientprotocol.com/libraries/community) libraries; Bun compatibility and SDK coverage are unverified. Pin reviewed schemas/SDKs. Keep custom `_meta`/underscore-method features explicitly negotiated. [Extensibility](https://agentclientprotocol.com/protocol/v1/extensibility).

**Subsequent decisions:** ACP v1 is selected; each top-level thread has one chosen agent; existing checkout is the default with optional worktrees; handoffs are deferred. Plans, tools/MCP details, and inspectable child history are required presentation scope, with negotiated extension coverage tracked in [ACP activity research](acp-activity.md).

**Subsequently settled:** the background server owns agents, SQLite under the application home preserves state, writers queue per checkout, and turn comparisons are recorded observations rather than exclusive attribution. See [server behavior](../design/server.md), [editor collaboration](../design/editor.md), and [configuration research](acp-configuration.md). **Implementation details:** upstream restoration, direct versus client-mediated writes, effective permissions, exact adapter/dialect pins, and image limits. Optional terminal graphics are accepted in Q3, while agent image capabilities remain separate. All validation is **NOT RUN**: test version mismatch, missing capabilities, streamed updates, permission cancellation, agent crash, history replay duplication, output limits, write conflicts, unsupported content and concurrent sessions with each pinned agent/adapter.
