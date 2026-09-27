---
status: accepted (user decisions 2026-09-26; orchestrator defaults marked reversible)
---

# Interactive rebase

[ADR 0023](0023-merge-rebase-operations.md) added merge and non-interactive
rebase as journaled operations that stop, reserve the checkout and continue
across commands and restarts. On 2026-09-26 the user replaced
"non-interactive rebase only" with interactive rebase, planned manually or by
an agent (see [git-client.md, Pull and integration](../design/git-client.md#pull-and-integration)).
The research in
[go-interactive-rebase-2026-09-27](../research/go-interactive-rebase-2026-09-27.md)
mapped Sublime Merge's history editing to `git rebase -i` and showed how to
drive it without an editor. This records the server, protocol and client
slice (handoff slice 4); the TUI plan editor (slice 5) and agent planner
(slice 6) build on it.

## Decision

### User decisions (2026-09-26)

- **Parity target:** everything Sublime Merge does for history editing,
  as the research's todo subset: `pick`, `reword`, `edit`, `squash`,
  `fixup`, `fixup -C`, `fixup -c`, explicit `drop`, `break`, and reordering.
  Sublime Merge's one-shot items (edit message, edit contents, squash with
  parent or selection, fixup, drop, move up/down, rebase onto) are TUI
  presets that fill the same plan; there is no second server path.
- **`exec` lines are not supported.** A plan with any other action is
  invalid.
- **Merge commits in the rewritten range are flattened with a warning.**
  The plan read flags them (`Merge`, `MergeCount`); a plan may not name
  them, and starting needs `AcknowledgeMerges`. The rebase never uses
  `--rebase-merges`: Git drops the merge commits and replays their side
  commits linearly, and the confirmation must say so.
- **`--update-refs` is a per-plan toggle, default off.** The plan read lists
  the local branches that would move (`UpdateRefs`), so the confirmation can
  name them.
- **Cherry-pick and revert are deferred.**
- Earlier decisions stand: a clean tracked tree is required and nothing is
  ever stashed; rewriting published commits needs an acknowledgement; the
  application never force-pushes; Pull stays fast-forward only.

### Protocol

- **Plan read.** `GET /v1/git/rebase/plan?base=&onto=` (`GitRebasePlan`):
  `base` is a full ref, a full hash or `root`; `onto` is optional (default
  base; for `root` without onto Git creates a new empty root). It returns
  the checked-out branch and HEAD, the commits of `base..HEAD` oldest first
  in Git's todo order (`rev-list --topo-order --reverse`) with parents,
  subject, body (16 KiB), author, `Published` (reachable from a
  remote-tracking ref; unknown counts as published) and `Merge`, the
  `UpdateRefs` candidates, a `Fingerprint` over branch, HEAD, base, onto,
  commit list and update refs, and `Blocked` with the start refusal a start
  would get now (dirty tree, detached, operation in progress, empty range,
  more than 1000 commits, files in the way). "Rebase onto upstream" uses the
  upstream ref as base; "interactive rebase from here" uses the selected
  commit's parent, or `root` for a root commit (`--root`).
- **Start.** `git.rebase` with `GitIntegrate.Interactive`
  (`GitRebaseInteractive`: base and onto as read, fingerprint, ordered
  `GitRebaseEntry{Action, Commit, Message, EditMode, Fixup}`, `UpdateRefs`,
  `AcknowledgeMerges`) plus the usual `ExpectedBranch`, `ExpectedHead` and
  `AcknowledgePublished`. Target fields stay empty.
- **Validation** is `protocol.ValidateRebasePlan(plan, entries)`, shared by
  the server, the TUI and the agent planner: every non-merge commit of the
  plan exactly once by full hash and nothing else; break names no commit;
  a `squash` or `fixup` directly follows a pick, reword, edit, squash or
  fixup (not a drop or break); `reword` carries a message; a chain (a
  commit-producing entry and the squash/fixup entries after it) that
  contains a `squash` or `fixup -c` carries its combined message on its
  last entry, and no other entry carries a message; messages are UTF-8
  without NUL, at most 64 KiB, not blank. The server also re-reads the plan
  (`stale_plan` when the fingerprint changed), checks the acknowledgements
  (`merges_unacknowledged`, `published_commit`), identity, locks, and runs
  the ADR 0023 pre-scan for files in the way over the range.
- **Stops.** `GitOperationState.Interactive` (`GitRebaseProgress`) reports
  todo progress for any merge-backend rebase and, for an application plan,
  `Stop`: `conflict`, `edit_amend`, `edit_reset`, `break`, `message`,
  `commit_failed`, `empty` or `unknown`, derived from the repository
  (unmerged entries, the last `done` line, Git's `amend` marker and its
  parent compared with HEAD, staged changes) and the plan entry's edit mode
  (kept per todo line in the record, so a commit made at an amend stop
  does not turn it into a reset stop), the plan `Entry`, and `Failure`
  (`signing_failed`, `hook_rejected`, `helper_failed`) with `Hook` and
  `Detail` when the latest command reported one for this very stop (kept in
  `GitOperationRecord.Interactive.Failure`, keyed by the ADR 0023 stop key).
  `Can` is recomputed: Continue is available at edit, break and message
  stops too (not with unstaged tracked changes, nor with staged changes at a
  break); Skip only where Git could not apply a commit (conflict, commit
  failure, became empty). Continue is refused at a reset stop with nothing
  staged and no commit made there (`nothing_to_recommit`: it would drop the
  commit; stage its changes or abort), and at an amend stop where commits
  were made and changes are still staged (`staged_changes`).
- **Messages at stops (verify round 2026-09-26).** The helper never
  derives a message from Git's commented editor text (removing comments
  would remove the user's own `#` lines). Before every Continue or Skip the
  server writes the message of the step Git stopped at: the Continue's
  `Message`, else the plan's, else the message the step would have had
  without the stop: the chain's stored message for a squash or fixup in a
  chain that has one, HEAD's (the accumulated) message for a plain fixup,
  and otherwise the step commit's own message read with `git cat-file`.
  Every message of an application rebase is committed **verbatim**
  (`commit.cleanup=verbatim`, and `--cleanup=verbatim` on the server's own
  commits; second verify round 2026-09-26): the helper always writes the
  complete message, so no Git template or comment text can survive, and
  original and plan messages keep trailing spaces, repeated blank lines
  and `#` lines byte for byte. Probed on Git 2.55 for reword, the end of a
  squash chain, a plain fixup chain and `fixup -c`, and tested through the
  server with a resolved conflict. A final newline is added when a
  message lacks one. Under verbatim Git refuses only a completely empty
  message (a blank one is accepted), so the helper refuses any message
  that is empty or only whitespace. `git.operation_commit`, an ordinary
  new commit, keeps `git.commit`'s whitespace cleanup. `Message` is
  accepted only where the continue commits this step with its own message
  (pick, reword, edit, or the end of a chain carrying the plan's message);
  elsewhere it is refused (`invalid`), so a stop cannot change the plan's
  messages.
- **Stop snapshots (third verify round 2026-09-26).** Whether Git had
  already made a step's commit when it stopped cannot be re-derived from
  content later (a prepare-commit-msg hook changes the message; a commit
  made at the stop in a terminal or before a crash looks like progress), so
  the server records the stop's facts once, when a command of its own first
  observes it right after Git ran (`GitRebaseRecord.Snapshot`, keyed by the
  number of `done` lines and the step commit): HEAD then, the HEAD before
  the step's commit, whether Git had committed the step and whether the
  step had become empty. The stop's identity is the number of `done`
  lines, the step commit and the next todo line (a step Git rescheduled
  keeps the same `done` count). Git had committed a pick, reword or edit
  step, with a clean index and no conflicts, when (fourth verify round):
  HEAD is the step commit itself (fast-forwarded) or Git's `amend` marker;
  never when HEAD is an earlier result (onto, an original commit of the
  branch, that is an ancestor of `orig-head`, a commit in `rewritten-list`,
  or a commit the server made at an earlier stop); otherwise only when HEAD
  carries the step commit's author name, email and date (Git keeps the
  author on pick, reword and edit) and introduces exactly the step's change
  (contained in HEAD, not in HEAD's parent). Otherwise the step became
  empty when every path its commit changes (gitlinks and mode changes
  included, without a size limit) already has that content on HEAD. Later
  reads classify relative to the snapshot:
  - an empty stop keeps the commit on Continue only while HEAD is still the
    pre-step HEAD, and Skip is offered only then; once a commit was made at
    the stop (in a terminal, or by an attempt the server crashed after),
    Continue just continues and nothing is made twice;
  - a reword whose message step failed after Git made its commit is amended
    with the planned message only while HEAD is still that commit; HEAD
    amended since (its parent is the pre-step HEAD) counts as done; HEAD
    anywhere else refuses Continue (`not_supported`); Skip is never
    offered there, since it would silently drop the reword;
  - a stop no command observed (a crash between Git and the journal, or
    Git run elsewhere) gets a snapshot marked `Late` on the next read: at
    empty and message stops, where a guess could duplicate or drop a
    commit, Continue and Skip are refused (`stop_unobserved`) and Abort
    stays available;
  - a continue or skip that leaves the same step (a resolved conflict that
    then stops at the reword's message) records the step's facts again;
  - a stop recorded with conflicts or staged changes whose index is now
    clean with HEAD a child of the recorded HEAD was committed at the stop
    (in a terminal, or by the server just before a crash): stop `committed`,
    Continue runs Git's `--continue` without committing, Skip is refused,
    and the commit is listed for Abort;
  - a step Git put back into the todo list (its last `done` line is also
    the next todo line, for example an untracked file in the way of a pick)
    is stop `rescheduled`: Continue retries it once the obstacle is gone.
- **Edit steps that conflict (fifth round).** Git itself does not stop
  again for editing when an `edit` step's pick needed resolving: its
  `--continue` commits the resolution and goes on. The plan's edit stop is
  honoured without writing Git's state files: on Continue at such a step
  (resolution staged, no `amend` marker), the server commits the
  resolution itself (the step commit's author and raw message byte for
  byte, or the Continue's `Message`), does not run `--continue`, soft-resets
  it in reset mode, records the stop in the record (`GitRebaseRecord
  .EditStop`: the stop's identity, the resolution commit and its parent)
  and presents it as the entry's edit stop (`GitRebaseProgress.ServerEdit`,
  with the resolution commit as `Amend`). Every edit rule applies
  unchanged: at amend mode, Continue amends with what is staged (a
  server-made amend) and then runs `--continue`; at reset mode, commit
  parts, Continue recommits the rest, and nothing staged with nothing
  committed is refused. Git sees the step's commit already made, so its
  later `--continue` just proceeds. The resolution commit is a stop commit
  (listed for Abort and kept by the backup ref), and the stop survives a
  restart. A crash between the resolution commit and the journal leaves
  the stop as `committed` (Continue goes on without the edit stop; nothing
  is lost or made twice).
- **Abort count.** When HEAD left the stop's history, or the commits
  beyond the stop's HEAD exceed the 64 walked, `AbortDropsCount` is a
  lower bound (`AbortDropsAtLeast`); HEAD stands for the unknown rest and
  the backup ref keeps everything.
- **Prefills.** The plan read returns each commit's raw message
  (`GitRebasePlanCommit.Message`, byte for byte, 64 KiB); the operation
  read returns the message the stopped step would commit with
  (`GitRebaseProgress.StepMessage`). `base=upstream` resolves the branch's
  configured upstream (local ones included) on the server
  (`no_upstream` without one; `upstream_missing`, naming the configured
  ref, when it is configured but does not exist here).
- **Empty commits.** A commit of a pick, reword or edit step that became
  empty stops (`--empty=stop`); Continue keeps it (`commit --allow-empty`,
  original author, the original message or, for a reword, the planned one)
  and goes on, Skip drops it. A kept empty edit step does not stop for
  editing.
- **Empty messages.** A pick or edit step whose commit has an empty message
  and whose resolution is staged is committed by the server itself
  (`--allow-empty-message --reuse-message`) before continuing, since the
  helper refuses to hand Git a blank message.
- **Non-UTF-8 messages.** A commit with an `encoding` header or a message
  that is not valid UTF-8 blocks the plan (`unsupported_message`, naming
  the commits), since JSON would alter its bytes.
- **Work made at stops and Abort.** Commits the server makes at stops
  (`git.operation_commit`, the recommit at a reset stop, the amend at an
  amend stop, a kept empty commit, a reworded amend) are recorded
  (`GitRebaseRecord.StopCommits`, and per command
  `GitOperationResult.StopCommits`); a commit made at an edit stop in a
  terminal at any stop is detected as a commit on HEAD beyond the stop
  snapshot's HEAD that the server did not make. While any exist, the state lists them in
  `AbortDropsCommits`/`AbortDropsCount` and Abort needs `AcknowledgeDropped`
  (`drops_unacknowledged`), exactly as for a cherry-pick sequence. Before
  every abort of an application interactive rebase, the server points
  `refs/tui-go/rebase-backup/<operation>` at the rebase's HEAD
  (`GitOperationResult.BackupRef`, named in the result) so whatever it made
  stays recoverable; the ADR 0023 backups hold files, not commits, so a
  namespaced ref was chosen. Every listed commit HEAD does not reach (a
  resolution commit a reset stop soft-reset, a stop commit amended or
  replaced later) gets its own ref, `<that ref>-<n>`, named in
  `GitOperationResult.BackupRefs` and the result message, so an abort
  keeps every commit it lists; the abort is refused (`backup_incomplete`)
  if any ref cannot be written. The application never deletes it
  (`git update-ref -d <ref>` does). Recorded stop commits also make a
  retried Continue idempotent: a kept empty commit or reworded amend already
  made at this stop is not made again, and the recommit and amend at edit
  stops consume the staged changes, so a retry finds nothing left to
  commit. Continue re-checks the stop (unstaged changes included) after
  open documents are saved.
- **Commands at stops.** `git.operation_continue` may carry `Message` (the
  commit this stop makes or amends). New `git.operation_commit` commits the
  staged changes at an edit or break stop and stays stopped (splitting a
  commit, or inserting one). Client helpers: `Client.GitRebasePlan`,
  `DefaultRebaseEntries`, `GitRebaseInteractiveCommand`,
  `GitOperationContinueMessageCommand`, `GitOperationCommitCommand`.
  Capability `git-rebase-interactive` (Git 2.38 or newer).

### Driving Git

- **Invocation.** `git -c <neutralising overrides> rebase --interactive
  --no-autosquash --no-autostash --no-fork-point --reapply-cherry-picks
  --empty=stop --no-rerere-autoupdate --update-refs|--no-update-refs
  [--onto <onto>] <base>|--root`, with the ADR 0023 write environment and
  `rerere.autoUpdate=false`, `submodule.recurse=false`.
  `--reapply-cherry-picks` makes Git's todo contain every commit the plan
  lists, so an already-upstream commit is the user's to drop, not Git's.
- **Helpers (orchestrator choice, reversible).** The server re-invokes its
  own binary in a hidden mode, `tui-go git-rebase-helper sequence|message
  <file>`, as `GIT_SEQUENCE_EDITOR` and `GIT_EDITOR` (environment beats the
  `core.editor=:`/`sequence.editor=:` of every write; tests run the helper
  from `TestMain`). The pinned data lives in a per-operation file in the
  application home (`git-rebase/<hash of the operation ID>/plan.json`,
  directory 0700, file 0600), never in the repository; the environment
  carries its path and a random token that must match it, and the helper
  accepts only files inside that operation's Git directory. The sequence
  helper checks that Git's generated todo names exactly the pinned commits
  (abbreviated hashes matched by prefix) and update-ref branches, then
  writes the pinned todo (full hashes); any mismatch fails it and Git does
  not start. The message helper takes the step from the last line of
  `rebase-merge/done`, requires it to be the pinned line at that position,
  and writes the Continue override for that step, else the plan's stored
  message, else Git's prepared message without comment lines (for steps the
  plan does not reword, such as committing a resolved pick); an empty
  result fails. A failure is written to `helper-error` for the server.
  A POSIX-shell helper was rejected (fragile parsing, quoting); in-process
  editors are impossible because Git runs editors as processes.
- **Neutralised configuration (orchestrator default, reversible).**
  `rebase.autoSquash`, `autoStash`, `updateRefs`, `rebaseMerges`,
  `forkPoint`, `rescheduleFailedExec`, `stat` off, `rebase.backend=merge`,
  `rebase.missingCommitsCheck=error`, `rebase.abbreviateCommands=false`,
  `rebase.instructionFormat=%s`, `rerere.enabled=false`,
  `commit.cleanup=verbatim` (see messages below), `commit.verbose=false`,
  `advice.waitingForEditor=false`. The user's `core.commentChar` is kept:
  an earlier draft forced `#`, which would make Git's own fixup cleanup
  remove `#` lines even for users who chose another comment character.
  **User configuration honoured:** everything else, as for every write
  (ADR 0020/0021): identity, `commit.gpgSign` and signing programs, hooks
  (`core.hooksPath`), merge drivers and attributes. The research found that
  the user's XDG global configuration applies even when only
  `GIT_CONFIG_NOSYSTEM` is set; this is intended (CLI parity), and every
  setting that would change the plan's meaning is overridden on the command
  line, which wins over all configuration files.
- **Signing honoured (orchestrator default).** A commit that cannot be
  signed (no pinentry: `GPG_TTY` is removed) stops the rebase with
  `Failure signing_failed`, recognised from Git's output: "failed to write
  commit object" together with one of Git's own `error:`/`fatal:` signing
  lines ("gpg failed to sign the data", "Couldn't load public key", ...),
  so other output mentioning "sign" (a path like `assign.go`) does not
  count. Git
  records the signing choice at the start (`gpg_sign_opt`), so Continue
  signs again; fix signing and continue, or abort.
- **Hooks run (orchestrator default).** A hook that refuses (pre-rebase,
  pre-commit, prepare-commit-msg, commit-msg, pre-merge-commit exiting
  non-zero, found in trace2 events of every Git process of the command) is
  `hook_rejected` with its name: at the start nothing runs
  (`not_started`), later the rebase stops and Continue retries.
- **rerere is disabled** for application rebases (orchestrator default):
  no preimage is recorded or replayed.
- **Edit modes (orchestrator default: reset).** `EditMode reset`, Sublime
  Merge's "edit commit contents", is the default: when Git stops at the
  entry, the server immediately runs `git reset --soft <parent>` in the same
  command, so the commit's changes are staged and can be committed again or
  split (`git.operation_commit`); Continue commits what is still staged
  with the original message (or `Message`) and the original author, then
  continues. A root commit cannot be reset and stays applied (the result
  says so). `EditMode amend` stops with the commit applied; Continue amends it
  with what is staged (the server's own `commit --amend`), using `Message`
  or keeping its message byte for byte.
  Commits made at an edit-reset stop keep the edited commit's author; at a
  break they are the user's.
- **Message steps.** Git makes a reword's commit with the old message before
  running the editor. When the editor, a hook or signing fails there, the
  commit exists with its old message and Git's own `--continue` would keep
  it; the server's Continue therefore amends HEAD with the stored (or
  given) message first, after checking that HEAD's author and message are
  exactly the original commit's, then continues. Squash, fixup and pick
  retries are Git's own `--continue`, with the helper supplying the stored
  message.
- **Abort** is Git's `rebase --abort`, with every ADR 0023 check, backup
  and discard acknowledgement. It restores the original branch and HEAD;
  `--update-refs` branches are only written when Git finishes, so an abort
  leaves them where they were. The server verifies the branch and each
  pinned update-ref branch after an abort and reports any that differ
  (`abort_refs_moved`) instead of moving them.
- **Backstop.** When Git leaves rebase state after rejecting the todo
  without running a step (no `done` file), the server aborts it and reports
  `todo_rejected` (`not_started`); normally validation prevents this.
- **Records and recovery.** The operation record (`GitOperationRecord`)
  gains `Interactive` (`GitRebaseRecord`: fingerprint, base, root, entry
  count, the todo line-to-entry map, pinned update refs, dropped merges and
  the latest failure). Target is the onto commit; for `--root` without onto
  it is learned from Git's `onto` file when first observed, and until then
  the record matches by original head. After a server restart the record,
  the repository and the plan file together present the same stop, and
  Continue works; when the plan file is missing, Continue, Skip and
  `git.operation_commit` are refused (`plan_missing`) and Abort still
  works. Per-operation directories are removed when the rebase ends and
  swept (at startup and after operation commands) once no active record
  uses them.
- **External interactive rebases** (started in a terminal) keep ADR 0023's
  behaviour: their interactive stops are left to the terminal. The todo
  progress is reported for them too.
- **Plan files** are removed when the rebase ends here, when a read
  observes that it ended elsewhere, and by the startup sweep. The helper
  refuses symlinked todo or message files. Branches pointing into the range
  whose names cannot be handled (not valid UTF-8) are listed in
  `UpdateRefsUnsupported`; starting with update refs is then refused
  (`update_refs_unsupported`).

## Consequences

- A plan's messages, including agent-proposed ones, are stored in the
  application home for the rebase's duration and removed afterwards.
- The helper is the running binary: replacing the binary on disk while a
  rebase is stopped makes the next Continue run the new binary's helper,
  which refuses a state file of another version (`helper_failed`).
- The token only binds the helper to its state file. Hooks inherit the
  editor environment, so a hook that runs `git commit` with an editor
  during a message step receives that step's stored message.
- Continue, Skip and `git.operation_commit` need the stored plan; a home
  restored without it leaves the rebase to a terminal or Abort.
- Clients must show the merge-flattening and update-refs consequences before
  starting; the server only checks the acknowledgements.

## Known limits

- Git's internal state files (`done`, `amend`, `update-refs`,
  `gpg_sign_opt`) are not a documented interface; the classification is
  pinned by tests against Git 2.55 and needs re-verification on upgrades.
- The plan read limits a range to 1000 commits and 32 MiB of log output.
- Order is not verified against Git's generated todo, only the set of
  commits, since Git's order around merges may differ from `rev-list`
  while the pinned todo replaces it anyway.
- Work done in a terminal is listed for the abort acknowledgement only
  when it is on HEAD beyond the stop's snapshot HEAD (within the last 64
  first-parent commits); anything else is still kept by the backup ref.
- The committed-step test relies on Git keeping the step commit's author
  (name, email, date) and on the change it introduces: a previous result
  made in a terminal at an earlier stop with exactly the same author, date
  and change as the next step's commit would be taken for that step's
  commit. `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL` and `GIT_AUTHOR_DATE` in the
  server's environment are passed to Git (CLI parity) and override the
  author of every commit Git makes, which defeats this test (every step
  then looks like the previous one); do not run the server with them set.
- A prepare-commit-msg hook's additions are kept on steps whose message Git
  itself composes (pick) but replaced on steps whose message the helper
  writes (reword, the end of a squash chain, `fixup -c`, a resolved
  conflict): the helper writes the complete stored message, and honouring
  hook additions selectively would make the stored message ambiguous.
- Skipping the last squash or fixup of a chain leaves the chain's earlier
  commit with the message Git gives it (its own), not the plan's combined
  message.
- A server crash between a stop commit and the journal leaves that commit
  unrecorded: a retried Continue may then make it again (only for a kept
  empty commit or a reworded amend), and Abort does not list it (the backup
  ref keeps it).
- A commit left untracked after a soft reset (a file the edited commit
  added and the user unstaged) is not committed by Continue; it stays in
  the working tree, and files in the way of later commits refuse Continue
  as in ADR 0023.
- Hook detection relies on trace2 (Git 2.36+ for hook events); signing
  detection on untranslated Git messages.
- Only the application rebase's own stops are handled; `exec`, `label`,
  `reset` and `merge` lines never occur in its todo and an external one
  with them stays terminal-only.

## Alternatives considered

- **`--rebase-merges`:** rejected by the user; merges are flattened with an
  acknowledgement.
- **Omitted lines as drops:** rejected; `rebase.missingCommitsCheck=error`
  and the validator require an explicit drop.
- **Autosquash:** not used; a client may propose a plan from `fixup!`
  subjects, which the server validates like any other.
- **Popping messages in order** (the probe's helper): rejected for keying by
  the `done` position, so a retried step gets its own message again.
