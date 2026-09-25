---
status: accepted (user decisions 2026-09-24)
---

# Git ref and remote actions: branch, switch, soft reset, fetch, pull, push

[ADR 0020](0020-git-write-actions.md) added durable, pinned Git writes for
paths and commits. The Git client also needs branch creation, branch
switching, soft reset (with undo), fetch, fast-forward-only pull, ordinary
push and a way to cancel long network operations. These move refs, rewrite
working-tree files or contact remotes, so they need their own safety,
credential and cancellation rules.

## Decision

- **Same durable flow.** `git.branch_create`, `git.switch`,
  `git.reset_soft`, `git.fetch`, `git.pull` and `git.push` use the two-phase
  journaled command of ADR 0020: refusals record nothing, a running receipt
  and `GitOp` are persisted before Git starts, a retry never runs Git again,
  and a restart turns a running receipt into `outcome_unknown`. Each kind has
  one typed sub-payload (`GitWrite.Ref` or `GitWrite.Sync`) pinning exactly
  what the user saw: the checked-out branch, HEAD, the target's tip and, for
  pull and push, the upstream.
- **Lease split.** Switch, soft reset and pull change HEAD, the index or the
  working tree and hold the checkout writer lease (refused while a thread
  holds it; turns wait while they run). Fetch, push and branch creation
  change neither, so they take only a Git slot (`git_busy`) and run beside
  agent turns. The slot allows one Git write per worktree; a ref or remote
  action also excludes every Git write in the repository's other linked
  worktrees, which share its refs (keyed by the common Git directory). Push therefore pins the branch
  tip immediately before Git runs and reports `pushed_newer_head` if the
  remote-tracking tip after the push is not the reviewed HEAD, rather than
  blocking agent turns for up to 15 minutes.
- **Credentials: non-interactive only** (user decision). Credential helpers,
  `ssh-agent` (`SSH_AUTH_SOCK` is kept) and already-trusted host keys work as
  in the user's CLI, and so do the user's environment settings for it:
  `GIT_SSH`, `GIT_SSH_COMMAND`, `GIT_SSH_VARIANT`, `GIT_SSL_*`,
  `GIT_PROXY_COMMAND`, `GIT_PROXY_SSL_*`, `GIT_HTTP_*`,
  `GIT_CONFIG_GLOBAL`/`SYSTEM`/`NOSYSTEM`, `GIT_CEILING_DIRECTORIES`,
  `GIT_ALLOW_PROTOCOL`, `GIT_PROTOCOL_FROM_USER`,
  `GIT_AUTHOR_*`/`GIT_COMMITTER_*` and the proxy variables. Every other
  `GIT_*` variable is removed for all writes, in particular those that
  redirect the repository (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`,
  object and common directories, `GIT_NAMESPACE`) or inject configuration
  (`GIT_CONFIG_COUNT`/`KEY`/`VALUE`, `GIT_CONFIG_PARAMETERS`). Nothing
  prompts: `GIT_TERMINAL_PROMPT=0`, an empty `GIT_ASKPASS` (which also stops
  Git consulting `core.askPass` and `SSH_ASKPASS`), `SSH_ASKPASS` removed,
  `SSH_ASKPASS_REQUIRE=never`, `GCM_INTERACTIVE=never`, and Git has no
  controlling terminal. Fetch, pull and push also run without `DISPLAY` and
  `WAYLAND_DISPLAY`, so they start no graphical prompt; commits keep
  `DISPLAY` for a graphical signing pinentry (ADR 0020). A signed push
  (`push.gpgSign`) therefore fails with `signing_failed` unless gpg-agent
  can sign without asking; a gpg-agent that was itself started with a
  display may still show its own pinentry, which this process cannot
  prevent. Failures are classified from untranslated
  output (`LC_MESSAGES=C`; other locale categories are kept) as
  `auth_required`, `host_key_unknown`, `agent_unavailable` or `transport`,
  plus `timeout` and `cancelled`, always with Git's bounded raw output. The
  advice for host keys and passphrases is to fetch once in a terminal (or
  load the key into `ssh-agent`) and retry.
- **Switch carries with explicit confirmation** (user decision). Git's own
  semantics apply: what Git carries is carried, and when a local change or
  an untracked (including ignored, via `--no-overwrite-ignore`) file would
  be overwritten, Git refuses before anything moves (`would_overwrite`, with
  the parsed paths). With a dirty working tree the request must carry
  `AcknowledgeCarry` equal to the number of status entries the user saw and
  a `WorktreeFingerprint` (`protocol.GitWorktreeFingerprint`, a digest of
  every entry's pin) that the server recomputes from a fresh status. It
  never uses `--merge`, `--discard-changes`, `-f`, stash or submodule
  recursion, and is refused during an operation, with conflicted paths, or
  when the branch is checked out in another worktree. Switching away from a
  detached HEAD whose commits no branch, tag or remote-tracking ref contains
  needs `AcknowledgeLeaveCommits` equal to their number (`leaves_commits`).
  Locks on HEAD, the index, the branch refs involved and `packed-refs` are
  refused up front, because Git rewrites files before it updates refs;
  when Git fails with HEAD unchanged the server compares the working tree
  and index with the checked state and reports `partial_switch`
  (`outcome_unknown`) if they changed. `would_overwrite` paths are Git's
  verbatim names; `PathsIncomplete` marks a list that cannot be parsed
  reliably (a name with a newline). The fingerprint is a
  shared protocol function rather than a new `GitStatus` field, so the read
  contract is unchanged.
- **Soft reset** runs `git reset --soft <full oid>` after rechecking HEAD. It
  is refused on an unborn branch, to HEAD itself, with unmerged paths and
  during any operation. `AcknowledgePublished` is required when commits
  leaving the branch are on any remote-tracking ref (unknown counts as
  published); `AcknowledgeNotAncestor` when the target is neither an
  ancestor nor a descendant of HEAD. The result reports the previous tip
  (Git also records `ORIG_HEAD`); undo is a pinned soft reset back to it,
  which as a descendant needs no acknowledgement.
- **Pull is fetch plus an explicit fast-forward decision.** The server never
  runs `git pull`. Like it, it fetches exactly the upstream branch
  (`git fetch <remote> <upstream ref>`, read from `FETCH_HEAD`; the
  remote-tracking ref is updated opportunistically when the remote's
  refspecs map it, so a refspec that excludes the branch cannot hide it),
  then compares HEAD with the fetched commit: `up_to_date` and `ahead` succeed without change,
  `diverged` stops with Fetch and Integration reported separately ("fetched,
  not integrated"), and only `fast_forward` integrates with `git merge
  --ff-only --no-autostash --no-overwrite-ignore <tip>`, so inherited
  `pull.rebase`, `pull.ff`, `merge.ff` and `merge.autostash` cannot turn it
  into a merge, rebase or stash. A pull needs no carry acknowledgement:
  Git's fast forward carries unrelated local changes and refuses
  (`would_overwrite`) when it would overwrite one. Pulls from detached HEAD,
  without upstream, from a local (`.`) upstream, on an unborn branch or
  during an operation are refused.
- **Push is plain** (user decision). `git push --porcelain --no-follow-tags
  <remote> refs/heads/<branch>:<upstream ref>` pushes only the current
  branch to its configured upstream: never `--force`,
  `--force-with-lease` or a `+` refspec, and the pre-push hook runs.
  As with the CLI's `push.default`, an upstream with a different branch name
  is used only with `push.default=upstream` (or `tracking`); `simple` (the
  default), `current` and `matching` refuse it (`upstream_name_mismatch`)
  and `nothing` refuses every push. It is
  refused for detached HEAD, a missing or gone upstream, a mirror remote, a
  configured `remote.<name>.push` refspec (such as Gerrit's `refs/for/*`), a push remote that
  differs from the upstream's remote, a local upstream, and whenever the
  local remote-tracking tip is not an ancestor of HEAD (`behind_upstream`).
  The optional `ExpectedUpstreamOid` pins the tip the user saw. Remote
  rejections (non-fast-forward, fetch first, remote hooks) and pre-push hook
  refusals are reported as `rejected` with the reason. A pre-push hook
  refusal is claimed only when Git's trace2 events (written to a private
  file for the push) show the pre-push hook, from the hooks directory or a
  `hook.<name>` entry, exiting non-zero; a push whose connection died, or whose
  remote reported `remote failure` or no status, is `outcome_unknown`
  (the remote may have accepted it). Signing failures are `signing_failed`.
- **Budgets and progress.** Fetch and pull end Git after 60 s without
  output, push after 5 minutes because a pre-push hook may run silently
  (`timeout`, worded "no output ... (a hook or the remote)"), and all
  three after 15 minutes in total; switch and reset have 5
  minutes. Git runs with `--progress`; its progress lines are parsed into
  `GitOp.Progress` (published at most four times a second, never journaled,
  cleared when the command ends and on restart) and kept out of the bounded
  output.
- **Cancel.** `git.cancel` names a running command. Fetch and push are
  cancellable throughout, pull only while it fetches (`GitOp.Cancellable`
  turns false before integration, atomically with any pending cancel);
  switch, reset and branch creation are never cancelled once started. The
  cancel request itself is not journaled; the cancelled command's receipt
  records the outcome.
- **Buffer coordination.** Switch and pull integration call
  `beginWorktreeRewrite` around the Git command that rewrites files. It
  asks the shared-document coordinator (`beginDocumentRewrite` /
  `endDocumentRewrite`) to finish pending saves and pause autosave for
  every open document whose root overlaps the repository toplevel, and
  afterwards to reconcile and resume, after success, failure and
  cancellation alike. If a document cannot be saved first, Git does not run
  (`document_unsaved`). Because saving can change the working tree, switch
  re-checks the fingerprint and carry count afterwards (`stale_status`),
  and both switch and pull take their pre-run snapshot only after the
  save. The snapshot also records every path that differs between the two
  commits, ignored or not, so `partial_switch` covers files Git wrote even
  where status does not list them. Pull fetches with `--write-fetch-head`
  and accepts only the `FETCH_HEAD` line for the upstream branch, so
  `fetch.writeFetchHEAD=false` cannot make it integrate an older fetch.
  Leave-commit counts exclude the switch target (creating a branch at the
  detached HEAD leaves nothing behind) and are recounted just before Git
  runs. Classification reads the head and the last 32 KiB of Git's output. A document opened after the pause begins is not
  paused.

The wire contract, including every code, is in
`apps/go/internal/protocol/git_write.go`; the Go client builds commands with
`client.GitBranchCreateCommand`, `GitSwitchCommand`, `GitSwitchCreateCommand`,
`GitResetSoftCommand`, `GitUndoResetSoftCommand`, `GitFetchCommand`,
`GitPullCommand`, `GitPushCommand` and `GitCancelCommand`
(`apps/go/internal/client/git_remote.go`).

## Consequences

- Network operations can run for minutes while agent turns continue (except
  pull). A fetch or push can race an agent's own Git use; Git's ref locks
  arbitrate, and a lost race fails honestly.
- A push can publish a commit an agent made after the user's review when the
  branch moves in the tiny window after the pinned check; the result says so
  (`pushed_newer_head`) but cannot undo it.
- Remotes that need interactive authentication or an unknown host key cannot
  be used until the user sets them up in a terminal.
- Failure classification depends on Git's and SSH's English messages; an
  unrecognized failure falls back to `transport` with the raw output, and
  for push to `outcome_unknown`.
- Writes honor `GIT_CONFIG_GLOBAL` and similar variables that the read
  policy removes, so reads and writes can see different global
  configuration when those are set.
- Progress publication bumps the snapshot revision without persisting it; a
  snapshot saved for another reason may contain progress, which startup
  clears.

## Deferred

Merge and rebase as explicit follow-ups to a diverged pull, force push,
background or automatic fetch, fetching or pushing other branches or tags,
setting upstreams, triangular (separate push remote) workflows, submodule
fetch/update, mixed and hard reset, switching to remote branches with
tracking, deleting or renaming branches, interactive credential entry, and
real editor-buffer coordination.

## Alternatives considered

- **`git pull --ff-only`:** its behaviour depends on `pull.*` and `merge.*`
  configuration (autostash, rebase) and hides the fetch/integration split.
  Rejected for an explicit fetch plus decision.
- **`--force-with-lease` or an oid source refspec for push:** either pins the
  exact remote or local commit, but the first is a force push the user ruled
  out and the second changes what the pre-push hook sees. Rejected for CLI
  parity plus a pinned check and an honest warning.
- **Taking the lease for push and fetch:** would stop agent turns for the
  whole network operation although neither touches the working tree.
- **Stashing or `--merge` to switch with changes:** rejected by the user in
  favour of Git's carry semantics with explicit acknowledgement.
