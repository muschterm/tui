# Agent questions

Implementation evidence (2026-09-19): the [first Go slice](go-slice.md) now exercises a fixture-driven subset of this contract. See the [validation report](../research/go-slice-validation-2026-09-19.md) for executed checks and limitations; provider/editor/real-terminal behavior below is not implied by fixture results.

Status: required by the user on **2026-09-19**. This specifies the shared UI and delivery contract; provider integration and terminal interaction are **NOT RUN**. See [source evidence](../research/agent-questions.md) for the distinction between awaiting an answer and continuing work while a later answer is delivered.

## Placement and navigation

Pending questions appear immediately above the prompt, alongside the fixed current-plan and active-subagent controls. This area is outside transcript scrolling. The prompt stays present and editable with its settings and usage; a question must not replace it or consume its draft. Keep its controls usable when the right surface is maximized. At short heights, collapse the optional bottom panel and bound/scroll question content before sacrificing answer navigation or the prompt.

One request can contain multiple questions. Show one question at a time inside an outlined container, with short question tabs and answered markers along its top. Keep the active tab visible, with a question menu only when tabs overflow. Place Back/Next beside these tabs only when a previous/next question exists; never wrap around at the ends. Preserve choices and typed answers while moving between questions. The selected tab is client-local; another client's navigation must not move this user's focus. Incomplete and answered questions remain distinguishable without relying on color alone.

Show the requesting agent or child identity and the actual question. Render supplied single choices as vertical radio options and multiple choices as vertical checkboxes, including a final Other/free-fill choice where supported. Open-ended questions use text input. Preserve required/optional rules and validation. Options… appears only when some choice content is outside the visible viewport; scrolling and keyboard focus still reach every choice. Free text must be available when the source supports it; do not invent an answer shape the agent cannot receive. Source defaults remain editable and are never already submitted answers. Identify blocking versus asynchronous behavior because it changes what the user can expect.

Back, Next, and selecting a tab only navigate. Selecting a radio option records the draft and automatically advances to the next question if present. Checkbox toggles stay on the current page so several values can be chosen. Choosing Other focuses its free-text field and does not advance with an empty answer. Open-ended answers use their text field; users can navigate through the top tabs and applicable Next control. Submit is explicit and separate. Validate required answers before submitting. For a source expecting one answer object, submit its answers together; do not turn a multi-question request into unrelated prompts. Per-question delivery requires explicit integration support. Decline, cancel and skip are distinct actions where supplied; skipping a required question must not fabricate a response.

For several requests, provide a compact Requests selector with pending count and source/status. Hide that selector when only one request is pending; do not label it Next (1). Show one request's question pages at a time instead of stacking unbounded cards. New requests remain discoverable without stealing text input or erasing another draft. Navigation and submission have keyboard and mouse paths; exact bindings remain prototype work and must respect editor/PTY focus.

When space is tight, this area contains one scrollable question or approval card alongside the compact Plan and Agents summaries. Scroll the card's content while keeping request navigation and explicit answer/approval actions reachable. The normal prompt, settings and usage remain visible. Preserve drafts when switching requests and do not use overflow handling to submit, dismiss or resolve one.

Permission requests share this area through distinct [approval cards](activity.md#approval-cards), with action details and provider-supported decisions rather than question inputs. Switching between a question and an approval preserves the question's draft and does not submit either request.

## Blocking and asynchronous requests

| Mode | Execution | Answer delivery |
| --- | --- | --- |
| Blocking | The requesting run waits at the input boundary. Its turn remains unfinished. Independently running work may continue; identify the actual scope. | Submit to that pending request. Continue through upstream resolution; an answer does not create a new turn. |
| Asynchronous | The requesting agent continues eligible work while the request remains pending. | Submit later against the same live request identity. Preserve delivery/acceptance state; storing a draft does not mean the agent received it. |

Use concise states such as “Waiting for answer” or “Answer anytime” when supported by known execution semantics. Do not infer asynchronous behavior from an `async` SDK callback, a responsive UI, or an unrelated subagent still working. If the mode is unknown, show the pending question without asserting continued execution or a pause.

Answers are separate commands from ordinary prompts. The prompt remains available: normal messages follow the prompt queue and explicit-interrupt rules; answers resolve their referenced question. Navigation and answers must not implicitly interrupt another run, advance the prompt queue, release a checkout writer lease, or authorize unrelated work. Permission approvals retain separate semantics even when sharing this presentation area.

If the agent completes, withdraws the request, changes its question, or disconnects, reconcile actual state before accepting a late answer. Do not attach it to a newer turn or reinterpret it as a fresh prompt. Accept an answer after turn completion only if the integration explicitly keeps the request open. Preserve unsent text when delivery is no longer possible and show the reason.

## Server authority and recovery

The server owns request identity, originating thread/turn/child and upstream connection scope, question revision, execution mode, lifecycle and delivery state. Preserve pending requests and accepted answers in application-home storage. Per-client drafts and navigation remain distinct from the accepted shared response. A connection-bound request must not be routed to another connection merely because a session name matches.

All attached clients see the same resolved state. Reconcile submission identity and request revision so competing clients cannot answer an already-resolved request twice. A losing or stale client sees the accepted outcome and retains any unsent draft; it must not overwrite that answer. Distinguish submitting, accepted for delivery, confirmed delivered/resolved, and failed or uncertain outcomes where supported. A local optimistic state is not provider receipt.

Reattaching to a running server restores current questions and drafts without answering or restarting work. After server restart, retained requests are history until revalidated during explicit Resume; old RPC identifiers do not automatically become live. Preserve drafts while reconciling changed or newly issued requests. No automatic answer follows from reconnect, timeout, focus changes, preselection, no connected clients, or Next. If a provider itself expires or auto-resolves a request, report that event honestly and disable stale submission controls.

## Integration requirements

Represent structured questions, multi-question payloads, choice/free-text formats, blocking behavior, continued-work asynchronous behavior, and late-answer delivery as separate capabilities. Both modes are required product targets for the initial agent examples; unsupported adapters remain recorded integration gaps before claiming parity.

Current ACP v1 includes capability-gated form elicitation. A form can be presented as question pages while its underlying request remains intact. The method alone does not guarantee continued reasoning while a response is pending. URL elicitation concerns an external interaction, not a generic asynchronous-answer mechanism. Follow the negotiated schema and its sensitive-input boundaries. [ACP evidence](../research/agent-questions.md)

A true asynchronous adapter path must establish continued useful work, the pending request's identity, a later answer reaching the intended execution context, and defined rejection/cancellation/reconnect outcomes. Ordinary prompt queuing or `turn/steer` alone does not establish that contract. Do not downgrade “Answer anytime” to an answer received only in a new turn.

## Required evidence

Shared scenarios cover one/several questions, mixed choices/free text, multiselect, required validation, Back/Next without submission, retained drafts, and explicit Submit/decline/cancel. Exercise simultaneous requests from root and child runs, blocking scope alongside other active work, real progress during async waiting, and acknowledged later answers.

Test competing clients, stale revisions, withdrawal during editing, lost acknowledgment, provider timeout, late answers, reattach and restart/Resume. Inspect long questions, narrow/short layouts, surface maximization, transcript scrolling and preserved prompt drafts. Terminal checks and provider checks are separate; screenshots establish neither.

## Question-card refinement — 2026-09-20

These refinements are accepted from the user's live review. The Go implementation
uses a maximum 12-row card including borders, top tabs and fixed Submit; short
windows reduce its content viewport first. The optional text field grows to three
rows before scrolling. These are prototype dimensions, not provider limits. The
composer, its settings, usage and actions remain visible. Arrow keys move through
focused choices and reveal offscreen options; Space/Enter select or toggle. F6
reaches the question content or active text field; Tab reaches controls.

The fixture contract supports additive structured answers with separate selected
choices and free text. Legacy string answers and approval responses remain
compatible. Validation rejects unknown/duplicate options, unsupported answer
shapes and missing required values. A checked Other field must contain text or
be deselected, including on an otherwise optional question. Selection, navigation,
reconnect and draft restoration never send a response.

Drafts retain request identity, revision and question schema separately from the
accepted server answer. A changed revision/schema uses a fresh draft; the prior
draft remains in client storage for recovery, rather than being attached silently
to a changed question. Older text drafts are upgraded once at load. Provider
question schemas, real asynchronous delivery and connection-bound identities
remain integration work; fixture acceptance proves only this local contract.

## Submit feedback and older-server compatibility

A click on Submit must produce visible progress, acceptance or a reason that the
answer could not be sent. Show validation and server errors in the request card
above its fixed actions; the Submit hover label must not hide them. Keep the
card bounded and the composer visible. Validation selects the incomplete question
and clears when its draft is edited. Submission failures remain until another
explicit attempt or a changed request. Scope feedback by thread, request identity
and revision so it cannot appear on a different question request.

Show pending delivery and preserve its command identity for explicit Retry if
acceptance is uncertain. Missing capability, disconnected state, pending commands
and Resume requirements have visible reasons. Submit never implicitly resumes
saved execution. A restart still requires the existing explicit revalidation flow.

Untyped legacy questions (no question Kind) accept one choice or free text per
question. Send their responses through the original Answers string array so an
already-running first-slice server can accept them. Explicitly typed requests
keep structured QuestionAnswers, including multiple choices and Other text. The
legacy encoding is chosen from the question schema before dispatch; do not blindly
retry a potentially accepted command with a changed payload or identity.
