# Question delivery status — 2026-09-23

The user reported that previously answered agent questions still showed
**Submitted · delivery uncertain**.

## Evidence

A copy of `~/.tui-go/state.sqlite` (with its WAL) was inspected read-only in a
scratch directory. Snapshots are JSON in `state.data`. Every native question
record belonged to one Codex thread:

| Request | Mode | State / delivery | Rev | Turn | Turn outcome |
| --- | --- | --- | --- | --- | --- |
| `question-23f6…` | async | closed / `acp-uncertain` | 4 | `prompt-fa2c…` | completed, `end_turn` |
| `question-2c43…` | async | closed / `acp-uncertain` | 4 | `prompt-c873…` | completed, `end_turn` |
| `question-34bc…` | async | closed / `acp-uncertain` | 4 | `prompt-6909…` | completed, `end_turn` |

Each record has a submission ID and answers, and `SubmittedRevision` 1. The
agent replied after the answer in the same turn, and the thread is idle without
Resume. The server log has no question or delivery lines. The fixture records
were confirmed.

## Cause

- `finishQuestion` records `acp-unconfirmed` at handoff
  (`internal/server/questions.go`). On success `finishTurn` left it unchanged, and no bridge
  reported delivery, so nothing could confirm an answer.
- `recoverACPThread` (`internal/server/settings.go`) downgraded every
  `acp-unconfirmed`/`acp-delivered` record to `acp-uncertain` on each restart,
  including records whose turn had already completed. That produced revision 4
  (pending 1 → submitted 2 → unconfirmed 3 → restart 4) on all three records.
- Treating turn completion alone as proof would not be truthful. Both bridges
  can answer the provider with an error or drop a stale response, and the
  provider can then still end the turn normally.

## Change

- Built-in bridges emit a pinned `tui_question_delivery` session update
  (`tui-go.question-delivery.v1`). Claude sends it on Claude's successful tool
  result for an `AskUserQuestion` we answered. Codex sends it once the answer
  response is written to an unresolved `item/tool/requestUserInput`. Codex
  emits it while holding the request-ledger lock, so the native reader cannot
  forward turn completion before it.
- The server trusts the update only from the built-in bridge identities and the
  current connection generation, session and turn. It holds the update in
  memory. `finishTurn` resolves a **blocking** question
  (`resolved`/`acp-turn-confirmed`) only when the update arrived and the same
  turn ended without error or cancellation. A turn error still yields
  `acp-uncertain`. Continued-work questions stay `acp-unconfirmed`.
- Restart marks accepted-but-unsent answers uncertain. It marks handed-off
  answers uncertain only when their turn was running or waiting. Completed,
  failed and cancelled turns keep their recorded status.
- On load, a closed `acp-uncertain` answer is repaired to `acp-unconfirmed` when
  its retained user prompt activity is `completed`. A failed turn marks the
  prompt failed and a cancelled one interrupted, so only the old restart
  downgrade produces that pair. The repair is idempotent and never claims
  delivery.

A dry run of `recoverThreads` on the copied snapshot turned the three Codex
records into `acp-unconfirmed` (revision 5). The fixture records were unchanged.
They are continued-work questions, so they remain **Submitted · provider
confirmation unavailable** instead of Answered.

## Validation

- New server tests cover the following cases:
  - Receipt then `end_turn` gives Answered.
  - Receipt then a turn error gives uncertain.
  - Without a receipt, or in continued-work mode, the answer stays unconfirmed.
  - A receipt from an unpinned adapter is ignored.
  - A restart after a completed or cancelled turn changes nothing.
  - A restart during a running or waiting turn gives uncertain.
  - The legacy repair works and does not touch failed, interrupted or trimmed turns.
- Bridge and agent tests cover receipts for Codex answers, errors, withdrawals
  and approvals, and Claude's tool results.
- `make check` passed: vet, staticcheck, race tests and build.

## Limits

No live Claude or Codex session was run. The receipt conditions follow the
pinned wire shapes and the fake runtimes, not observed provider traffic.
Codex's `isBlocking: false` semantics are undocumented in the 0.156.0 schema.
