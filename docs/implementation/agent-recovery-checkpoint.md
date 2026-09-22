# Agent dispatch and recovery continuation — 2026-09-22

Continuation of the [agent handoff](agent-integration-handoff.md) and
[native-question slice](native-question-checkpoint.md). Earlier staged and
uncommitted implementation remains intact; this work adds corrections on top.

## Selected slice and assumptions

The user confirmed **personal local prototype first**, using the existing
official runtime login. The rechecked [Claude comparison](../research/claude-integration-matrix-2026-09-22.md)
supports that scope; distributed SDK subscription product eligibility remains
an explicit separate question. No API credential substitution, token handling,
login change or installation was introduced.

Retain Claude ACP **0.80.0** / Agent SDK **0.3.278** and Codex ACP **1.12.0**
for this slice. Native Claude forms already have a tested translation; owning
a direct CLI host would duplicate permissions, settings, correlation and
cleanup. Codex remains behind official App Server; its source/schema gaps
prevent enabling questions and strict steering. Runtime selection in the live
checks is explicit: installed Claude **2.1.280** and Codex **0.155.1**. The
earlier source/resolver inspection saw Claude **2.1.278**; the installed CLI
changed before live validation. This is evidence for these combinations, not
compatibility with arbitrary updates. No runtime dependency was added.

ACP remains between server and agent. Frontends still submit the same
`request.answer`; approvals keep exact provider choice IDs and their distinct
kind. UCF was consulted read-only, with no runtime dependency or copied code.
Its turn-ending question convention establishes neither native nor async input.

## Corrections

- **Commit before prompt I/O.** Session setup and configuration still precede
  dispatch. `startTurn` saves a candidate snapshot with the
  captured prompt removed from the queue and retained in user activity before
  authorizing `session/prompt`. Failed storage leaves the snapshot, queue,
  subscribers and live-turn channels untouched. Fatal storage failure remains
  latched through server shutdown so dispatch cannot restart in the gap.
- **No replay of failed in-flight prompts.** Once dispatch crosses that durable
  boundary, an RPC failure may follow actual tool effects or an accepted answer.
  Retain the full prompt/settings/attachments in history, show the failure and
  require explicit Resume. Send may queue new input but preserves the warning
  and gate. Resume dispatches only never-sent queued work; it does not replay
  the failed prompt or accepted answer. Settings/setup failures before dispatch
  continue retaining their unsent queue head.
- **Preserve the recovery capture through long streams.** The current turn's
  user activity is retained inside the existing 128-entry bound. A later turn
  can still age older history out under the explicit truncation policy; this
  does not create an unlimited archive.
- **Conservative migration.** On restart, remove an old requeued copy only when
  a retained failed/running/interrupted user activity proves that exact prompt
  capture crossed dispatch. Compare the entire capture, including revision,
  settings and attachments. Edited queued input keeps its entire capture but
  receives a fresh prompt identity so later dispatch cannot overwrite original
  history or reuse that turn identity; unmatched input stays unchanged. Existing
  command receipts retain their historical targets.
  Failed ACP threads require Resume after restart. Migration is idempotent;
  absence of old history cannot establish whether a legacy queue copy ran.
- **Transactional approval admission.** Like native questions, approvals save
  a candidate before registering their callback or publishing a card. Refuse
  admission above 128 retained requests or projected snapshot capacity. Pending
  approvals reserve space for the exact accepted option ID/label and command
  metadata, including JSON escaping; competing queue work cannot consume it.

No new frontend API or database schema is needed. The existing JSON snapshot
and command receipts retain captures and answer identities. Callback return,
pipe write, completed tools and a final provider reply remain insufficient to
mark a question Answered. `acp-unconfirmed` and restart `acp-uncertain` remain
deliberate, even when live behavior corroborates the selected answer.

## Validation and remaining work

See [live and regression evidence](../research/agent-recovery-live-2026-09-22.md).
Configured Go checks and focused persistence/recovery regressions cover the
new transitions. Live checks use isolated application homes/checkouts and an
exec wrapper that records only runtime protocol flag presence and process
identity, never credentials or raw arguments.

Authoritative request receipts/withdrawal, live multi-question/Other shapes,
Codex question metadata/timeout repairs, true asynchronous questions, strict
same-turn steering, child histories, full approval restrictions and real
`session/load` recovery remain open. Structured-text fallback is disabled until
queue/writer admission and continuation correlation are implemented. The
checkout writer queue is still missing; these checks run one thread per
isolated checkout. General provider process-tree cleanup is not established by
tracking the adapter and runtime processes in a no-tool check.
