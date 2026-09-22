# Built-in Go ACP bridge evidence — 2026-09-22

This follows the [Go bridge checkpoint](../implementation/go-adapter-checkpoint.md)
and [ADR 0016](../adr/0016-built-in-go-acp-bridges.md). Earlier third-party adapter
reports remain historical evidence; these runs use the Go bridges shipped in
the application, with no npm ACP adapter or Agent SDK sidecar.

## Runtime and protocol sources

- Installed Claude Code **2.1.280**. Direct non-inference control initialization
  returned the runtime model catalogue and acknowledged `default` permissions.
  The bridge uses `-p`, stream-json input/output, partial messages, an explicit
  session UUID and the native stdio permission host. It retains ordinary CLI
  configuration loading; no `--bare` or permission-bypass flag is used.
- Installed Codex CLI **0.155.1**. Its experimental App Server JSON schemas were
  generated into temporary storage without inference. Native `thread/start`,
  `thread/resume`, `thread/settings/update`, `turn/start` and `turn/interrupt`
  are kept behind ACP. Model/effort discovery is supplied by `model/list`.
- Primary documentation: [Codex App Server](https://learn.chatgpt.com/docs/app-server)
  and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).
  Versioned local evidence includes SDK **0.3.278** `sdk.d.ts` control envelopes,
  permissions and result types. It is a read-only schema reference, not a runtime
  dependency. See the existing [Claude](claude-integration-matrix-2026-09-22.md)
  and [Codex](codex-integration-matrix-2026-09-22.md) comparisons for exact sources.
- UCF was inspected read-only for transport orientation. Its native-request
  policy and turn-ending question convention were not adopted. No UCF source
  was copied or changed.

## Live checks

The user restricted real-model tests to **Claude Haiku** and **Codex Luna low**.
Both IDs were checked against the installed runtime catalogue before Send.
No other model was used for live inference. These checks inherit the user's
existing official CLI login/configuration; they change neither authentication
nor billing. The two scenarios each submit an initial prompt and one subsequent
cancellation target, using isolated application homes and checkouts.

An exec wrapper passes the explicitly resolved installed CLI through unchanged,
recording only process IDs and protocol-flag presence. Application registry
commands select `builtin:claude` / `builtin:codex`; the other provider is disabled.
Only the application and installed CLI execute on the provider path. No external
adapter/SDK installation, bundled runtime or download is performed.

| Scenario | Result |
| --- | --- |
| Claude `haiku`, bridge `tui-go-claude 0.1.0` | **PASS**: one native AskUserQuestion, app waiting state, exact Blue answer through `request.answer`, duplicate command returns the same receipt, provider replies Blue. |
| Claude Stop and recovery | **PASS**: Stop while a second question waits; late answer rejected; Resume sends nothing; restart preserves no dispatched queue copy and retains the accepted answer as uncertain; retry returns the old receipt without replay. |
| Codex `gpt-6-luna` / `low`, bridge `tui-go-codex 0.1.0` | **PASS**: real pong reply, acknowledged model/effort, streamed response, cancellation target, Resume without duplicate prompt and restart without dispatched queue copies. |
| Owned process cleanup | **PASS**: three tracked official-runtime launches for each scenario were gone after confirmed isolated-server shutdown. This does not establish arbitrary detached-descendant cleanup. |

Retained selected-field reports: [Claude](go-bridge-evidence-2026-09-22/claude.json)
and [Codex](go-bridge-evidence-2026-09-22/codex.json). Private process records and
scratch logs remain outside the repository. The native Haiku answer remains
`acp-unconfirmed`, becoming `acp-uncertain` after restart; the provider's reply
corroborates behavior but is not an authoritative answer receipt.

Reproduction, from `apps/go` after building:

```sh
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent claude --model haiku --runtime /absolute/path/to/claude \
  --artifacts /tmp/tui-go-bridge-claude
TUI_GO_LIVE_ACP=1 python3 scripts/live_agent_recovery.py \
  --agent codex --model gpt-6-luna --effort low \
  --runtime /absolute/path/to/codex --artifacts /tmp/tui-go-bridge-codex
```

## Regression validation

`make check` **PASS**: formatting, `go vet`, pinned Staticcheck, `go test -race
./...`, and the application build. Real provider commands were overridden with
nonexistent fixture paths. The final run allowed local loopback sockets for the
server tests; the initial sandbox-only server run could not bind those sockets.
Python syntax checks for both edited live harnesses, local documentation links
and `git diff --check` also passed.

`make pty` **PASS**: smoke (38 checks), navigation (28), small-screen (42),
sidebar/settings (27), steering (11), path completion (23), and colors (11
synthetic-response cases). Both real providers were disabled explicitly for
this suite. The [retained report](go-bridge-evidence-2026-09-22/fixture-pty.json)
describes fixture behavior and terminal-response simulations, not live provider
or universal terminal compatibility.

The live runs precede the final fake-peer-driven race tightening; those changes
do not add another inference call. Tests cover callback withdrawal, stale grants,
cancel-before-dispatch, native terminal-result identity, result-before-exit,
bounded malformed framing and runtime descendants retaining stdio. Process
exit is distinct from ACP stream teardown and confirmed reaping. No user state,
checkout files, original fixtures or historical recordings are removed.

The Codex regressions include completion arriving before the interrupt RPC
response, withdrawal before callback registration, cancelled/stale start guards,
and Stop followed by a lost start response. A dispatch attempt with a missing
response remains uncertain and closes the runtime; Stop alone cannot turn it
into confirmed cancellation. Internal dispatch markers never reach App Server.

## Limits

- Claude's bridge exposes discovered model selection and acknowledged default
  permissions. Effort, speed and context controls are unavailable in this slice.
  Native questions are bounded, blocking main-agent AskUserQuestion forms, under
  `tui-go.claude-questions.v1`. Unsupported interactive approvals fail closed;
  the general approval surface offers Deny/Allow once, with no persistent grants.
- Codex questions, MCP elicitation, permission-profile elevation and steering
  remain unavailable unless specifically implemented and tested. `turnId`,
  `itemId`, `isBlocking` and timeout metadata must not be discarded to force a
  Codex question into the Claude form. There is no turn-ending fallback.
- Codex native request withdrawal conservatively retires the connection: the
  current SDK callback does not expose the inbound RPC identity for exact
  correlation. This can interrupt otherwise valid work, including continuation
  after an ordinary answered approval when its resolved notification arrives;
  it never confirms answer delivery or silently retries. Native EOF uses a
  bounded event-drain grace period, so a delayed completion can conservatively
  remain a failure rather than establish success.
- No authoritative answer receipt, true async question, full child history,
  subscription-window/cost parity or checkout writer scheduler is established.
  Live native approval/file-write, images, live session-load restoration,
  multi-client provider recovery and live TUI coverage remain gaps. Fixture PTY
  checks are separate from provider integration evidence.
- Only the installed versions above were live checked. Schema validation and
  unsupported-state errors do not establish compatibility with arbitrary CLI
  upgrades, remote sessions, SSH/tmux or Windows process-tree behavior.
