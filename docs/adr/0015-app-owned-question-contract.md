---
status: accepted
---

# Standardize questions in the application and translate delivery in adapters

Confirmed 2026-09-22: all frontends use one application-owned question model,
presentation and answer action. The server identifies and revisions each
request, records accepted answers, and routes delivery through the originating
adapter. Users never choose a transport or format a provider-specific reply.
This preserves a consistent UI while allowing different agent mechanisms.

Prefer native, structured, correlated question responses. An explicitly
supported structured-text fallback may end a turn and deliver the answer as a
new user turn, while remaining an answer to the original application request.
That fallback is not a native pending tool response, same-turn steering or
continued-work asynchronous delivery. Show whether the requesting agent is
waiting or continuing; only offer answer shapes the integration can deliver.
Do not change a failed native response into a new prompt as an implicit fallback.

Approvals remain distinct authorization requests with the provider's actual
action, scope and supported decisions, even when sharing UI components with
questions. Do not turn an ordinary question answer into tool authorization.

The [question specification](../design/questions.md) defines lifecycle,
recovery and presentation. This decision qualifies its earlier unconditional
"an answer does not create a new turn" rule only for the declared turn-ending
fallback; it does not relax explicit Submit, request correlation, deduplication,
queue integrity or the separate asynchronous-question target. Implementation
and wire details remain in the [handoff](../implementation/agent-integration-handoff.md).
