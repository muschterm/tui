# Implementation sequence

Status: the user asked the assistant to choose the easiest reference app to implement first. **Go is selected first.** The accepted blueprint is documented and a [first Go server/shell slice](go-slice.md) now implements a bounded subset of steps 1–3 with synthetic activity for later presentation work. It does not complete those steps or establish real provider/editor/Git/terminal integration. Deferred choices are resolved in their relevant prototype or implementation slice.

## Why Go first

Bubble Tea v2 with Bubbles and Lip Gloss gives the selected design an established update/render/effect framework and component ecosystem. Ratatui leaves more application event/focus assembly to us. The chosen Bun stack adds explicit Bun/Ink runtime validation and mouse/graphics integration work; Bun.Terminal addresses PTY I/O rather than a complete embedded terminal view. On the current evidence, Go is the most straightforward first reference. This is an engineering judgment, not a measured performance or effort benchmark. [Stack research](../research/language-stacks.md)

Go-first does not relax the shared interaction or visual requirements. It establishes a concrete reference for the other idiomatic implementations. Each language supplies a complete server and TUI against the shared client/server contract. Do not scaffold three general-purpose frameworks or force Go's internal types into Rust or TypeScript. The later collaboration requirement adds a significant shared-document boundary; Go remains first, with feasibility checked before expanding the editor.

## Vertical slices

1. **Validate the risky boundaries.** Check Go keyboard/mouse/resize behavior through required terminal paths. Probe shared-document convergence, client-specific undo, and autosave/external-write reconciliation. Verify that pinned ACP adapters expose the configuration, plans, tools, child history, usage, approvals and question modes they claim to supply, including real continued-work async answer delivery. Use recorded streams for deterministic cases and distinguish them from live integration evidence. Check Bun/Ink and embedded-terminal feasibility before investing in its full port.
2. **Build the smallest server/attach slice.** Use an application-specific home and SQLite, single-server discovery, authenticated loopback HTTP/WebSocket, and native Go lifecycle ownership. Demonstrate start/status/stop, two clients, coherent catch-up, durable command acknowledgment, graceful cancellation, and explicit Resume after restart. TUI disconnect leaves work running. Exact wire messages and migrations become versioned artifacts in this slice.
3. **Build and tune the Go shell.** Establish left navigation, center, optional bottom panel, right surfaces, focus, mouse/keyboard resizing, hide/expand, dark/light themes, and narrow layouts. Exercise thread-view restoration and compact plan/running-agent/request overflow while preserving prompt, settings and usage. Use the interactive prototype to choose bindings and numeric geometry. Keep client-local view state distinct from shared domain state.
4. **Add structured activity and the ADE.** Render messages, plans, tools, MCP, and nested subagent transcripts with their Agents/Plan/Activity surfaces. Connect ACP v1 using the agent-first configuration flow, one chosen agent per thread, editable/removable/reorderable prompt queues, checkout writer queues, and optional worktrees. Implement captured prompt context/settings, stream-only usage, recorded turn comparisons, questions/approval cards, background attention, and required Codex/Claude coverage with pinned adapters. Exercise dispatch races, streaming, replay, long output, and unavailable data.
5. **Add collaborative files and the terminal.** Implement shared text documents, presence, own-edit undo, autosave, and external-change resolution before growing the browser/editor. Add Markdown modes. Implement independent server-owned terminals in the right and bottom panels, each with one input/resize controller and a bounded emulator/rendering surface. Closing a terminal ends its session; panel toggles preserve it. A PTY or text dump alone does not complete this feature.
6. **Complete Git workflows.** Start with status/graph/diffs, then add staging, commits, branch actions, soft reset, explicit fast-forward-only pull, rebase, and manual resolution. Agent resolution edits files and stops for review before staging/continuation. Coordinate Git writes with collaborative documents and preserve unrelated work.
7. **Verify Go and document adoption.** Apply [shared quality checks](quality.md), actual terminal/SSH/tmux checks, live provider evidence, multi-client failure cases, and visual review. Record versions and gaps. Show how another domain supplies navigation, primary workflow, surfaces, and its own application identity. Document actual commands when tooling exists.
8. **Build the other complete references.** Implement native Rust and Bun servers and their Ratatui/React-Ink clients idiomatically. Reuse wire fixtures and observable behavior scenarios, including collaboration. Choose their order after Go and Bun feasibility results; all three remain required. Cross-language compatibility applies to the selected wire contract, not implicit interchange of database files.

## Completion evidence

The queued-message [Steer contract](activity.md#steering-a-queued-message) is a
shared requirement for all three apps. The Go fixture exercises it before real
providers: active-turn identity, retained captures, stale-action rejection and
deduplicated receipts. Step 1/4 adapter probes must separately verify same-turn
steering, supported content/settings and uncertain-delivery reconciliation;
implement honest unavailability when unsupported. This does not replace the
separate async-question probe or permit a Stop-and-Send fallback.

The first interactive review also requested [clipboard context and centered read-only previews](activity.md#clipboard-intake-and-read-only-previews). Implement this as a bounded part of captured-context work in step 4: first durable artifact intake and retrieval, then text/Markdown viewers and image clipboard/thumbnail/graphics integration with actual terminal checks. The viewer does not depend on step 5's collaborative editor. Agent delivery still needs negotiated input capabilities and must not be inferred from a working local preview. See the [current feasibility and integration gaps](../research/clipboard-previews-2026-09-19.md); this staging does not claim the functionality already exists.

Each slice needs observable behavior and relevant validation. Simulated agent output is useful test data but does not establish working provider integration. Library support claims do not establish the full terminal matrix. A reference app is not complete while mouse paths, file-write safety, source-supported child history, or agreed fallback states are absent.

The final documentation should let an agent start a new domain-specific application by following the shared specification, copying a small working reference, and understanding which contracts the application must supply. Extract reusable packages only when those real usages clarify the boundary.

The 2026-09-20 sidebar/settings slice adds local thread search, project identity
controls, server-owned defaults and confirmed project removal. Restart remains
Resume-gated by default, with explicit opt-in only for verified eligible Demo
continuation. Real worktree provisioning and provider recovery remain integration
work. See [settings](settings.md) and [validation](../research/go-sidebar-settings-2026-09-20.md).

The draft-first refinement keeps New thread local until atomic first Send, keeps the active settings row read-only, and allows reading Closed threads before explicit Reopen or atomic reopen-and-send. Its read-only checkout/branch context does not complete the Git workflows in step 6. Real Codex and Claude connections were already required in step 4; selectable fixture labels do not add those integrations. See [thread configuration](thread-configuration.md), [thread lifecycle](threads.md) and [current slice](go-slice.md#draft-first-creation-and-closed-composer).
