---
status: accepted (user decisions 2026-09-26)
---

# Partial staging: hunk and line stage and unstage

[ADR 0020](0020-git-write-actions.md) shipped whole-file stage, unstage and
discard and deferred hunk and line staging. The accepted scope in
[git-client.md](../design/git-client.md#accepted-scope) includes
file/hunk/line staging, and its verification target requires that partial
staging preserve unrelated staged work. On 2026-09-26 the user decided to
ship hunk **and** line stage and unstage together, and to keep partial
**discard** out of scope. The mechanism follows the headless evidence in
[go-partial-staging-2026-09-26.md](../research/go-partial-staging-2026-09-26.md)
(case IDs A1…, B1… below refer to it). This records the Go server, protocol
and client slice; the Go TUI binding is separate.

## Decision

- **Computed index blob, no `git apply`.** The server reads the diff's
  pre-image blob (the index blob for stage, the HEAD blob for unstage),
  applies the selection to its bytes in memory, writes the result with
  `git hash-object -w --no-filters --stdin` and installs it with
  `git update-index --cacheinfo <mode>,<oid>,<path>`, keeping the index
  entry's mode. The resulting object ID is known before Git runs and is
  compared with the index entry afterwards, extending ADR 0020's
  verification. Content is handled as bytes in Git's normalized ("index")
  representation: the worktree is never read for content and never
  written; what is staged is exactly the selected lines of the diff that
  was shown (A1/B1, A5, A14, A18).
- **Unstage is defined on the displayed diff.** The staged diff (HEAD against
  the index) is shown; unstaging a selection sets the index to HEAD's content
  with every change that was *not* selected applied. That reverts exactly
  the selected changes, returns restored HEAD lines to their HEAD position,
  and keeps a staged rename, since only the renamed path's entry changes
  (A15). A staged addition is diffed from the empty blob; unstaging all of
  its lines leaves an empty staged file (removing the entry stays a
  whole-file action).
- **Line ordering inside a change block.** Kept base lines and selected new
  lines of one block (adjacent removals and additions) are ordered by the
  server, not by patch surgery (A2). The selected additions take the place
  of the first selected removal: kept removed-side lines before it stay in
  front of them, kept lines after it follow them. So in `-l4 -l5 +L4 +L5`,
  selecting `-l4 +L4` gives `L4 l5` (the research's A2 fix) and selecting
  `-l5 +L5` gives `l4 L5`. A block with no selected removal keeps its old
  lines and appends the selected additions after them, as `git add -p`
  does. This refines the orchestrator's first 2026-09-26 rule ("kept old
  lines follow the selected new lines of the same block"), which is the
  same for the research case but put an appended line (`+c` after `-b +b`)
  above the kept lines and gave `L5 l4` for the second pair; the refinement
  was accepted after verification (2026-09-26). For unstage the rule applies
  to the unselected changes, which are the ones applied to HEAD. A missing
  final newline belongs to whichever line ends up last. A line that only
  gained or lost its final newline is a removal/addition pair with equal
  text; selecting one side alone is applied literally (unstaging only `-b`
  from `-b` (no newline), `+b`, `+c` leaves `b` twice), so clients should
  select both sides together. No principled automatic pairing was found:
  linking the two sides made such an unstage a no-op.
- **Addressable diff with pinned options.** `GET /v1/git/hunks` returns one
  path's hunks with header ranges, per-line kinds, 1-based line numbers and
  response-wide line indices, plus a fingerprint. The diff is generated
  with fixed options that neutralize user diff configuration (`-U3`,
  `--inter-hunk-context=0`, Myers with the indent heuristic, `--no-renames`,
  `--no-textconv`, `--no-ext-diff`, `--no-color`, `--full-index`, `a/`/`b/`
  prefixes, and `diff.suppressBlankEmpty`, `diff.mnemonicPrefix`,
  `diff.noprefix` and `core.quotePath` pinned), so hunk boundaries do not
  depend on `diff.*` settings (A17, and the probe's `diff.mnemonicPrefix`
  observation). A staged rename is diffed blob-to-blob (HEAD blob of the
  source, index blob of the destination), independent of rename detection.
  The parser refuses anything unexpected, and the server checks end to end
  that applying the whole parsed diff to the pre-image reproduces the
  post-image object ID (for stage, the worktree object ID that Git's own
  `--full-index` diff reports; for unstage, the index blob); a mismatch
  refuses partial staging for that path. The stage post-image comes from
  the diff rather than `hash-object --path`, which does not consult the
  index and so misses Git's rule that keeps CRLF under `core.autocrlf` when
  the index blob already has CR (verification review, 2026-09-26; the
  whole-file stage of ADR 0020 had the same flaw and now predicts its
  result the same way).
- **Staleness fingerprint.** The fingerprint pins the status entry's pin,
  the index record (the `ls-files --stage -v` record: mode, object ID,
  stage, skip-worktree or assume-unchanged tag; plus the intent-to-add bit
  from `ls-files --debug`, since an intent-to-add entry and an empty file
  have identical records), the HEAD tree record (unstage), the path's
  `filter`, `diff` and `working-tree-encoding` attributes, the worktree
  `lstat` token and post-image object ID (stage), and a SHA-256 of the
  exact diff bytes, as length-prefixed fields. Selections
  name indices within that diff, so the diff hash binds indices to content.
  The write recomputes everything under the writer lease before journaling
  and refuses a difference with `stale_diff`, recording nothing. After the
  result object is written, the index record, the HEAD record (unstage) and
  the worktree token (stage) are compared again immediately before
  `update-index`, because `--cacheinfo` has no compare-and-swap (B1); a
  difference ends the command `failed`/`stale_diff` with nothing changed.
  Never partially applied.
- **Refusals.** Partial stage/unstage is refused (`not_supported`; whole-file
  actions remain) for unmerged paths (`conflicted`; `update-index` would
  silently resolve them, A12), submodules (A16), symlinks, binary content
  or `-diff` (A13), any path with a `filter` attribute (A7/A8: LFS pointers
  and non-idempotent clean filters), a `working-tree-encoding` attribute
  (unprobed), deletions and type changes, non-regular worktree nodes,
  renamed intent-to-add files, skip-worktree and assume-unchanged entries
  (`update-index` would drop those bits), mode-only changes (no content
  hunks; A10), diffs over 4 MiB and base blobs over 16 MiB. A selection of
  more than 65 536 hunk and line indices is refused with `too_large`
  (command bodies are capped at 768 KiB; select hunks instead). Content with a
  mode change can be partially staged; the mode stays as it is in the index
  and remains a whole-file action. Intent-to-add entries can be partially
  staged; the entry becomes an ordinary entry holding the selected lines
  (A11). Partial discard is refused (`not_supported`).
- **Same command envelope as ADR 0020.** `git.stage` and `git.unstage`
  carry `GitWrite.Partial` (path, group, fingerprint, hunk and line indices)
  instead of `Paths`, behind capability `git-partial-stage`, which is
  advertised only with Git 2.28 or newer: `--no-relative` first appears in
  v2.28.0's `Documentation/diff-options.txt` (absent in v2.27.0), and the
  other pinned options and plumbing are older. With older Git the endpoint
  and the writes answer `unavailable`. Journal and
  receipt, command-ID deduplication, the writer lease and Git slot,
  `checkout_busy` while a thread holds the checkout, the thread's
  checkout/worktree resolution, lock retries, the budget and the
  resolution-job index decision record are the whole-file path's.
- **Hooks.** CLI parity as in ADR 0020: `hash-object -w` runs no hook and
  `update-index` runs only `post-index-change`, as `git add` and
  `git apply --cached` do (probe "Hooks" row, without `core.fsmonitor`
  configured); no other hook runs. The
  write runs with the write policy; every read and the fingerprint use the
  read policy of `git.go`, as status pins do.

The wire contract, including every code, is documented in
`apps/go/internal/protocol/git_partial.go`; the Go client fetches with
`Client.GitHunks` and builds commands with `client.GitPartialCommand`.

## Consequences

- A partial write costs about ten Git invocations (status, ls-files,
  check-attr, hash-object, diff, cat-file, the checks, then hash-object -w,
  update-index and a verification read). A 20 000-line file with 2 000
  hunks stages in well under a second in the test suite.
- Each partial write leaves one loose blob; unreferenced ones are pruned by
  ordinary Git garbage collection.
- `update-index --cacheinfo` records no stat data, so the next status
  rehashes the file, as after `git apply --cached`.
- The mechanism differs from `git add -p` (which applies a patch); visible
  results match for whole hunks and follow the ordering rule above for
  lines.

## Known limits

- Worktree diffs are not lock-free. `git --no-optional-locks diff` still
  refreshes stat data for stat-dirty but unchanged paths under Git's own
  `index.lock` (`refresh_index_quietly` ignores optional locks; observed
  with `GIT_TRACE2_PERF` on Git 2.55). This affects the stage diff of
  `GET /v1/git/hunks` and of the partial-stage recheck, the whole-file
  stage prediction (`worktreeDiffOid`) and the existing `GET /v1/git/diff`.
  The rewrite changes stat data only, under Git's normal locking, so index
  content is never at risk, but another client's Git command may briefly
  see `index.lock` and refuse. A possible fix is `git diff-files -p`, which
  should not refresh the index; that is unprobed.
- A narrow race remains between the last index check and `update-index`
  taking `index.lock`: a change another process makes in that window is
  overwritten for this path and is not detected; the verification read
  only reports `index_changed` for a change that lands after
  `update-index`. Other paths are never affected. The research recorded
  this window (B1); closing it would need Git to offer a compare-and-swap
  index update.
- Only one Git version (2.55.0) on Linux was probed. Windows line endings on
  NTFS, macOS, older Git, `core.safecrlf`, `ident` expansion, sparse
  checkout, split index and `core.fsmonitor` are untested here.
- Paths with any `filter` attribute are refused even when their clean output
  is ordinary text; the user decision on a narrower rule is open (research
  question 3).
- The stage diff runs under the read policy, which neutralizes
  repository-local filter drivers; such paths are refused by attribute, so
  this never changes what is staged.
- Hunk boundaries use three context lines; selecting inside a larger hunk
  needs line selection. The server does not split hunks.
- Line text crosses JSON as UTF-8 with invalid bytes replaced; selection is
  by index, so staged bytes are exact, but a client cannot show Latin-1
  text faithfully.

## Deferred

Hunk and line **discard** (user decision 2026-09-26; the research
recommends `git apply -R` with pinned full context and the ADR 0020 discard
gates if it is approved), staging a mode change separately from content,
partial staging of untracked files, splitting hunks, multi-path
selection, and editing a hunk before staging it.

## Alternatives considered

- **Patch application (`git apply --cached`, `-R` for unstage).** Matches
  `git add -p`'s plumbing, but line selection inside a replacement block is
  applied in the wrong order without an error (A2); a hunk unstage of a
  staged rename also reverses the rename (A15), or fails with
  `--no-renames`; patch text depends on user diff configuration unless every
  output option is pinned; context checks are weakened by `-C`/
  `--unidiff-zero` (A3) and `-3` leaves conflict stages behind (A4).
  Rejected for stage/unstage; the computed blob has an exactly predictable
  result and puts the ordering decision in tested server code.
- **Strict "kept old lines follow the selected new lines" ordering.** The
  orchestrator's stated rule; kept for the research case, refined as above
  because an appended line moved above unrelated kept lines.
- **Unstage by applying the reversed diff to the index blob.** Equivalent
  line content, but restored HEAD lines were placed after kept index lines
  in mixed blocks. Replaced by applying the unselected changes to HEAD.
- **`hash-object --path` for the result.** Would re-run clean filters on
  already-clean bytes, which is wrong for non-idempotent filters (A7).
- **Pinning only the status entry.** The `lstat` token misses same-size
  same-second rewrites and nothing binds hunk indices to content; the diff
  hash and object IDs are needed.
