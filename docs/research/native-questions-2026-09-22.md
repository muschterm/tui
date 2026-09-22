# Native-question validation — 2026-09-22

This report covers the [bounded Claude question slice](../implementation/native-question-checkpoint.md).
Earlier provider observations remain in the versioned matrices and earlier
live reports; they are not fresh question-delivery evidence.

## Source and adapter-converter evidence

Reused Claude ACP **0.80.0**, Anthropic Agent SDK **0.3.278**, official Claude
Code **2.1.278**, Codex ACP **1.12.0** and the earlier Codex **0.154.0 / 0.155.1**
comparison. The [Claude dependency follow-up](claude-integration-matrix-2026-09-22.md#follow-up-fewer-dependencies-with-a-direct-go-bridge)
rechecked CLI help, pinned control types and primary documentation. No install,
login, provider prompt, account/config read or billing change was performed.

Executed the installed adapter's pure `askUserQuestionsToCreateRequest` and
`applyAskElicitationResponse` functions with synthetic single-question,
multi-question/Other and optional-omission inputs. The resulting redacted
[fixture](../../apps/go/internal/agent/testdata/claude-questions-0.80.0.json)
records the exact adapter version and SHA-256 of `dist/elicitation.js`, generated
forms/responses and converted SDK inputs. The Go fixture test checks the
normalizer's responses against these values. Node is needed only to regenerate
this research fixture, not to run Go tests. This executed converter mapping;
it did not launch Claude or prove model/tool behavior.

## Fake-peer and application evidence

`GOCACHE=/tmp/tui-go-build STATICCHECK_CACHE=/tmp/tui-native-question-staticcheck make check`
passed on Darwin arm64 / Go **1.27.1**, including gofmt, vet, pinned staticcheck,
the full race suite and build. The initial sandboxed attempt could not bind
loopback listeners; the rerun with local networking passed. Test storage and
checkouts were temporary. This includes the existing approval regressions,
HTTP connection drain, stalled bodies, late-view-write gate and accurate CLI
stop diagnostics. No shutdown implementation was replaced.

Focused checks establish:

- Pinned Claude capability/dialect gating; Codex, wrong versions and unsupported
  forms do not gain native-question support. General forms, URL mode, previews,
  unknown constraints, duplicate keys/labels/question text and bounds fail closed.
- Single/multiple choice, per-question Other, optional omissions, original source
  retention and exact wire values. Source forms remain private in public snapshot
  projection; normalized question cards survive it.
- Explicit `request.answer`, durable acceptance without confirmation, competing
  clients, original command retry receipts and one callback response. No new
  prompt or queue dispatch substitutes for a native answer.
- Stop, disconnect and actual peer `$/cancel_request` invalidate the callback;
  stale generation/session and missing callbacks reject late submissions.
- Actual response-write failure preserves an uncertain accepted answer, and a
  restarted engine returns the original receipt without replay. Generic tool
  updates and successful turn completion do not mark the answer confirmed.
- Invalid/unsafe answers are rejected before acceptance and can be corrected.
  Repeated tool requests and malformed forms do not create new cards.
- Failed initial persistence changes neither authoritative snapshot, subscribers
  nor callback count. Per-pending-question capacity reservations survive competing
  queue admission and fit four maximum 4096-byte legacy answers without retaining
  redundant legacy strings. These are command admission guarantees; existing
  streamed activity limits are not a new global provider-storage guarantee.
- TUI tests feed actual normalized forms into the existing UI, resize through
  160×50, 48×20 and 100×16, preserve the composer, keep selection local and emit
  structured answers only on Submit. Shared delivery descriptions remain truthful.

Independent review found the initial failed-save publication and shared-headroom
bugs before completion; both were fixed and covered by focused regressions.
Approval exact option IDs/scope handling, deduplication and uncertain recovery
remain unchanged.

## Terminal evidence

The seven existing OS-PTY harnesses run against the rebuilt binary with
`TUI_GO_AGENT_CLAUDE_COMMAND` and `TUI_GO_AGENT_CODEX_COMMAND` set to missing
scratch executables. Each harness uses isolated application homes/checkouts;
no real provider is invoked. Raw streams remain in the private temporary
artifact directory. These are terminal regressions of shared/fixture behavior,
not a live native-question round trip, GUI-terminal, SSH or tmux verification.

`make pty` exited **0**. Smoke passed 38 checks, navigation 28, small-screen 42,
sidebar/settings 27, fixture steering 11, path completion 23, and colors 11
synthetic cases. [Sanitized reports](native-questions-pty-2026-09-22.json) retain
check names/results; private raw artifacts remain under
`/tmp/tui-native-questions-pty.41Cy6O`. The shared question PTYs cover radio,
checkbox/free-text drafts, explicit Submit, keyboard/mouse navigation, resize
and ordinary-prompt preservation; they use Demo requests. The normalized native
route itself is covered by converter, fake-peer and TUI unit tests above.

## Remaining evidence gaps

No fresh live Claude/Codex prompt or native question was sent. Provider receipt
confirmation, native question behavior under actual account/runtime settings,
live recovery/child questions, descendant cleanup and full approval restriction
parity remain unverified. The pinned form supplies no upstream turn/child ID;
tested correlation covers the original callback, session, connection generation
and application turn at admission, without inventing upstream provenance. The application does not render Answered history
without authoritative resolution evidence.

Codex blocking/turn/timeout metadata and steering defects are not corrected by
this work; affected capabilities stay disabled. Claude strict same-turn steering,
true async delivery, general elicitation and structured-text fallback remain
unavailable. Fallback still awaits queue/writer scheduling. No dependency,
authentication, billing, personal application storage or remote Git changes were
made; the prior uncommitted implementation is preserved.
