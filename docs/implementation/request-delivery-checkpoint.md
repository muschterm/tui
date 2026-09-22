# Request delivery correctness — 2026-09-22

This continues the agent-integration handoff with a bounded approval lifecycle
slice. The existing working tree is preserved. The versioned comparisons cover the
pinned adapters; no new provider question route or fallback is enabled here.

## Implementation contract

- `request.answer` remains the sole answer command. It accepts optional
  `ApprovalChoiceID`, negotiated through `approval-choice-ids`; legacy labels
  are accepted only when they identify exactly one offered option.
- The durable request retains its originating application turn, delivery route,
  submission command ID, submitted revision and accepted option ID. Its original
  card stays intact. Live routing additionally requires the original connection
  generation, upstream session and pending callback; these stay server-private.
- Acceptance commits `submitted` / `acp-accepted` before any handoff. Preparing
  the ACP callback response closes the local request with `acp-unconfirmed`.
  The pinned SDK supplies no response-write or upstream receipt callback, so
  neither `sent` nor `confirmed` is claimed. Generic tool updates and turn
  completion are not acknowledgment evidence.
- Missing callbacks and stale connections reject new submissions. Stop, turn
  termination and disconnect close unanswered calls without granting permission.
  Accepted answers remain retained; a potentially handed-off response becomes
  uncertain rather than replayable after restart. Old `acp-delivered` records
  also become uncertain; their former label was never receipt evidence.
- Command deduplication prevents repeated handoff. General ACP questions fail
  explicitly until a negotiated question route implements this lifecycle.
  Fixture question behavior remains compatible.

## Assumptions and open work

ACP approval callbacks expose no raw RPC ID to the handler; the SDK owns that
correlation. A unique callback plus connection generation and session/turn checks
is the private routing identity for this slice. No child identity is invented.
The larger normalized question schema, exact original provider payload retention,
native elicitation, fallback parser/scheduling, checkout writer admission,
upstream confirmation extensions and Answered Q&A history remain separate work.

## Adapter comparison and next slice

The [Claude matrix](../research/claude-integration-matrix-2026-09-22.md) verifies
0.80.0 / SDK 0.3.278 executable selection and the already-implemented native form
bridge. The [Codex matrix](../research/codex-integration-matrix-2026-09-22.md)
compares 1.12.0 with both installed runtime schemas (0.154.0 and 0.155.1), finding
lost blocking/turn metadata, omitted-timeout handling and a steering-to-new-turn
fallback. Reuse with focused fixes is the current recommendation; this slice
retains the baseline adapters and does not adopt a custom bridge.

Claude subscription/product eligibility remains an explicit open question for
distribution of this local host. Runtime login and billing configuration were
not changed, and no account state or credentials were read. The next native
question slice must negotiate bounded forms, reject upstream ambiguities such
as duplicate label-based enum values, preserve execution/correlation metadata,
and define authoritative withdrawal/receipt evidence. Fallback still awaits
queue/writer scheduling; it cannot serve as a substitute for a failed native
response or for true asynchronous delivery.

## Validation

Executed on Darwin 27.0.0 arm64 with Go 1.27.1:

- Lifecycle regressions first reproduced premature confirmation, unsupported
  ACP question acceptance, ambiguous-label selection and missing uncertain
  restart handling; these tests now pass.
- `GOCACHE=/tmp/tui-go-build make check` passed: gofmt, vet, pinned staticcheck,
  the complete race suite and build. An earlier sandboxed package run could
  not bind loopback test ports; the full suite passed with local networking
  available, using isolated test storage.
- Focused race tests cover exact duplicate-label choices, two competing clients,
  lost command receipts, cancelled/disconnected requests, missing live callbacks,
  stale connection/session requests and updates, unsupported ACP questions and
  uncertain recovery. A subsequent failure-injection test drops the actual ACP
  approval response write; the accepted snapshot stays uncertain and a command
  retry against restarted state returns its original receipt without replay.
- TUI tests cover inline and menu ID routing, legacy encoding, global capability
  compatibility with fixture approvals, missing/duplicate IDs, stale menus,
  preserved composer drafts and truthful delivery descriptions.
- All seven `make pty` harnesses passed: smoke (38 checks), navigation (28),
  small screen (42), sidebar/settings (27), fixture steering (11), path
  completion (23), and colors (11 synthetic cases). Both provider command
  overrides pointed to missing temporary executables. The suite sent no real
  provider prompts. [Retained reports](../research/request-delivery-pty-2026-09-22.json)
  omit raw PTY streams; scratch artifacts are under
  `/tmp/tui-request-delivery-pty-20260922`.
- Independent approval-lifecycle review found the stale-notification gap; the
  generation/session guard and regression check address it. Reviewed mutation
  order and deduplication for wrong-scope grants, data loss and repeated delivery.
  Documentation link targets, formatting and whitespace checks passed.

The receipt boundary is grounded in pinned acp-go-sdk v0.13.5
`connection.go:566–629`: `handleInbound` calls the handler, marshals its result,
then discards `sendMessage`'s error. The application cannot infer a successful
write or provider acknowledgment from returning a permission response.

No fresh live Claude/Codex prompts, native GUI review, SSH/tmux checks or provider
recovery tests were run. Earlier live evidence remains historical. This continuation added no dependencies or authentication changes, touched no
personal application storage, and performed no remote Git operations. The
pre-existing uncommitted changes remain in place.


## Subsequent shutdown correction

The user subsequently reported a stop deadline. The
[shutdown regression report](../research/go-shutdown-2026-09-22.md) records its
isolated HTTP reproduction, connection-drain fix, late-view-write protection and
validation. This is separate from provider answer-delivery evidence.

## Subsequent native-question continuation

The [bounded Claude question slice](native-question-checkpoint.md) now builds on
this lifecycle. It preserves the approval safeguards, adds pinned elicitation
handling, private source snapshots and RPC withdrawal, and still makes no
upstream confirmation claim. The original approval-only statements above
describe this earlier checkpoint. Codex affected capabilities and fallback
delivery remain disabled.
