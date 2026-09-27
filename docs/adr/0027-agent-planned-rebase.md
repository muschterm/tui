---
status: accepted (user decision 2026-09-26 for the behaviour; transport, lease and bounds are orchestrator defaults, reversible)
---

# Agent-planned interactive rebase

The user decided on 2026-09-26 that an interactive rebase can be planned by
an agent: "an agent job reads the commits and the user's instruction and
proposes a plan (and reworded messages); the user reviews and may edit it
in the same editor, and only then does the server run it"
([git-client.md, Pull and integration](../design/git-client.md#pull-and-integration)).
[ADR 0026](0026-interactive-rebase.md) added the plan read, the plan
validator (`protocol.ValidateRebasePlan`) and the start; this records the
server, protocol and client slice of the planner (handoff slice 6). The
TUI loads a proposal into the existing plan editor
(`openGitRebaseAgentPlan`) and starts it with an ordinary `git.rebase`.

## Decision

### A planning job is a read-only job thread

- **Job thread.** A planning job reuses the resolution-job machinery
  ([ADR 0023](0023-merge-rebase-operations.md) S4): a job-kind thread
  (`Thread.Job.Kind = rebase_plan`, hidden from the thread list) whose turns
  run through the ordinary ACP dispatch (approvals, questions, activity,
  interrupt, captured per-prompt settings, restart gating). Ordinary thread
  commands are refused (`job_thread`); `thread.interrupt` stays available.
  Only ACP agents can plan (`not_supported` for the fixture).
- **No writer lease (orchestrator default).** The job is read-only work, so
  it neither holds nor waits for the checkout writer lease: other agent
  turns and lease-taking Git writes proceed while it plans, and it starts at
  once even while other work runs. Rationale: planning takes a whole agent
  turn, and holding the lease would block unrelated work in the checkout
  for that long; unlike a resolution job, the plan's correctness does not
  depend on the checkout staying still (only the branch, which the
  fingerprint pins). The cost: the after-turn check below cannot tell the
  agent's changes from concurrent ones. The server records whether
  application work that may change the repository ran meanwhile (another
  thread's turn, any application Git write with or without the lease, a
  shared document's disk save, also between the pin and the job's turn),
  matched by path or by repository (the common Git directory, so work in
  another worktree counts; `Summary.Concurrent`). The taint reason says
  "the repository changed during the planning turn", what changed and
  whether such work was noted, and never attributes the change to the
  agent; terminals, fixture turns and outside programs are not tracked. The start and revise commands are journaled Git writes that take
  the repository's Git slot for the plan read but not the lease.
- **Pinned state.** At the start the server reads the plan (the ADR 0026
  plan read with the requested base and onto, `upstream` included) and
  pins it with its fingerprint; before every turn it pins the checkout
  and what a later rebase would run (verify round 2026-09-27): branch,
  HEAD and the raw HEAD file (its symbolic target); a SHA-256 of
  `ls-files --stage -z` (the whole index) and of `ls-files -v -z` (every
  entry's flags) with the stat token of each skip-worktree or
  assume-unchanged path, whose edits status hides; the complete
  `status --porcelain=v2 -z --untracked-files=all --ignore-submodules=none
  --no-renames` (submodule working trees included) with a stat token for
  every path whose working-tree side differs, so a second edit of an
  already changed file shows (read up to 64 MiB; the digest covers all of
  it, and up to 2000 entries are kept to name what changed); the effective
  configuration of every scope (`config --list --show-scope --show-origin`,
  includes resolved; global and system scope included, since an agent
  could set `core.hooksPath` there); the effective hooks directory
  (`core.hooksPath` or `hooks/`: names, modes, content hashes, a symlinked
  hook by its target's content, a dangling one by its text); `exclude`,
  `attributes`, `sparse-checkout` and `grafts` in `info/` of the repository
  and, when different, of the worktree (`git gc` writes other files
  there); and every ref with its target, the stash included, except
  remote-tracking and prefetch refs, which fetches and background
  maintenance move (`for-each-ref`). A part that cannot be
  read completely makes the pin fail: a start is refused (`unavailable`)
  and a turn's comparison taints. Both are
  content-addressed JSON files in the application home
  (`git-baselines/`, integrity-checked, swept once no job references
  them), not in the snapshot.
- **After the turn** the server compares the checkout with the pin. Any
  difference makes the proposal **tainted** with an explicit reason
  (`Changes`: branch, HEAD, index, index flags, working-tree paths,
  configuration, hooks, info/, refs), whether or not the
  answer is valid; the answer is kept for inspection but never offered for
  loading. A comparison that cannot be made taints too.
- **Refusals at start.** The plan's start refusals that make planning
  meaningless are errors (`unborn`, `detached`, `empty_range`,
  `too_many_commits`, `unsupported_message`, `not_supported`), and so are
  more than 300 commits (`GitRebasePlanJobCommitsMax`, bounded by the
  prompt). A dirty tree, an operation in progress or files in the way can
  be fixed before starting, so they only show in the proposal read
  (`Blocked`). A start may pin the plan read the user saw
  (`Fingerprint`; `stale_plan`).

### Permissions: the provider's read-only mode where it is verified

(Coordinator follow-up 2026-09-27: "the agent must not modify the checkout"
is a user requirement, so the built-in bridges gained a read-only mode.)

- **Read-only bridges.** `acpbridge.OpenWith(..., OpenOptions{ReadOnly:
  true})` (`agent.Options.ReadOnly`) opens a built-in bridge whose one
  session is locked to the provider's read-only mode. The server opens a
  planning job's agent this way when the agent is a verified built-in
  bridge: `Command` is `builtin:claude` or `builtin:codex` and the probed
  identity is exactly this binary's `tui-go-claude`/`tui-go-codex`. The
  session's Permissions option then offers only the read-only value, which
  the job's settings use (a selected permission is replaced, never
  widened); every other value is refused by the bridge. Ordinary threads
  never get these values: they are not in the ordinary catalogue
  (planning-job only, so
  [thread configuration](../design/thread-configuration.md) is unchanged).
  `Summary.ReadOnlyMode` records it.
- **Claude: `plan`.** The bridge launches `claude -p` with
  `--permission-mode plan`, `--settings
  {"fastMode":true,"useAutoModeDuringPlan":false}` and without
  `--allow-dangerously-skip-permissions`, and refuses to open unless
  Claude's initialize reports `current_permission_mode: "plan"`. Documented
  behaviour ([permission modes](https://code.claude.com/docs/en/permission-modes),
  [permissions](https://code.claude.com/docs/en/permissions),
  [settings](https://code.claude.com/docs/en/settings-reference#useautomodeduringplan),
  read 2026-09-27): in plan mode Claude "reads files, runs shell commands to
  explore, and writes a plan, but does not edit your source", edits "stay
  blocked until you approve the plan"; with `useAutoModeDuringPlan` false
  "commands outside the built-in read-only set prompt for approval"
  (`ls`, `cat`, `grep`, `find`, read-only forms of `git`, ...), and a
  `--settings` key overrides user, project and local settings but not
  managed ones. Every prompt, including leaving plan mode (ExitPlanMode),
  reaches the server as an approval, which it declines (`ApprovalGated`).
  Not prevented: commands the user's own `permissions.allow` rules or hooks
  approve, and managed settings; the after-turn check covers those.
- **Codex: `read-only`.** The bridge narrows the thread at open and on every
  turn to App Server v2 `sandboxPolicy {"type":"readOnly",
  "networkAccess":false}` with `approvalPolicy "never"` (reviewer `user`).
  Documented behaviour ([App Server](https://learn.chatgpt.com/docs/app-server),
  read 2026-09-27: "prevents write operations to the filesystem"; the
  protocol source `codex-rs/app-server-protocol/src/protocol/v2/permissions.rs`
  and `codex-rs/protocol/src/protocol.rs` at openai/codex
  `334b6e7321d55697a2415a42e1725c814d3c0df4`: `ReadOnly { network_access }`
  with network off by default, and `Never`: "Failures are immediately
  returned to the model, and never escalated to the user for approval").
  Codex's sandbox is its own (platform sandboxing); MCP tools the user
  configured run outside it. The installed Codex CLI's schema was not
  re-read for this (no CLI was launched); an App Server that rejects the
  policy fails the settings acknowledgement, so the turn does not start.
- **Approvals are declined** as before, as defence in depth: every
  permission request a planning turn raises is answered with the reject
  option the agent offered (`reject_once`, else `reject_always`; ACP's
  option kinds, not labels) or cancelled when there is none, recorded in
  the transcript and counted (`DeclinedApprovals`).
- **Unverified agents** (any other ACP agent) keep the selected settings:
  the declines and the after-turn check are all that apply, and
  `Enforcement` says so. The prompt tells every agent not to change the
  checkout; it is not relied on.
- **The after-turn check stays** for every agent: none of these modes is a
  filesystem boundary the application controls, and none covers files Git
  ignores or other repositories.

### What the agent is given

The first turn's prompt (at most 128 KiB, stored in the job thread like any
prompt) contains the task and the user's instruction (at most 4 KiB), the
answer format and rules, the branches `update_refs` would move, and the
commits oldest first: full hash, author, date, subject, published and merge
flags; then per commit the body (2 KiB each, marked when cut) and
`--numstat` (40 files each), and finally each commit's diff (6 KiB each,
`log -p -M --no-ext-diff --no-textconv`, one `git log` over the range, read
up to 8 MiB) while the prompt budget lasts, with every omission marked
(also commits a truncated log read did not reach), and the bound includes
those notes. Repository text (branch names, messages, authors, paths,
diffs) is fenced between `BEGIN-REPOSITORY-DATA-<nonce>` and
`END-REPOSITORY-DATA-<nonce>` lines with a random 96-bit nonce per prompt,
and the prompt says it is data, never instructions. A commit can still try
to instruct the agent; the read-only modes, the after-turn check and the
user's review are the defence.

### Output transport: structured text, not a tool

The agent answers with exactly one fenced block whose info string is
`tui-rebase-plan`, containing JSON
(`GitRebaseProposalWire`: `version` 1, `entries` in `GitRebaseEntry` form,
`update_refs`, `rationale` of at most 16 KiB), parsed server-side from the
turn's final agent message segment: an answered question during the turn
starts a new segment (`agent-<turn>:after:<answer>`), so a block written
before the question (an example, say) does not count. Questions stay
available during planning turns and reach the user as usual.

- **Why not a tool.** ACP gives the client no way to add a tool to an
  agent's own tool set; the application would have to serve an MCP server
  to each session, and the built-in bridges would have to pass it to
  `claude -p` and `codex app-server`, with provider-specific
  registration, approval and failure behaviour to verify for each. A
  fenced block works with every ACP agent, needs no approval, keeps the
  answer visible in the transcript and is parsed with a bounded, strict
  parser, the same shape ADR 0015 allows for a structured-text fallback.
  A tool would give schema-level validation during the turn; the fix-up
  turn below covers that at the cost of one explicit round trip.
- **Parsing.** A missing, unclosed or second block, invalid JSON, unknown
  fields, keys that match a field only when case is ignored and repeated
  keys (checked token by token, since encoding/json would accept both and
  let the loaded plan differ from the text), trailing data, a version other than 1, a rationale over 16 KiB,
  `exec` ("exec is never allowed") and any action outside the ADR 0026 set
  are errors. A unique commit prefix of at least 7 hex digits is expanded
  to the full hash (agents abbreviate); ambiguous or unknown prefixes are
  errors. `update_refs` is dropped when no branch would move, and is an
  error when the plan lists branches that cannot be moved. The result is
  then validated with `protocol.ValidateRebasePlan` against the pinned
  plan, so a merge commit named in the plan, a missing commit or a wrong
  message is reported exactly as for a manual plan. Up to 16 errors of up
  to 512 bytes are kept.

### Result: a revisioned proposal

- The job's `ThreadJob.Proposal` (`GitRebaseProposalSummary`) is in the
  snapshot, so every client sees it and it survives reconnects and
  restarts; the full proposal (the pinned plan, parsed entries, update-refs
  suggestion, rationale, the agent's raw final message, errors, changes,
  stop reason) is a content-addressed file read with
  `GET /v1/git/rebase/proposal?job=`. `Revision` increases with every
  state change. `GET /v1/git/rebase/proposals` lists a repository's jobs.
- **States:** `running`, `proposed`, `invalid` (errors kept, raw text
  kept), `tainted`, `failed` (the turn failed or never started, or the
  server restarted while it ran), `cancelled`, and `stale`, which is
  derived by the proposal read only: a proposed plan whose fingerprint no
  longer matches a fresh plan read. The read also returns the fresh read's
  `Blocked`.
- **Fix-up turns are explicit.** `git.rebase_plan_revise` (the proposal
  revision the user saw, optional instructions) sends the latest errors or
  checkout changes back with the rules; the server never sends one by
  itself. A job takes at most 8 turns (`too_many_turns`), is refused while
  running, and refused with `stale_plan` once the branch changed (start a
  new job). Every revise pins the checkout again.
- **Cancel and end.** `git.rebase_plan_cancel` interrupts the turn (the
  proposal becomes `cancelled`); a revision whose turn has not started (its
  dispatch waits, for example on failing storage) is cancelled directly and
  its unsent prompt dropped; once the turn has started (`TurnID` is the
  revision's prompt) a cancel is refused while its answer is checked, so a
  finished turn is never relabelled. A dispatch that cannot start (the agent is not
  ready, the job's worktree is unavailable, the connection or settings
  failed) ends the revision as `failed` with the reason; `git.rebase_plan_end` deletes the job
  thread (refused while a turn runs or its answer is being checked).
- **Restart.** A revision whose turn was running or not yet dispatched
  becomes `failed` and the thread waits for an explicit revise; a turn that
  ended before its answer was checked is checked at startup.
- **Nothing runs from a proposal.** The client loads `Entries` (and
  `UpdateRefs` as the toggle) into the plan editor, where the user may edit
  them, and starts an ordinary `git.rebase` against a fresh plan read,
  giving the merge and published acknowledgements itself: the agent cannot
  acknowledge anything, and every ADR 0026 check runs again at the start.
- Client helpers: `GitRebasePlanStartCommand`, `GitRebasePlanReviseCommand`,
  `GitRebasePlanCancelCommand`, `GitRebasePlanEndCommand`,
  `Client.GitRebaseProposal`, `Client.GitRebaseProposals`,
  `ProposalLoadable`. Capability `git-rebase-agent-plan` (with
  `git-rebase-interactive`).

## Consequences

- A planning job never blocks other work, but a concurrent change in the
  checkout (another thread, a Git write, a terminal) taints its proposal;
  the user asks again (revise) once the checkout is quiet.
- The job thread's transcript holds the prompt (up to 128 KiB, twice: queue
  and history) until the job is ended; a start that would push the snapshot
  over its limit fails with `capacity`.
- Blobs for plans, checkout pins and results live in the application home
  and are swept an hour after no job references them.

## Known limits

- Enforcement depends on the integration, as stated above: the built-in
  bridges' read-only modes are the providers' documented behaviour, verified
  here only with fake CLIs (the bridge's launch arguments, catalogue,
  refusals and the policy sent to App Server), not against a live account;
  user allow rules and hooks (Claude), MCP tools (Codex) and unverified
  agents can still write the checkout during the turn, which is only
  detected afterwards, not prevented or undone. Ignored files (other than
  info/exclude), other repositories, the global and system configuration
  files themselves when they do not change the effective values, and other
  parts of the Git directory (objects, alternates, `modules/`) are not
  compared. A ref changed by a fetch the user runs meanwhile taints.
- The read-only Claude session is also launched with
  `"disableAutoMode":"disable"` (documented for any settings file: no
  session starts in auto mode, so the auto-mode classifier cannot approve
  what the server would decline); documentation only, not verified against
  a live Claude. `plansDirectory` is not overridden: the settings reference
  says Claude keeps its default (`~/.claude/plans`) for a path outside the
  project, so no server-owned directory can be set. A repository whose own
  settings point `plansDirectory` inside the project would make Claude
  write plan files into the checkout; the pin detects that (tainted).
- Hooks of submodules (`.git/modules/*/hooks`) and other parts of their Git
  directories are not pinned.
- A change the user makes in a terminal (a commit in another worktree, a
  local branch, a global configuration change) during a planning turn
  taints its proposal, without concurrent work being noted.
- Pinning costs a full status (submodules included), two index listings
  and hashing the hooks and info/ trees (at most 4000 entries or 64 MiB
  each) before and after every turn; a larger status or tree cannot be
  pinned and refuses the start.
- The comparison uses stat tokens for changed, untracked and flagged files, so
  touching a file (same content, new mtime) taints; a tracked file changed
  and restored with its old stat within the turn does not.
- Prompt bounds: 300 commits, 128 KiB; long bodies and diffs are cut. The
  agent may read more with its own read-only tools where its permissions
  allow.
- The answer must be in the final agent message segment of the turn; an
  agent that puts it in a thought, a tool call or before a question it then
  asks produces `invalid`.
- `Concurrent` covers application work only.

## Alternatives considered

- **Hold the writer lease** like a resolution job: attributable changes,
  but every planning turn would block other threads and Git writes in the
  checkout. Rejected (reversible) in favour of the after-turn comparison.
- **An MCP tool** for the answer: see above.
- **Automatic fix-up loops:** rejected; each extra turn is the user's
  explicit action, bounded at 8 turns per job.
- **Loading tainted proposals:** rejected; the user can read them and ask
  again.
