# Agent question interfaces

Source-checked **2026-09-19**. Local T3 Code reference: `72c44a847c0a76f33b0d21f47548125b7032ec35`. No agents, integrations or terminal interactions were executed. These findings support the [question contract](../design/questions.md); they do not establish installed-adapter parity.

## ACP v1

Current [ACP v1 elicitation](https://agentclientprotocol.com/protocol/v1/elicitation) documents `elicitation/create`. Clients explicitly advertise `clientCapabilities.elicitation.form` and/or `.url`; `elicitation: {}` alone does not enable form mode. Requests specify a mode and connection-bound session or request scope. Form input is a restricted flat object schema, with accept, decline or cancel responses. Fields can be paginated without splitting the response identity. Validate and allow review before submission; form mode is not for secrets/credentials.

This is current v1 functionality, not a blanket draft-only gap. It is request/response and does not promise continued agent execution while awaiting an answer. URL-mode `elicitation/complete` concerns external-workflow completion, not a generic late-answer channel. Pin SDK/adapter revisions and verify advertised support rather than assuming every v1 implementation includes it.

## Codex

The [official App Server reference](https://learn.chatgpt.com/docs/app-server) documents experimental `item/tool/requestUserInput`, resolved-request notifications and an optional automatic-resolution timeout. `turn/steer` supplies input to an active turn; alone it is not asynchronous question correlation/resolution. The inspected interface did not establish a native `request_user_input_async` equivalent. A desktop host/harness tool is not automatically part of headless Codex or ACP.

The local T3 handler at [CodexSessionRuntime.ts:2097](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/server/src/provider/Layers/CodexSessionRuntime.ts#L2097) handles the request and awaits a deferred answer. This establishes a waiting interaction, not continued-work semantics for that request.

## Claude

The [Claude Agent SDK user-input documentation](https://code.claude.com/docs/en/agent-sdk/user-input) routes `AskUserQuestion` through `canUseTool`, returning answers through `updatedInput`. It documents one to four questions, choices and multiselect, with current restrictions for Agent-tool subagents. Bare `claude -p` parity was not established by this source.

T3's local [ClaudeAdapter.ts:3861](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/server/src/provider/Layers/ClaudeAdapter.ts#L3861) similarly awaits a deferred answer and handles abort cleanup. An asynchronous callback lets the application wait responsively; it does not prove continued agent execution by Claude.

## Presentation versus execution

T3's [ComposerPendingUserInputPanel.tsx:50](https://github.com/pingdotgg/t3code/blob/72c44a847c0a76f33b0d21f47548125b7032ec35/apps/web/src/components/chat/ComposerPendingUserInputPanel.tsx#L50) tracks question progress and advancement. This informs paging above the composer. Our tabs, Back/Next, explicit Submit, stable prompt and fixed active-work controls follow the user's contract rather than every upstream convention.

**Integration conclusion:** blocking structured requests have concrete source paths. True asynchronous questions still require a validated adapter/backend path for continued work, request correlation and acknowledged later-answer delivery. Independent work around a waiting request is insufficient evidence. Preserve separate capabilities and report gaps rather than replacing answers with ordinary queued prompts.
