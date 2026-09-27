# Interactive rebase: Sublime Merge parity and driving `git rebase -i` — 2026-09-27

Research for slice 1 of [the 2026-09-26 handoff](../implementation/handoff-2026-09-26.md#next-run-git-history-editing-and-fetch-finish-in-one-run).
Context: [git-client.md, Pull and integration](../design/git-client.md#pull-and-integration),
[ADR 0023](../adr/0023-merge-rebase-operations.md), [ADR 0021](../adr/0021-git-ref-and-remote-actions.md).
This note records evidence and a recommendation. Nothing here is an accepted decision; the open
questions in the last section need the user.

Evidence labels: **verified here** = observed with the probes below on Git 2.55.0 (Linux,
2026-09-26); **documented** = stated by the cited upstream source, not exercised here.

## 1. Sublime Merge parity list

Sublime Merge's official documentation is thin on history editing. The docs index
(<https://www.sublimemerge.com/docs/>) has no rebase page; the relevant official sources are:

- [Getting started — Fixing and editing commits](https://www.sublimemerge.com/docs/getting_started) (S1)
- [Blog: Sublime Merge Tips — Editing Git Commits](https://www.sublimemerge.com/blog/sublime-merge-tips-editing-git-commits) (S2)
- [Changelog on the download page](https://www.sublimemerge.com/download) (S3) and [Build 1092 notes](https://www.sublimemerge.com/blog/sublime-merge-build-1092) (S4)
- [Menus reference](https://www.sublimemerge.com/docs/menus) (S5) — lists `Commit.sublime-menu` but not its entries.
- Command names `move_commit` (arg `down`/up), `squash_commit` ("with its parent"), `fixup_commits`,
  `edit_commit_contents` come from a third-party key-binding article
  ([viteinfinite, 2022](https://viteinfinite.com/2022/12/rebase-key-bindings-for-sublime-merge/), S6) — secondary evidence only.

The full commit context menu was not available in official text; items below are exactly those the sources name.
Sublime Merge performs each as a separate one-shot rebase; it does not expose a todo editor.

| # | Sublime Merge item (source) | What it does | `git rebase -i` mapping |
| --- | --- | --- | --- |
| 1 | Edit Commit → Edit Commit Message… (S1, S2) | New message for any commit | `reword <c>` + stored message (or `git commit --amend` for HEAD) |
| 2 | Edit Commit → Edit Commit Contents… (S1, S3 b1092) | "Commit gets reversed" into the working area; user re-commits, then Continue/Abort rebase; also used to split a commit (S4) | `edit <c>`, then at the stop `git reset --soft HEAD~` (S1's "reversed") so its changes are staged; user commits one or more times; `--continue`. Splitting = several commits during the stop |
| 3 | Amend Previous Commit (--amend), commit button dropdown (S1) | Fold staged changes into HEAD | Not a rebase: `git commit --amend` (existing commit flow) |
| 4 | Edit Commit → Squash Selected Commits (S2, S3 b1084) | Combine selected commits into one new commit | Move selected commits adjacent to the oldest one, `pick` first + `squash` the rest, stored combined message |
| 5 | Squash with parent (`squash_commit`, S6 only) | Meld commit into its parent | `pick <parent>` / `squash <c>` + stored message |
| 6 | Edit Commit → Fixup Commits (S3 b1092, `fixup_commits` S6) | Meld into previous, discarding message | `fixup <c>` (target keeps its message; no editor — verified here) |
| 7 | Edit Commit → Drop Commit / Drop Selected Commits (S2, S3 b1079/b1092) | Remove commits; may conflict | `drop <c>` (explicit line, never an omitted line — see §2.5) |
| 8 | Edit Commit → Move Commit Up / Down (S3 b1079, `move_commit` S6) | Swap with neighbour | reorder two `pick` lines |
| 9 | Rebase Commit / rebase onto, available for all commits (S3 b1116) | Rebase current branch onto a commit | Existing non-interactive `git.rebase` (ADR 0023) or `rebase -i --onto`; new interactive plan may start from it |
| 10 | Cherry Pick (multiple) (S3 b2059) | Apply commits onto HEAD | Out of scope for rebase: `git cherry-pick`. Not built; separate history action |
| 11 | Revert (multiple, incl. merge commits) (S3 b2059, b1116) | New inverse commits | Out of scope for rebase: `git revert [-m]`. Not built |
| 12 | Continue rebase / Abort rebase (S1, S2) | Resume or cancel after stop/conflict | `rebase --continue` / `--abort` (ADR 0023 already has these) |
| 13 | Soft reset (user's workflow; not quoted from SM docs) | Manual squash | Already built (ADR 0021), not a rebase |

Git features "any real Git client" also offers, not named by SM's docs:

| Item | Mapping | Note |
| --- | --- | --- |
| fixup -C / -c (use later commit's message) | `fixup -C <c>` (no editor), `fixup -c <c>` (editor → stored message) | verified `-C` |
| Autosquash of `fixup!`/`squash!`/`amend!` commits | `--autosquash` produces the todo; the server can instead compute it and pin it | SM issue [#1844](https://github.com/sublimehq/sublime_merge/issues/1844) requests it — suggests SM lacks it |
| Pause without a commit | `break` | verified |
| Run a command after a commit | `exec <cmd>` | open question Q1 |
| Keep dependent branches in a stack | `--update-refs` → `update-ref refs/heads/<b>` lines | verified; open question Q3 |
| Preserve merge topology | `--rebase-merges` → `label`/`reset`/`merge` | open question Q2 |

## 2. Driving `git rebase -i` without a human editor (Git 2.55.0)

### 2.1 Editors

- Precedence (documented, git-rebase / git-var): `GIT_SEQUENCE_EDITOR` > `sequence.editor` > the commit
  editor (`GIT_EDITOR` > `core.editor` > `VISUAL`/`EDITOR`). Editors are run through the shell with the file path appended.
- **Sequence editor helper** (verified, probe 01): a server-owned executable replaces `.git/rebase-merge/git-rebase-todo`
  wholesale with the pinned todo. Git's own proposal is available to the helper first, which lets the
  server check that Git's generated range equals the commits it pinned (and fail otherwise).
- **Message helper** (verified, probe 01): `GIT_EDITOR` pointing at a helper that writes a stored message to the file argument.
  Invoked for `reword` (file `COMMIT_EDITMSG`, current message plus comment help) and for the **last** `squash` of a
  chain (file starts `# This is a combination of N commits.`). Not invoked for `fixup` or `fixup -C`; documented
  to be invoked for `fixup -c`. Messages should be keyed by todo position/commit, not popped in order as the
  probe does; the helper cannot easily tell which step it is except via `.git/rebase-merge/done` (last line) —
  recommended: the helper reads the last line of `done` and looks up that step's stored message; a missing message
  makes the helper exit non-zero, which fails the step and leaves the rebase stopped (never an empty/default message).
- The existing write path (`internal/server/git_write.go:982-993`) sets `GIT_EDITOR=:`, `GIT_SEQUENCE_EDITOR=:`
  and `-c core.editor=: -c sequence.editor=:`. For rebase -i, the env vars must be replaced with the helper paths;
  env wins over `-c`, so both must be set consistently.
- Helper authentication: pass a per-run token/nonce and state path in env; the helper refuses to run without it,
  so a stray editor invocation (e.g. from an `exec` line or hook calling `git commit`) cannot consume stored messages.

### 2.2 Stops and their detection (verified, probes 02 and 03)

State dir: `git rev-parse --git-path rebase-merge`. Observed files:

| State | `stopped-sha` | `amend` | `REBASE_HEAD` | unmerged index entries | last line of `done` |
| --- | --- | --- | --- | --- | --- |
| `edit` stop | present (=commit) | **present** (=HEAD) | present | none | `edit <sha>` |
| conflict stop (pick) | present | absent | present | yes (`git diff --diff-filter=U`) | `pick <sha>` |
| `break` stop | absent | absent | **absent** | none | `break` |
| failed `exec` (documented) | absent | absent | absent | none | `exec …` |
| message helper failure (reword) | — not probed; expected: HEAD has the picked commit, rebase stopped, `amend` present | | | | `reword <sha>` |

Rule: stopped ⇔ `rebase-merge/` exists and no git process holds it. Classify by (1) unmerged entries → conflict;
(2) last `done` line command (`edit`, `break`, `exec`, `reword`/`squash`/`fixup -c` without message); (3) `amend`
presence for edit. `git-rebase-todo` holds remaining steps; `done` the executed ones; `head-name`, `onto`,
`orig-head` identify the branch and base. Other files observed: `author-script end git-rebase-todo.backup interactive message msgnum no-reschedule-failed-exec patch rewritten-list` (and `update-refs` with `--update-refs`, documented).
These files are internal to Git, not a documented interface; pin by version test and re-verify on Git upgrades.

Resume (verified): for `edit` the user amends (`git commit --amend`) or soft-resets and re-commits, then `git rebase --continue`.
For `break`, `--continue` only. For conflicts, the existing ADR 0023 resolution/review flow, then `--continue`.
`--continue` at a squash/reword step will invoke the message editor again — keep the helper env on continue.

### 2.3 Abort (verified, probe 03)

`git rebase --abort` after a conflict restored branch `main` and the exact original HEAD oid. A rejected todo (see 2.5)
leaves no changes: HEAD unchanged.

### 2.4 Options and config

| Item | Evidence | Recommendation |
| --- | --- | --- |
| `--autosquash` / `rebase.autoSquash` | documented | Pass `--no-autosquash` and `-c rebase.autoSquash=false`; the server pins the full todo, so autosquash (if offered) is a *plan proposal* computed in the editor |
| `--update-refs` / `rebase.updateRefs` | verified: adds `update-ref refs/heads/stack` after the branch's commit; dropping that commit moved `stack` to its new parent and printed "Updated the following refs with --update-refs" | Always pass explicitly (`--update-refs` or `--no-update-refs`) per Q3 |
| `--rebase-merges` / `rebase.rebaseMerges` | verified todo shape (`label onto`, `reset`, `pick`, `label`, `merge -C <sha> side`); without it the merge is dropped and its side commits linearised | Q2 |
| `exec` | verified: runs arbitrary shell in the worktree (`EXEC-RAN pwd=…/r`) | Q1 |
| `--empty`, `--keep-empty` | documented: `-i` implies `--empty=stop` for commits that *become* empty; commits that *start* empty are kept (verified: `pick … # c-empty # empty` in todo) | Pass `--empty=stop` explicitly; a stop with nothing to commit is classified as "became empty" and the user chooses skip/keep |
| `commit.gpgSign`, `-S` | documented; rebase re-signs when configured, needs pinentry/agent | Probes neutralise to `false`. Q4 |
| Hooks | verified with reword: `pre-rebase HEAD~1`, `prepare-commit-msg`, `commit-msg`, `post-rewrite amend`, `post-rewrite rebase`; `-c core.hooksPath=/dev/null` suppresses all | Q5 |
| `rebase.instructionFormat`, `rebase.abbreviateCommands` | documented; only affect Git's *generated* todo | Override to `%s`/`false` so the helper's range check parses reliably; the pinned todo always uses full command names and full oids |
| `rebase.missingCommitsCheck` | verified: `error` rejects a todo with an omitted commit before anything changes | Set `-c rebase.missingCommitsCheck=error`; server also validates |
| `rebase.autoStash` | documented | `false`; a dirty checkout is rejected before start (as ADR 0023) |
| `rebase.rescheduleFailedExec`, `rebase.forkPoint`, `rebase.backend` | documented | `false`; always pass explicit upstream/`--onto`; `-i` implies merge backend |
| `rerere.enabled` / global config | observed: with only `GIT_CONFIG_NOSYSTEM`, the user's XDG global config still enabled rerere ("Recorded preimage") | Decide per the existing write-path policy; at minimum `-c rerere.enabled=false`, or treat rerere autoresolution as a conflict still needing review |
| `advice.*`, `core.editor` | — | `advice.waitingForEditor=false`; stdout/stderr are not parsed for state |

### 2.5 Validation observed from Git itself (verified, probes 01 and 03)

- `squash` as the first line: `error: cannot 'squash' without a previous commit`; nothing changed.
- Unknown oid: `error: invalid line 1: pick deadbeef`; nothing changed.
- Omitted commit with `missingCommitsCheck=error`: rejected; nothing changed.
- In all three cases Git leaves `rebase-merge/` with a stale todo ("fix with --edit-todo"); the server must
  `git rebase --abort` (verified to restore) and should validate *before* starting so this path is only a backstop.

### 2.6 `git fetch --porcelain` (for the parallel fetch/prune slice)

- Introduced in **Git 2.41.0** (documented: [RelNotes 2.41.0](https://raw.githubusercontent.com/git/git/master/Documentation/RelNotes/2.41.0.adoc), "git fetch learned the --porcelain option").
- Format (documented, [git-fetch OUTPUT](https://git-scm.com/docs/git-fetch)): stdout lines `<flag> <old-oid> <new-oid> <local-ref>`;
  flags: space fast-forward, `+` forced, `-` pruned, `t` tag update, `*` new ref, `!` rejected/failed, `=` up to date.
  Incompatible with `--recurse-submodules=yes|on-demand`; overrides `fetch.output`.
- Verified (probe 05) with `git fetch --all --prune --porcelain` over two remotes: pruned ref as
  `- <old> 0000…0000 refs/remotes/origin/gone`, new ref as `* 0000…0000 <new> refs/remotes/origin/newbr`,
  fast-forwards with a leading space; exit 0, **stderr empty**; a no-op rerun prints nothing. `--dry-run`
  prints the same lines without applying. Lines carry no remote name column — the remote is derived from the ref prefix.
  Parse by splitting on single spaces from a fixed-width 1-char flag (the flag may itself be a space). Not verified: `+`, `!`, `t`.

## 3. Probes

Directory: [`go-feasibility-probes/interactive-rebase/`](go-feasibility-probes/interactive-rebase/). Each script
creates a `mktemp -d` repo with an isolated `HOME`/`XDG_CONFIG_HOME`, and removes it on exit. Run `sh 0N-*.sh`.

| Script | Shows | Result (Git 2.55.0) |
| --- | --- | --- |
| `lib.sh` | neutralising `-c` set, sequence and message helpers, state dump | — |
| `01-reorder-squash-reword.sh` | `pick a; reword d; squash b; pick c; fixup -C e` | history `a`, `d+b squashed`, `e` (c+e with e's message); editor called twice (reword, squash); omitted-commit todo rejected, history unchanged |
| `02-edit-stop-resume.sh` | `edit b; break; pick c` | state table in §2.2; amend added a line to `b.txt`; final `c`,`b`,`a` with amended b |
| `03-conflict-abort.sh` | reorder conflict, abort, invalid todos | conflict state per §2.2; abort restored `main` and HEAD; invalid todos rejected |
| `04-update-refs-exec-merges.sh` | update-refs, exec, hooks, rebase-merges todo | as §2.4 |
| `05-fetch-porcelain.sh` | fetch porcelain | as §2.6 |

## 4. Recommendation

**Todo subset** (manual and agent plans share one plan model): `pick`, `reword` (stored message),
`edit` (stop for contents: amend or split), `squash` (stored combined message), `fixup`, `fixup -C`, `fixup -c`
(stored message), `drop` (explicit), `break`, and reordering. `update-ref`, `exec`, `label`/`reset`/`merge` only per Q1–Q3.
Sublime Merge's one-shot menu items (1, 2, 4–8) become presets that open the plan editor pre-filled (or run directly
after confirmation), so parity does not need a second code path. Cherry-pick and revert (10, 11) are separate history
actions, recommended as a later slice (Q6).

**Validation before start** (server, against a pinned base and branch oid):

1. The range is `<base>..<branch>` with the branch oid pinned at plan time; start fails if the branch moved (revision guard).
2. Every commit in the range appears exactly once (pick/reword/edit/squash/fixup/drop); no commit outside the range; full oids.
3. `squash`/`fixup` must follow a line that produces a commit (not first, not after `drop`/`break`-only prefix).
4. Every `reword`, `fixup -c` and the final `squash` of each chain has a non-empty stored message; `fixup -C` needs none.
5. Merge commits in range → rejected unless Q2 allows `--rebase-merges`.
6. Clean checkout; no other operation in progress; checkout writer lock held (ADR 0023 path).
7. The sequence helper compares Git's generated commit list with the pinned range before replacing it; mismatch → exit non-zero (Git aborts cleanly).

**Stop classification**: conflict (unmerged entries) → existing resolution flow; `edit` (`amend` file, last done `edit`)
→ "Editing commit <sha>" with amend/split/continue; `break` (last done `break`, no `stopped-sha`) → "Paused";
became-empty (nothing staged, no conflict, `stopped-sha`) → skip or keep-empty; message-helper failure → "Message missing",
supply and continue; unknown → show raw state and offer Abort only.

**Invocation sketch**: `git -c core.editor=false -c sequence.editor= -c rebase.autoSquash=false -c rebase.autoStash=false
-c rebase.updateRefs=false -c rebase.missingCommitsCheck=error -c rebase.abbreviateCommands=false -c rebase.instructionFormat=%s
-c rebase.rescheduleFailedExec=false -c rebase.rebaseMerges=false -c rerere.enabled=false rebase -i --no-autosquash --empty=stop
--no-update-refs|--update-refs --onto <onto> <base> <branch>` with `GIT_SEQUENCE_EDITOR`/`GIT_EDITOR` = helper + token env,
plus the existing non-interactive env. Signing and hooks per Q4/Q5.

## 5. Open questions for the user (not decided)

- **Q1 `exec` lines.** Options: (a) not supported; (b) supported only in manual plans with an explicit per-plan confirmation showing each command, never from an agent plan without the same confirmation; (c) allowed like any line. *Recommend (a) now, (b) later if wanted* — exec runs arbitrary shell in the checkout (verified) and would bypass the terminal/process ownership model.
- **Q2 merge commits in range / `--rebase-merges`.** Options: (a) refuse plans whose range contains merges, offering only rebasing the linear part; (b) linearise (Git's default, drops merges — verified) with an explicit warning; (c) `--rebase-merges` with label/reset/merge shown read-only (reorder only within linear segments); (d) full label/reset/merge editing. *Recommend (a)*, then (c) if needed; (b) silently changes topology.
- **Q3 `--update-refs`.** Options: (a) off; (b) on, showing each `update-ref` line and the branches it will move in the plan; (c) per-plan toggle defaulting off. *Recommend (c)*: it force-moves other local branches (verified), which should be visible and chosen.
- **Q4 signing.** Options: (a) honour `commit.gpgSign` (may block on pinentry; fail with a distinct "signing failed" state, as for push); (b) force unsigned (`commit.gpgSign=false`) with a notice; (c) refuse to start when signing is configured. *Recommend (a)* with non-interactive failure detection, consistent with `pushSigningFailure` in `git_remote.go`.
- **Q5 hooks.** Options: (a) run repository hooks (pre-rebase can veto; commit-msg can reject a stored message → stop); (b) disable via `core.hooksPath=/dev/null`. *Recommend (a)*, consistent with the user's repository policy, reporting hook rejection as its own state.
- **Q6 cherry-pick and revert** (SM items 10–11) are history actions outside rebase. Options: include in this run; defer to a later slice. *Recommend defer*; confirm since the user asked for "everything Sublime Merge does".
- **Q7 Edit Commit Contents semantics.** Options: (a) SM style — stop with the commit soft-reset into the index (enables split); (b) plain `edit` stop (commit applied, user amends). *Recommend offering both* in the stop UI, defaulting to (a) for SM parity.
- **Q8 rerere.** Options: disable during app rebases; allow but still require review of autoresolved files. *Recommend disable* unless the user relies on rerere.
