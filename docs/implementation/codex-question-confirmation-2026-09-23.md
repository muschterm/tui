# Codex question confirmation — 2026-09-23

## Reported behavior and diagnosis

Codex answers in continued-work mode remain **Submitted · provider confirmation unavailable** after the turn finishes. The server stores the answer and sends a native `item/tool/requestUserInput` response. Its built-in bridge receipt is emitted after a complete write to the App Server pipe, but the receipt does not establish that App Server accepted the answer. Turn completion is also insufficient: a continued-work question can be cleared while that turn ends normally.

The same evidence gap applies to blocking Codex questions. Before this review, the server promoted a blocking Codex answer to `acp-turn-confirmed` when the write receipt was followed by a normal turn end. That could claim confirmation after a response was dropped during a turn transition. This review restricts that promotion to Claude, whose bridge receipt follows a successful provider tool-result event. Codex answers retain their submitted snapshot and `acp-unconfirmed` delivery status; they are never replayed automatically.

## Pinned evidence

- Installed `codex-cli 0.156.1` generated experimental TypeScript bindings: `ToolRequestUserInputParams` includes `threadId`, `turnId`, `itemId`, `isBlocking` and `autoResolutionMs`; `ServerRequestResolvedNotification` carries only `threadId` and `requestId`. There is no answer-accepted result in that notification.
- In [Codex 0.156.1 `on_request_user_input_response`](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/app-server/src/bespoke_event_handling.rs#L1598), App Server emits `serverRequest/resolved` immediately after its callback completes, **before** parsing the response and submitting `Op::UserInputAnswer`. Parse failures become empty answers; submission failures are logged. The [official App Server documentation](https://learn.chatgpt.com/docs/app-server#toolrequestuserinput) also says the same notification is emitted when turn start, completion or interruption clears a pending request.
- The [Codex 0.156.1 request handler](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/core/src/tools/handlers/request_user_input.rs#L82) awaits a core answer and turns it into a function-tool output. A correlated output event could be stronger evidence, but the available `rawResponseItem/completed` stream is an experimental, internal opt-in at `thread/start`. The generated `thread/resume` schema has no matching opt-in, and [the listener attach path](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/app-server/src/request_processors/thread_processor.rs#L3376) does not enable it for an ordinary resumed root thread. It cannot provide a complete answer-confirmation contract for the existing resumed threads.

## Decision and validation boundary

Do not treat a pipe write, `serverRequest/resolved`, or a normal Codex turn end as answer acceptance. Keep `acp-unconfirmed` distinct from `acp-uncertain`, `acp-undeliverable` and `acp-cancelled`. The application still needs a correlated Codex event emitted after core accepts the actual answer, available across start and resume, before it can reliably show **Answered** for these requests. Existing saved answers cannot be retroactively confirmed from a completed turn.

This correction applies to new turn outcomes. It does not rewrite stored question history, including any blocking Codex answer that an earlier server version marked confirmed.

The regression test covers the former blocking false-positive path with a Codex write receipt and normal end. It is a fake ACP peer test, not a live Codex round trip.
