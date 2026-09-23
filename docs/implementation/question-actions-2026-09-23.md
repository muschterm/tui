# Question actions and option descriptions — 2026-09-23

Implements the [actions refinement](../design/questions.md#question-actions-and-option-descriptions--2026-09-23)
in the Go reference app (`apps/go`), plus small follow-ups from the
[delivery work](question-delivery-2026-09-23.md). Deterministic captures are in
[go-question-captures-2026-09-23](../research/go-question-captures-2026-09-23/);
they are renderer output, not terminal screenshots.

## What changed

Contract (`internal/protocol`):

- `Question.OptionDescriptions` (parallel to `Options`), `Request.Actions`
  (offered `decline`/`cancel`), `Request.Action` (the chosen one) and
  `Command.RequestAction`. All are `omitempty`, so existing JSON and retry
  fingerprints are unchanged.
- `ValidateQuestion` rejects a description slice that is not parallel to
  `Options`; `ValidateAnswer` calls it. `ValidateRequestActions` and
  `ValidateRequestAction` check offered values and refuse any structured or
  legacy answers with an action.

Agent parsers (`internal/agent`):

- Claude/ACP elicitation (`elicitation.go`): option descriptions become
  `OptionDescriptions` instead of `label: description` paragraphs in `Text`;
  requests offer `decline` and `cancel`. `QuestionForm.ActionResponse` emits
  `{"action":"decline"}` / `{"action":"cancel"}` with no content, only for an
  offered action; `Respond` selects it or the accepted answers.
- Codex (`codex_questions.go`): descriptions are structured the same way;
  `Actions` stays empty (see Codex evidence below).

Server (`internal/server`):

- `commands.go` `request.answer`: a `RequestAction` is valid only on a question
  request, must be offered and carries no answers; it then records
  `Request.Action` and follows the answer path unchanged (revision guard,
  submission identity, dedupe, `acp-accepted` → handoff, fixture
  `resolved`/`fixture-confirmed`, one chronology marker).
- `questions.go`: validation before acceptance and the callback response use
  `form.Respond(request.Action, …)`. The server's own withdrawal still answers
  `{"action":"cancel"}` without recording an `Action`.
- `agents.go`: the delivery receipt and `acp-turn-confirmed` settlement apply
  only when `Action` is empty. The bridges emit no receipt for a denial, so a
  decline or cancel stays unconfirmed.

Fixture: `question-blocking` offers decline and cancel and has one option
description; `question-async` offers none. Existing application homes keep
their saved fixture requests; only a fresh home gets these.

TUI (`internal/tui`):

- `question_card.go`: `questionActionsPlan` lays out the right group — Cancel,
  Decline, Submit when they fit; More… (menu) + Decline + Submit; then More… +
  Submit. Submit is never displaced. All use `questionButton` with Submit's
  disabled rule. Description rows are muted, wrapped, indented to the label
  column, keyless (no hover/focus/hit) and counted in the row budget. Up from
  Decline/Cancel/More… behaves like Up from Submit.
- `question_drafts.go` `submitRequestAction`: offered-action, Resume and
  connection checks, then `request.answer` with `RequestAction` and no answers;
  drafts are never touched. `actions.go`/`model.go`: `answer-action` joins the
  hidden-work refusal and request-bound (stale menu) checks; More… opens
  "Request actions"; acceptance reads "Decline accepted by server · upstream
  confirmation unavailable"; in-flight notice reads Declining…/Cancelling….
- `question_history.go`: Declined/Cancelled status (muted when resolved,
  otherwise the answer copy's delivery sub-status and colour), original
  questions with unselected labels, no answer rows; same in preview and copied
  text. Activity detail adds `Response: Declined · no answer sent`.
- Follow-ups: `deliveryConfirmed` includes `acp-turn-confirmed`;
  `requestDeliveryDescription` maps it to "Answer taken by provider · turn
  completed"; `queue_card.go` uses `containerStyle`, so the queue outline
  follows keyboard focus inside it, never hover
  ([components](../design/components.md) sentence added).

## Codex decline evidence

`codex app-server generate-json-schema --out <scratch>` with the installed
codex-cli 0.156.0 (the bridge pins 0.155.1):

- `ToolRequestUserInputResponse`: `{"answers": {<id>: {"answers": [string]}}}`,
  `answers` required, no action/status/decline field; both sides marked
  EXPERIMENTAL.
- `McpServerElicitationRequestResponse` in the same schema defines
  `action: accept | decline | cancel` with nullable content, so the protocol
  models decline where it supports it.

An empty answers map is schema-valid but has no documented decline meaning (and
was the semantics loss recorded in the
[Codex integration matrix](../research/codex-integration-matrix-2026-09-22.md));
a JSON-RPC error is not a documented decline. Codex questions therefore offer
no Decline/Cancel, the server rejects them with `unsupported_action`, and the
bridge keeps refusing non-accept responses.

## Tests

- protocol: description length validation (also through `ValidateAnswer`);
  decline/cancel normalization (offered, not offered, unknown, duplicate,
  approval, with structured/legacy answers, empty slices).
- agent: Claude descriptions structured and actions offered; `Respond`
  decline/cancel wire shape; unoffered/unknown actions refused; Codex
  descriptions structured, no actions, `ActionResponse` refused.
- acpbridge: Claude `{"action":"decline"}`/`{"action":"cancel"}` with or
  without empty content deny the tool; Codex decline and cancel are refused.
- server (`question_actions_test.go`): decline and cancel via the fake ACP peer
  reach it as `{"action":…}` with no content, stay `acp-unconfirmed`, dedupe
  retries, persist `Action` and one history marker; stale revision, unknown
  action, answers with an action, approval choice and a decline after an
  accepted answer are rejected; Codex offers none and rejects both; fixture
  decline resolves like an answer and `question-async` refuses one.
- tui (`question_actions_test.go`): Decline only when offered and placed left
  of Submit, Cancel left of Decline; overflow plan at four widths and the More…
  menu; decline command shape, notice, drafts and prompt kept after a failed
  decline, acceptance status; unoffered/Resume/disconnected refusal with drafts
  kept and disabled rendering unchanged by hover; Tab/Shift+Tab reach and Enter
  activation; description rows muted, keyless, counted (+1 row) with the option
  hit extent unchanged, menu labels only, malformed slice ignored; history
  Declined/Cancelled text, colour, unselected options and copy; the
  `acp-turn-confirmed` mapping; queue outline follows focus, not hover.

## Validation

Run in `apps/go` on macOS (Darwin 27.0.0), Go 1.27.1:

- `make check` (fmt-check, vet, staticcheck, race tests, build): pass.
- `PTY_ARTIFACTS=<scratch> make pty`: all harnesses pass (smoke, navigation,
  small screen, sidebar settings, steering, path completion, colors).
- `TUI_GO_CAPTURE_DIR=<scratch> go test ./internal/tui/ -run 'TestQuestionReviewCaptures|TestConversationRegressionPlacesLegacyQuestion'`
  then `scripts/render-capture.py --font ~/Library/Fonts/JetBrainsMonoNerdFont-Regular.ttf`;
  PNG and `.ansi.gz` copied. New scenarios, reviewed:
  `160x50-…-decline-description` (Cancel, focused Decline, Submit; muted
  description under Compact), `48x22-…-narrow-decline` (all three still fit;
  wrapped description) and `60x32-…-history-declined`.

The PTY runs exercise a local terminal emulator only; no SSH, tmux or other
terminal was checked.

## Remaining gaps

- Live provider behaviour of decline/cancel is not verified: neither the
  built-in Claude bridge (which denies the tool for both) nor the external
  claude-agent-acp 0.80.0 adapter was run against a real CLI.
- Claude's `can_use_tool` result has no separate decline and cancel outcomes;
  both become the same denial.
- No receipt exists for a denied question, so a decline never reaches a
  confirmed state on ACP; the finishTurn guard for a receipt paired with a
  declined request is covered by review, not a pinned-bridge test.
- The More… overflow is unit-tested but not captured; the 48-column capture
  still fits all three buttons.
- Codex has no Decline/Cancel until its app-server contract documents one.
