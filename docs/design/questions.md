# Agent questions

Implementation evidence (2026-09-19): the [first Go slice](go-slice.md) now exercises a fixture-driven subset of this contract. See the [validation report](../research/go-slice-validation-2026-09-19.md) for executed checks and limitations; provider/editor/real-terminal behavior below is not implied by fixture results.

Status: required by the user on **2026-09-19**, with normalization accepted **2026-09-22**. This specifies the shared UI and delivery contract. General native provider questions, turn-ending fallback delivery and true asynchronous answers remain unverified in this app. Existing fixture terminal checks and live approval observations are separate evidence; see [the ACP validation report](../research/go-acp-2026-09-22.md). See [source evidence](../research/agent-questions.md) for the distinction between awaiting an answer and continuing work while a later answer is delivered.

## One application contract, multiple delivery mechanisms

**Accepted 2026-09-22:** every frontend uses one question model, presentation and
request-specific answer action. The server owns request identity, revision,
origin and delivery route; adapters translate supported provider mechanisms.
Users never choose a transport or format a provider-specific answer. Prefer
native structured questions and correlated responses. See
[ADR 0015](../adr/0015-app-owned-question-contract.md).

An explicitly supported structured-text fallback may produce a question at the
end of a turn and deliver its answer through a correlated new upstream turn.
The application still records an answer to the original request, not an
ordinary prompt submission. Preserve the same card, supported answer shapes,
explicit Submit and history; show whether the agent is waiting or continuing
work. Transport names do not belong in normal user flows.

Enable that fallback only with a bounded schema/parser and a defined delivery
contract. Partial, malformed, duplicate, quoted/example and tool-output blocks
must not accidentally create actionable requests. Native response failure or
uncertain receipt never silently selects fallback delivery. Approvals retain
their separate authorization semantics and provider-supported scope/choices.

## Placement and navigation

The question/approval card shares the prompt's rounded outline and stable interior
background. Question tabs, Back/Next arrows, overflow, submission/approval buttons
and the inline Requests selector use single-row square fills. Rest is neutral,
hover stronger neutral, and selection accent plus bold. Keyboard focus adds its
mark in the leading cap independently (2026-09-22; previously an underline).
Selected tabs keep their treatment when a neighbor is
hovered. Reserved bracket end cells preserve compact controls in monochrome.
The Go card is bounded to 13 rows including its interior header (12 before the
[2026-09-23 refinement](#question-card-refinement--2026-09-23)), with scrollable
content; plain and limited-color modes preserve geometry and hit regions. See the
accepted [component rule](components.md).

Pending questions appear immediately above the prompt, alongside the fixed current-plan and active-subagent controls. This area is outside transcript scrolling. The prompt stays present and editable with its settings and usage; a question must not replace it or consume its draft. Keep its controls usable when the right surface is maximized. At short heights, collapse the optional bottom panel and bound/scroll question content before sacrificing answer navigation or the prompt.

One request can contain multiple questions. Show one question at a time inside an outlined container, with compact, individually filled question buttons and answered markers along its top. Keep the active tab visible, with a question menu only when tabs overflow. Use filled left/right arrows for Back/Next in fixed slots at the ends of the row, with plain `<`/`>` fallbacks. Hide an arrow when that direction has no question, preserving its empty slot; tabs must not use that space. Never wrap around at the ends. When the row overflows, show a contiguous group containing the current question and its nearest neighbors that fit. Arrow navigation selects and reveals the adjacent question. Reserve answered-marker space so progress does not shift the buttons. Preserve choices and typed answers while moving between questions. The selected tab is client-local; another client's navigation must not move this user's focus. Incomplete and answered questions remain distinguishable without relying on color alone.

Show the requesting agent or child identity and the actual question. Render supplied single choices as vertical radio options and multiple choices as vertical checkboxes, including a final Other/free-fill choice where supported. Open-ended questions use text input. Preserve required/optional rules and validation. Options… appears only when some choice content is outside the visible viewport; scrolling and keyboard focus still reach every choice. Free text must be available when the source supports it; do not invent an answer shape the agent cannot receive. Source defaults remain editable and are never already submitted answers. Identify blocking versus asynchronous behavior because it changes what the user can expect.

Back, Next, and selecting a tab only navigate. Selecting a radio option records the draft and automatically advances to the next question if present. Checkbox toggles stay on the current page so several values can be chosen. Choosing Other focuses its free-text field and does not advance with an empty answer. Open-ended answers use their text field; users can navigate through the top tabs and applicable Next control. Submit is explicit and separate. Validate required answers before submitting. For a source expecting one answer object, submit its answers together; do not turn a multi-question request into unrelated prompts. Per-question delivery requires explicit integration support. Decline, cancel and skip are distinct actions where supplied; skipping a required question must not fabricate a response.

Give Submit, Options, Requests and approval choices the same compact button treatment where present, with clear gaps and hover/focus feedback; use an overflow menu before choices can overlap. For several requests, provide a compact Requests selector with pending count and source/status. Hide that selector when only one request is pending; do not label it Next (1). Show one request's question pages at a time instead of stacking unbounded cards. New requests remain discoverable without stealing text input or erasing another draft. Navigation and submission have keyboard and mouse paths; exact bindings remain prototype work and must respect editor/PTY focus.

In the [single-column phone layout](layout.md#single-column-layouts-on-small-screens), this card lives in Conversation; the attention bell remains available in the other columns and reveals it without losing answer drafts. When space is tight, this area contains one scrollable question or approval card alongside the compact Plan and Agents summaries. Scroll the card's content while keeping request navigation and explicit answer/approval actions reachable. The normal prompt, settings and usage remain visible. Preserve drafts when switching requests and do not use overflow handling to submit, dismiss or resolve one.

Permission requests share this area through distinct [approval cards](activity.md#approval-cards), with action details and provider-supported decisions rather than question inputs. Switching between a question and an approval preserves the question's draft and does not submit either request.

## Answered questions in conversation history

**Accepted, 2026-09-20; clarified 2026-09-22:** once the server has durably
accepted a response snapshot, show one compact, read-only card in the chat
conversation at that submission's recorded chronology point. Label the card
**Answered** only after authoritative resolution confirms the response. While
provider delivery or resolution is unconfirmed, show the captured submitted
answers with a truthful status such as **Submitted · delivery unconfirmed** or
**Submitted · delivery uncertain**; known delivery failure also retains the
answers with its failure status. Later evidence updates this same card in place.
The response belongs to the original request and its thread/turn; it is not an
ordinary prompt, queued message or Steer action. For a declared turn-ending
fallback, also retain its link to the upstream continuation turn without
duplicating the user-visible answer in history.

Use one rounded outlined container with a stable background, following the
[component rule](components.md). Give it a quiet Answered header and a completion
indicator for the response, not the entire agent turn. Pair each original question
with its submitted answer in question order, using spacing and text hierarchy
without repeated You/Agent labels or nested boxes. Retain meaningful requesting
child identity when needed to distinguish the source.

Show each original question with all of its available choice labels. Mark every
selected choice, including each selected value for a multiple-choice answer,
and show the accepted free text, including Other text; recede unselected choices
without relying on color alone. Preserve explicit optional omissions/skips as
such; never display defaults or a losing client's unsent draft as the submitted
answer. Retain the question wording/options as answered rather than relabelling
old answers using a newer request schema.

Short Q&A stays readable directly in the card. Bound long previews and show Expand
only when content is hidden, indicating additional questions or text. Expansion
reveals the full accepted Q&A inline in the transcript; Collapse restores the
compact presentation. Mouse and keyboard reach both actions, and all text remains
selectable/copyable. Transcript scrolling handles expanded content; history never
occupies the fixed pending-request area or reduces the prompt/settings/usage
footer. Preserve the reading anchor when expanding/collapsing and restore local
expansion/reading state with the thread.

Place the card at the response's recorded submission point in conversation
chronology, retaining its connection to the originating request. If later
evidence confirms resolution, update the existing card rather than inserting a
second copy. Later agent work follows normally. Incoming resolution must not
steal focus or pull someone away from older text they are reading. The card has
no answer-editing inputs, Submit, queue or Steer controls; viewing it cannot
change or resend the answer.

Submitting, accepted-for-delivery and uncertain/failed delivery must not appear
as successfully Answered. Keep the existing pending/delivery feedback until the
authoritative outcome is known. Declined, cancelled, withdrawn, expired or
provider-resolved requests retain their actual outcomes and must not imply the
user supplied an answer. If an integration distinguishes answer acceptance from
confirmed delivery, preserve that distinction visibly.

Retain one history entry per answered request revision, backed by the accepted
question-and-answer snapshot and resolution identity/order. Repeated snapshots,
lost receipts, reconnect and restart must update/recover that entry without
duplicates or resubmission. All clients see the accepted response, while losing
or stale local drafts remain separately recoverable. For older records, show
only preserved content and known ordering; do not invent missing answers,
request wording, delivery confirmation or timestamps.

**Implementation evidence (2026-09-22):** the Go slice records a stable
transcript marker with each accepted question response and renders a read-only
question/answer card at that position. Server-confirmed fixture resolutions use
**Answered**; native ACP answers remain visibly submitted while delivery is
unconfirmed or uncertain. Ordinary submitted records without provider receipts
use a neutral “Submitted · provider confirmation unavailable” header; this is
an integration limitation, not a pending action or confirmed failure. Actual
uncertain and failed delivery retain their distinct warning states. If an older or trimmed snapshot lacks its chronology
marker, the card follows the last retained activity of its originating turn and
identifies that its exact earlier position is unavailable. If no activity from
that turn remains, it appears under Earlier history before the retained
conversation. It must not follow every newer message at the bottom. Long-card expansion and per-thread
expansion restoration use a client-local control and saved thread view.
Preserving the reading position while expanding or collapsing remains
incomplete; ordinary transcript scrolling exposes the preserved content in the
current slice.

## Blocking and asynchronous requests

| Mode | Execution | Answer delivery |
| --- | --- | --- |
| Blocking | The requesting run waits at the input boundary. Its turn remains unfinished. Independently running work may continue; identify the actual scope. | Submit to that pending request. Continue through upstream resolution; an answer does not create a new turn. |
| Asynchronous | The requesting agent continues eligible work while the request remains pending. | Submit later against the same live request identity. Preserve delivery/acceptance state; storing a draft does not mean the agent received it. |
| Turn-ending fallback | The originating turn has ended and left an application-owned question pending. Do not depict a still-running blocked tool. | A request-specific answer may start a correlated upstream continuation turn under the declared adapter contract. This is not asynchronous or same-turn delivery. |

Use concise states such as “Waiting for answer” or “Answer anytime” when supported by known execution semantics. Do not infer asynchronous behavior from an `async` SDK callback, a responsive UI, or an unrelated subagent still working. If the mode is unknown, show the pending question without asserting continued execution or a pause.

Answers are separate commands from ordinary prompts. The prompt remains available: normal messages follow the prompt queue and explicit-interrupt rules; answers resolve their referenced question. Navigation and answers must not implicitly interrupt another run, advance the ordinary prompt queue, release a checkout writer lease, or authorize unrelated work. For a declared turn-ending fallback, the server must coordinate the continuation with queued work, captured settings, checkout writer admission and Stop/Resume gates before dispatch. Exact scheduling and stale-answer rules must be settled before enabling that route. Permission approvals retain separate semantics even when sharing this presentation area.

If the agent completes, withdraws the request, changes its question, or disconnects, reconcile actual state before accepting a late answer. Do not attach it to an unrelated newer turn or reinterpret it as a fresh ordinary prompt. Accept an answer after turn completion only if the integration explicitly keeps the request open, including the declared turn-ending fallback. Preserve unsent text when delivery is no longer possible and show the reason.

## Server authority and recovery

The server owns request identity, originating thread/turn/child and upstream connection scope, question revision, execution mode, delivery route, lifecycle and delivery state. Preserve pending requests and accepted question/answer snapshots in application-home storage. Per-client drafts and navigation remain distinct from the accepted shared response. A connection-bound request must not be routed to another connection merely because a session name matches. Stable question/option identities must survive duplicate display labels and provider payload translation.

All attached clients see the same resolved state. Reconcile submission identity and request revision so competing clients cannot answer an already-resolved request twice. A losing or stale client sees the accepted outcome and retains any unsent draft; it must not overwrite that answer. Distinguish submitting, accepted for delivery, confirmed delivered/resolved, and failed or uncertain outcomes where supported. A local optimistic state is not provider receipt.

Reattaching to a running server restores current questions and drafts without answering or restarting work. After server restart, retained requests are history until revalidated during explicit Resume; old RPC identifiers do not automatically become live. Preserve drafts while reconciling changed or newly issued requests. No automatic answer follows from reconnect, timeout, focus changes, preselection, no connected clients, or Next. If a provider itself expires or auto-resolves a request, report that event honestly and disable stale submission controls.

**Delivery evidence (2026-09-23):** an answer's delivery status reflects the
evidence available when its turn ends, and a restart never rewrites the outcome
of a turn that had already ended.

| Evidence | Recorded status |
| --- | --- |
| Answer accepted, not yet handed to the connection | Accepted; uncertain if the turn fails or the server restarts |
| Handed to the connection, no further evidence | Unconfirmed: provider confirmation unavailable |
| Blocking question, built-in bridge receipt, then the same turn ends without error or cancellation | Resolved: Answered |
| Handed off, then the turn fails or the server restarts mid-turn | Uncertain |
| Handed off, then the turn is cancelled | Unconfirmed |
| Decline or cancel handed off, then the same turn ends normally | Unconfirmed: receipts cover accepted answers only (the failure and restart rows still apply) |

A built-in bridge sends the pinned `tui-go.question-delivery.v1` receipt only
after the provider took the answer: Claude reports its own successful
`AskUserQuestion` tool result; Codex's response is written to a native request
that App Server has not resolved or withdrawn. The receipt precedes the turn's
prompt response on the ordered ACP stream. Neither the receipt nor turn
completion alone settles delivery. A continued-work question can outlive its
turn's normal end, so it stays unconfirmed. Receipts from other adapters are
ignored.

## Integration requirements

Represent structured questions, multi-question payloads, choice/free-text formats, blocking behavior, continued-work asynchronous behavior, turn-ending fallback, and late-answer delivery as separate capabilities. Native blocking and true asynchronous behavior remain required product targets for the initial agent examples; the fallback does not satisfy them. Unsupported adapters remain recorded integration gaps before claiming parity.

Current ACP v1 includes capability-gated form elicitation. A form can be presented as question pages while its underlying request remains intact. The method alone does not guarantee continued reasoning while a response is pending. URL elicitation concerns an external interaction, not a generic asynchronous-answer mechanism. Follow the negotiated schema and its sensitive-input boundaries. [ACP evidence](../research/agent-questions.md)

A true asynchronous adapter path must establish continued useful work, the pending request's identity, a later answer reaching the intended execution context, and defined rejection/cancellation/reconnect outcomes. Ordinary prompt queuing or `turn/steer` alone does not establish that contract. Do not downgrade “Answer anytime” to an answer received only in a new turn.

## Required evidence

Shared scenarios cover one/several questions, mixed choices/free text, multiselect, required validation, Back/Next without submission, retained drafts, and explicit Submit/decline/cancel. Exercise simultaneous requests from root and child runs, blocking scope alongside other active work, real progress during async waiting, and acknowledged later answers.

Test competing clients, stale revisions, withdrawal during editing, lost acknowledgment, provider timeout, late answers, reattach and restart/Resume. Inspect long questions, narrow/short layouts, surface maximization, transcript scrolling and preserved prompt drafts. Terminal checks and provider checks are separate; screenshots establish neither.

Exercise the same UI against native, fallback and verified async routes. For
fallback, verify bounded parsing, explicit adapter capability/configuration, original-request correlation,
continuation settings, answer-versus-queue/Stop races and no duplicate dispatch
after uncertain delivery. A local answer commit or successful pipe write alone
must not become confirmed Answered history.

For answered-history cards, verify exact single/multiple/free-text/Other answers
and optional omissions, original question snapshots, source identity and response
chronology. Exercise compact/expanded states, mouse/keyboard access, copying,
dark/light and narrow layouts, reading-anchor and per-thread restoration. Confirm
one entry across competing clients, duplicate snapshots, retries and recovery;
pending or failed delivery must never gain a successful Answered label. Viewing,
expanding and copying history must neither submit answers nor change the prompt.

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

Drafts retain request identity and question schema separately from the
accepted server answer. A changed schema uses a fresh draft; the prior draft
remains in client storage for recovery while the request is pending, rather than
being attached silently to a changed question. (Refined 2026-09-23: a
revision-only change keeps the draft, and the revision guards submission only;
see [below](#question-card-refinement--2026-09-23).) Older text drafts are upgraded once at load. Provider
question schemas, real asynchronous delivery and connection-bound identities
remain integration work; fixture acceptance proves only this local contract.

## Question-card refinement — 2026-09-23

Accepted by the parent session from a review of the Go card, informed by T3
Code; this repository's specification governs. It keeps the rounded outline,
fixed-slot Back/Next arrows, vertical radio/checkbox rows, indented Other field,
explicit Submit and bounded drafts, and changes the following.

- **Interior header.** The source row moves inside the outline as its first
  interior row: an icon slot (question or approval glyph; `?`/`!` in the plain
  symbol set), the agent's display name, then ` · ` and the mode in muted ink.
  Blocking modes (Waiting for answer, Approval required, Awaiting Resume) stay
  gold. The top border is an unbroken rounded run again. The header still opens
  request details; it is an unfilled text control: hover and focus embolden and
  lift it, and keyboard focus marks the gutter cell before its icon. Its right
  end holds the Requests N selector when several requests are pending, or
  otherwise, only while the tab row hides tabs, a muted `n of N` counter.
- **Tabs.** Each question tab is ` <label> <mark> `: the answered-marker cell is
  always reserved, so answering never moves the label. The marker uses the
  check icon and its plain fallback. Selected tabs keep accent fill plus bold;
  keyboard focus marks the leading end cap.
- **Question and choices.** The question text is bold. Each choice keeps the
  gutter cell for the focus mark, then its glyph, one gap and its label, with a
  hanging indent for wrapped rows. At rest the glyph is muted; selection is an
  accent glyph plus bold label. Hover and keyboard focus fill only the
  glyph-to-label extent with the stronger neutral, never the full row, and a
  selected row keeps its treatment under hover. The same extent is the hit
  target.
- **Answer field.** When Other is chosen, or for an open-ended question, the
  one-to-three-row field sits below, indented under Other's label (or the
  question text), on the input background. Its scrollbar shares the body's
  scrollbar column.
- **Actions.** The last interior row holds Options… at the left when choices are
  offscreen, the notice beside Submit when it fits (otherwise on its own row
  above), and the primary Submit at the right. Submit renders disabled — muted,
  without hover fill or primary ink — while the thread needs Resume, the client
  is disconnected or an answer submission is in flight, and the notice names the
  reason. Activation still reports that reason and sends nothing.
- **Bound.** The Go card is at most 13 rows; short windows shrink the content
  viewport first, keeping at least one content row and one answer row.
- **Answered history.** Answered/submitted cards in the transcript span the
  prompt outline's extent, like the user message box, instead of the former
  end-aligned inset. When the transcript overflows, its scrollbar takes the
  pane's gutter column to the right of that extent; it never paints over a
  card's border or a user box's tint, so their right edges stay aligned with
  the prompt outline. Where that gutter abuts the right pane divider, only the
  thumb is drawn (the blank track still pages), so the two never form a double
  rule.
- **Choice markers.** A selected choice is marked the same way wherever it is
  shown — pending card, answered card, its compact preview and copied text.
  Single choice, Other… included, uses the filled-dot radio (Nerd Font
  `fa-dot_circle_o`, `(*)` in the plain symbol set; `◉`/`○` when copied);
  multiple choice uses the checked box (`fa-check_square`, `[x]`; `☑`/`☐` when
  copied). A free-text Other answer therefore reads `◉ Other · <text>` beside
  `○` siblings. The check icon is reserved for a tab's answered marker and for
  an open-ended question's answer, which has no choice control. Earlier Go
  builds marked a free-text Other answer in history with the check.
- **Outlines.** The pending card and answered card use the prompt's rounded
  outline: one ink on all four sides, border cells on the canvas and a stable
  interior fill inside them. The pending card's outline is the rest ink, or the
  prompt's focused ink while keyboard focus is inside the card; hovering or
  focusing its inner controls never recolors it, since those controls carry
  their own state. The answered card always uses the rest ink; its delivery
  status colours only the header text (green Answered, gold/red/muted
  Submitted states), never one edge. Its Expand/Collapse row keeps a blank
  padding cell after the border for the focus mark.

Keyboard bindings in the card (prototype choices, like the rest of the Go
bindings):

| Focus | Key | Result |
| --- | --- | --- |
| Back, Next, a tab or the header | Enter | Navigate only; focus stays on the control, or moves to the active tab when the arrow disappears at an end |
| Answer field | Enter | Next question when one follows; otherwise validated Submit (an invalid answer shows the notice and sends nothing) |
| Answer field | Shift+Enter / Ctrl+J | Insert a newline |
| Answer field | Esc | Return to Other's row, or to the active tab/header for open-ended questions; the draft is kept |
| Anywhere in the card | Ctrl+S | Validated Submit of this request; never sends the ordinary prompt. An approval card asks for an explicit choice instead |
| An option row, a tab or the header | 1–9 | Select (radio) or toggle (checkbox) option N, following the same advance rule as a click; Other focuses its field. Digits type normally in text fields and the composer |
| First option / active tab or header | Up / Down | Move between the options and the tab (or header when there are no tabs) |
| Last option | Down | The visible answer field, otherwise Submit; Down on the field's last row reaches Submit and Up from Submit returns |

Tab/Shift+Tab traversal and F6 are unchanged. Drafts are keyed by request
identity and question schema: a revision-only bump (for example fixture Resume)
keeps the draft, while the revision still guards the submission. Drafts of the
loaded revision under the former revision-bound key migrate once at load; other
legacy drafts are dropped with pruning. After each snapshot, drafts whose request
is no longer live (pending or submitted) are pruned; a changed schema of a still-pending request
keeps the previous draft for recovery. A stale question index is clamped, so the
card renders the last question rather than failing. Decline/Cancel actions and
structured option descriptions follow in the
[actions refinement](#question-actions-and-option-descriptions--2026-09-23). See the
[implementation note](../implementation/question-card-2026-09-23.md).

## Question actions and option descriptions — 2026-09-23

Accepted by the parent session; it extends the card refinement above.

- **Offered actions.** A question request lists the non-answer responses its
  upstream contract supports (`Request.Actions`, from `decline` and `cancel`).
  Only offered actions are shown; a request that offers none shows only Submit.
  ACP form elicitation (Claude) offers both. Codex `request_user_input` offers
  none (see [Built-in Codex native questions](#built-in-codex-native-questions--2026-09-22)).
- **Actions row.** Decline sits directly left of Submit. Cancel sits left of
  Decline while the row has room; otherwise it moves into a More… menu, and
  when even Decline does not fit beside Submit both move there. Submit is never
  displaced. Decline, Cancel and More… use Submit's disabled rules (Resume
  needed, disconnected, answer in flight) and its busy gate, feedback path and
  hidden-work refusal. They are reached by Tab/Shift+Tab and activated with
  Enter; Up from them returns to the answer field or last option as from Submit.
  No new chord is added.
- **No answer, drafts kept.** A decline or cancel sends `request.answer` with
  `RequestAction` and no answers. Every question draft is left untouched, so a
  rejected or failed action can be followed by an ordinary answer; drafts are
  pruned only when the request itself stops being live, as after an answer.
- **Server rules.** The action must be one the request offers, carry no
  structured or legacy answers, and target a pending request at its current
  revision; it uses the same submission identity, retry dedupe, durable
  acceptance and delivery states as an answer. The chosen action is recorded on
  the request (`Request.Action`). The server's own withdrawal of a request
  (stale callback, disconnect, cancelled turn) also answers the peer
  `{"action":"cancel"}` but records no `Action`; it is not a user choice.
- **Delivery evidence.** The built-in bridge receipt covers accepted answers
  only, so a decline or cancel stays at its handed-off status (unconfirmed)
  even when its blocking turn completes. The fixture resolves it as it resolves
  an answer.
- **Option descriptions.** Supplied option detail is structured
  (`Question.OptionDescriptions`, parallel to `Options`) instead of being
  appended to the question text. The card paints each non-empty description as
  a muted, wrapped row under its option, indented to the label column. These
  rows count in the card's row budget and scroll with the body; they carry no
  hover, focus or hit target, so an option's fill stays on its glyph-to-label
  extent. The Options… menu and answered history list labels only. A
  description slice that is not parallel to `Options` is rejected by
  validation and ignored by the client.
- **History.** A declined or cancelled request keeps its answered-history card
  with the original questions and their offered labels, all unselected, and no
  answer rows. Its header reads Declined or Cancelled in muted (not green) ink
  once resolved, and otherwise keeps the delivery sub-status of the answer
  copy: for example `Declined · provider confirmation unavailable` or
  `Cancelled · not delivered` (red). Activity detail adds
  `Response: Declined · no answer sent`.

See the [implementation note](../implementation/question-actions-2026-09-23.md).

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

## Bounded Claude native-question implementation — 2026-09-22

The Go app now handles the pinned Claude 0.80.0 `AskUserQuestion` form dialect
through the existing UI and `request.answer`. This is a blocking native callback,
not a fallback prompt or true asynchronous question. See the
[checkpoint](../implementation/native-question-checkpoint.md) and
[evidence](../research/native-questions-2026-09-22.md). Live provider question
success and authoritative answer receipts remain unverified.

This slice presents 1–4 questions with 2–4 unique choices, single/multiple
selection, optional Other text and explicit optional omissions. Single choice
plus a separate note is not supported; choose the option or Other. Multi-choice
plus Other is additive. Previews, required/constraint-bearing alternate schemas,
URL/MCP/general forms and child questions are rejected rather than weakened.
The original question and option descriptions remain readable (descriptions as
structured option detail since the 2026-09-23 actions refinement); original
source bytes are retained privately. Labels are wire values in this pinned adapter, so
duplicate values and repeated original question text are rejected as ambiguous.

Submission durably records the response before callback handoff. The UI never
creates an Answered history card from callback return, a pipe write, generic
tool output or turn completion alone. The 2026-09-23
[delivery evidence](#server-authority-and-recovery) rule settles a blocking
answer only on a bridge receipt followed by normal completion of the same turn.
Withdrawal/cancellation preserves any accepted response without replay; a new
connection cannot revive the callback. Explicit Submit remains the only answer
action in this slice; omission is represented in the accepted form, and Stop
cancels the active turn. The 2026-09-23
[actions refinement](#question-actions-and-option-descriptions--2026-09-23) adds
explicit Decline and Cancel: the elicitation answers `{"action":"decline"}` or
`{"action":"cancel"}` with no content, and the built-in bridge denies the
`AskUserQuestion` tool for either (Claude's `can_use_tool` result has no
separate decline and cancel outcomes). No delivery receipt follows a denial.

## Built-in Codex native questions — 2026-09-22

The Go bridge negotiates `tui-go.codex-questions.v1` and enables the installed
Codex CLI's `default_mode_request_user_input` feature for a question-capable
client. App Server `item/tool/requestUserInput` requests route through the same
server-owned form, explicit Submit, revision guard and durable response path.
The supplied `isBlocking` selects blocking/Waiting or continued-work/Running;
ordinary Send remains separate from answering. Supported forms contain 1–4
questions, single choices or free text, supplied option descriptions, and
optional Other only when offered. Secret input and automatic-answer timeouts
remain explicitly unsupported. The native IDs and source payload are retained.

A bounded native request ledger correlates `serverRequest/resolved` to its
request. Resolution after our response keeps the connection alive; external
withdrawal prevents a late answer and conservatively retires the connection.
Acceptance, native write and provider completion remain distinct. A
continued-work answer stays unconfirmed; a blocking one follows the 2026-09-23
[delivery evidence](#server-authority-and-recovery) rule. Live Luna-low evidence covers answer/retry, Stop, late-answer
rejection and restart without replay in the
[bug-fix report](../research/ui-bugs-2026-09-22.md).

**Decline/cancel (2026-09-23).** Codex questions offer no Decline or Cancel.
The installed CLI's own schema (`codex app-server generate-json-schema`,
codex-cli 0.156.0; the bridge pins 0.155.1) defines
`ToolRequestUserInputResponse` as only `{"answers": {<question id>:
{"answers": [string]}}}`, with no action, status or decline field, while the
same schema's `McpServerElicitationRequestResponse` does define
`accept`/`decline`/`cancel`. An empty answers map is schema-valid but its
meaning is undocumented, and returning one was the question-semantics loss
recorded against an earlier adapter; a JSON-RPC error is not a documented
decline either. The bridge therefore still refuses a non-accept response, and
the server rejects a decline or cancel command for a Codex request. Stop
remains the way to abandon the turn. Option descriptions are carried as
structured option detail.
