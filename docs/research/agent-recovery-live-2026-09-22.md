# Personal agent prototype: live questions and recovery — 2026-09-22

The [selected slice](../implementation/agent-recovery-checkpoint.md) preserves
ACP at the server-agent boundary and the single application `request.answer`
contract. The user explicitly confirmed **personal local prototype first**.
Existing staged/uncommitted work, fixture behavior and UCF were preserved.

## Versions, authentication and selection

The independently rechecked [Claude](claude-integration-matrix-2026-09-22.md)
and [Codex](codex-integration-matrix-2026-09-22.md) matrices compare existing
adapters with proposed direct-runtime/thin-SDK bridges, including source
fingerprints, documented capabilities, fake-peer evidence and live limits.
Retain the existing adapters for this personal slice: no missing Claude form
translation justifies a replacement, and Codex's question/steering defects need
targeted repairs before exposure. Reuse retains Node and upstream translation
dependencies; a custom bridge would additionally own their permission, settings,
event, correlation and process lifecycle behavior.

Installed runtime versions changed between source review and execution:

| Layer | Inspected / actually used |
| --- | --- |
| Claude adapter | `@agentclientprotocol/claude-agent-acp` 0.80.0 |
| Claude SDK | `@anthropic-ai/claude-agent-sdk` 0.3.278 |
| Claude runtime | Earlier resolver/version review: 2.1.278. Fresh live checks: **2.1.280** through explicit `CLAUDE_CODE_EXECUTABLE` wrapper. This task did not install/update it. |
| Codex adapter | `@agentclientprotocol/codex-acp` 1.12.0 |
| Codex runtime | **0.155.1**, explicit `CODEX_PATH` wrapper, executed with `app-server`; bundled 0.154.0 remains source/schema comparison only. |

The wrapper immediately `execv`s the selected absolute installed binary. It
retains only PID/parent and allowlisted protocol **flag names**, not arguments,
environment or raw wire messages. Reports show actual JSONL Claude and App
Server launches. Resolver checks alone were not treated as proof of execution.
Configuration sources and effective-setting limitations remain those in the
matrices; setting acknowledgment is not independent proof of sandbox enforcement.

Official `claude auth status --json` was filtered in memory to `loggedIn: true`,
`authMethod: claude.ai`, `apiProvider: firstParty`, `subscriptionType: max`.
No account email, credentials or tokens were retained. All checked API-key,
OAuth-token and alternate Claude-cloud environment overrides were absent.
Official `codex login status` reported ChatGPT login. A prior sandboxed Codex
App Server account-state attempt exited before initialize and never sent
`account/read`; that attempt did not prove auth failure. The later live App
Server prompt succeeded with the inherited official login, without changing
configuration or billing.

Current official guidance supports ordinary personal SDK/CLI usage while the
announced billing change remains paused. It does not settle authorization to
ship a distributed subscription-backed SDK product. That separate scope remains
open; no API credential substitute or custom login was introduced.
[Claude plan guidance](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan),
[Claude legal/product guidance](https://code.claude.com/docs/en/legal-and-compliance),
[Codex authentication](https://developers.openai.com/codex/auth).

UCF source was re-inspected read-only at the commit/provenance recorded in the
Claude matrix. Its prompted turn-ending question convention still supplies no
native request receipt or true asynchronous-question evidence. No UCF code was
copied or runtime dependency added.

## Live HTTP evidence

[Selected-field reports](agent-recovery-live-2026-09-22.json) retain exact
results. Each run created a separate application home/checkout and disabled the
other adapter's startup probe. The harness inherited official configuration and
login. It used only synthetic text/questions, without authorizing file writes.
This is live server/adapter/provider evidence, not a live TUI capture.

| Scenario | Result |
| --- | --- |
| Claude native single-choice form | **Pass.** Actual AskUserQuestion became a blocking app question with Blue/Green options and optional Other. |
| Explicit structured answer | **Pass.** `request.answer` selected Blue; repeated command returned the original receipt; provider completed and replied exactly Blue. |
| Delivery truthfulness | **Pass.** Generic provider completion left `acp-unconfirmed`. Restart changed it to `acp-uncertain`; retry returned the saved command receipt without replay. The matching provider reply corroborates delivery but is not an authoritative protocol acknowledgment. |
| Claude Stop while waiting | **Pass.** A second native question stayed waiting until explicit Stop; upstream reported cancelled. Late answer received HTTP 409, with no accepted answer on the cancelled request. |
| Claude Stop while running | **Pass in the earlier successful run.** A separate long-reply prompt was cancelled, followed by Resume without another prompt. |
| Codex prompt / settings | **Pass.** Real pong; selected and adapter-acknowledged effective values recorded in the report. |
| Codex Stop / Resume / restart | **Pass.** Cancelled long-reply turn, empty-queue Resume with unchanged activity, restart with no dispatched queue copies. |
| Owned process exit | **Pass for tracked adapter/runtime PIDs in successful runs.** Claude: 10 across probe/thread/restart; Codex: 6. Checked within a six-second bound after server-stop acknowledgment. Arbitrary tool descendants were not exercised. |

Claude used discovered `fable[1m]`, high effort, default permissions; Codex used
discovered `gpt-6-astra`, high effort, agent mode, fast off. These are observed
options, not newly hardcoded provider menus. No native question, approval or
same-turn steering claim is made for Codex by its successful text reply.

Two preliminary harness runs failed because their wait predicate mistook the
initial idle/queued state for a completed prompt. They are retained as harness
failures and establish no provider outcome. Their immediate process-exit checks
were also unconfirmed; the corrected harness retains private process identities
and waits boundedly for exit. Do not reinterpret those failures as passing
cleanup. The successful runs above are independent fresh runs. A final harness
guard also waits through initial `unprobed` status; Python compilation passed
after this predicate-only change, without another provider prompt.

Reproduction from `apps/go`, with user-owned executables already installed:

```sh
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent claude --adapter <absolute-claude-acp-executable> \
  --runtime <absolute-official-claude-executable> --artifacts <scratch-directory>
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent codex --adapter <absolute-codex-acp-executable> \
  --runtime <absolute-official-codex-executable> --artifacts <scratch-directory>
```

## Application and terminal regression evidence

`GOCACHE=/tmp/tui-go-build STATICCHECK_CACHE=/tmp/tui-integration-staticcheck make check`
passed on Darwin arm64 / Go 1.27.1: gofmt, vet, pinned staticcheck, the full race
suite and build. The initial restricted attempt could not bind loopback sockets;
checks were rerun with local networking. The new tests cover:

- Failed dispatch persistence: queue/snapshot/subscribers/channels unchanged,
  no authorization to send, and no dispatcher retry loop.
- Post-dispatch failure: retained capture, explicit Resume gate and Send warning;
  only new input reaches the provider after Resume.
- Long streamed history followed by disconnect: full prompt retained durably
  inside the activity bound, with no queued replay.
- Legacy exact-copy removal, preserved edited captures with fresh identities,
  untouched original history, and idempotent migration.
- Approval admission storage/count/snapshot refusal without publication or a
  live callback; accepted-choice reservation covers worst-case JSON escaping.
- Existing native question/approval identity, competing-client, lost-receipt,
  actual failed-response-write and uncertain-recovery regressions.

`make pty` passed all seven existing fixture harnesses with both real agent
commands set to nonexistent paths. [Sanitized reports](agent-recovery-pty-2026-09-22.json)
retain smoke, navigation, small-screen, sidebar/settings, steering, path
completion and color results. They cover the shared question UI, drafts,
explicit Submit, keyboard/mouse and narrow geometry using fixture requests.
These are OS-PTY regressions, not native GUI-terminal, SSH/tmux or live-provider
UI evidence. Raw terminal artifacts stay in `/tmp/tui-integration-final-pty`.

Python harness compilation and documentation link/whitespace checks passed.
Diff review specifically covered duplicate-write, replay, retained-capture and
failed-save publication risks. No commit, pull, push, personal server stop,
personal TUI application-data mutation or credential extraction was performed.
Official runtimes still own their normal local session/configuration storage.

## Remaining gaps

The live form tested one single-choice question; multi-question/multiselect,
Other, malformed forms and duplicate-label behavior retain converter/fake-peer
evidence only. Full native approval restriction parity, live deny/approval
races, child questions/history, multi-client provider recovery and live
`session/load` remain unverified. No authoritative answer receipt means no
Answered history renderer is enabled. General withdrawal/cancel receipts and
precise upstream child/turn provenance remain limited by the pinned dialect.

Codex question turn/blocking metadata and timeout translation still need a
versioned adapter repair; strict steering must never start a new turn. True
async input remains unavailable. Structured-text fallback remains disabled
until explicit continuation correlation and checkout writer scheduling exist.
The writer queue itself remains unimplemented. Earlier unmatched legacy queued
records lack proof of prior execution; migration preserves and gates them
instead of guessing. Current-turn capture retention is bounded, not a permanent
archive. General descendant cleanup and distributed Claude SDK eligibility are
not established by these personal, no-tool runtime checks.
