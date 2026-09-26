# Go partial staging: hunk and line stage, unstage and discard

Date: **2026-09-26**. Headless feasibility evidence for how the Go server could implement hunk- and line-level stage/unstage (and, if the user approves it, hunk/line discard of worktree changes), which [ADR 0020](../adr/0020-git-write-actions.md#deferred) defers. This is not application code and changes nothing in `apps/go`. The recommendation at the end is a recommendation; the user decides.

Environment: linux/amd64 (Omarchy), **Git 2.55.0** (`git --version`; Arch package `2.55.0-1`). Upstream behavior is cited from the man pages installed with that version (`man git-apply`, `git-update-index`, `git-hash-object`, `git-diff`, `githooks`); the online copies are at [git-apply](https://git-scm.com/docs/git-apply), [git-update-index](https://git-scm.com/docs/git-update-index), [git-hash-object](https://git-scm.com/docs/git-hash-object), [git-diff](https://git-scm.com/docs/git-diff) and [githooks](https://git-scm.com/docs/githooks) (latest, not pinned to 2.55.0).

Probe: [`go-feasibility-probes/partial-staging/probe.sh`](go-feasibility-probes/partial-staging/probe.sh) creates throwaway repositories with logging hooks and runs each scenario; its captured output is [`probe-output-2026-09-26.txt`](go-feasibility-probes/partial-staging/probe-output-2026-09-26.txt). Case IDs below (A1…, B1…) refer to its `== ` blocks. The probe builds selected patches with `awk` and target blobs with `sed`; it exercises Git's plumbing, not a production selection algorithm.

## Approaches compared

- **A (patch):** recompute the diff, keep the selected hunks/lines, and apply with `git apply --cached` (stage), `git apply --cached -R` (unstage) or `git apply -R` (discard, worktree).
- **B (computed blob):** read the current index blob (stage/discard) or the HEAD and index blobs (unstage), apply the selection in memory in Git's normalized ("index") representation, write it with `git hash-object -w --no-filters --stdin`, and install it with `git update-index --cacheinfo <mode>,<oid>,<path>`. The resulting OID is known before Git runs, like the existing `prepareStage` expectation (`apps/go/internal/server/git_write.go`, `prepareStage`).

## Findings

| Case | Observation (Git 2.55.0) | A | B |
| --- | --- | --- | --- |
| A1/B1 whole hunk | `apply --cached` of one hunk and `update-index --cacheinfo` of the predicted blob give the same index entry; B's OID `c55780c…` matched the prediction exactly | works | works, OID predicted |
| A2 line selection | The usual transform (unselected `-` → context, unselected `+` dropped) applied **with exit 0 but reordered lines** (`l3 l5 L4 l6`) when a block is `-l4 -l5 +L4 +L5` and only `+L4` is selected. Moving converted context after the block's kept `+` lines gave `l3 L4 l5 l6`. Hunk counts happened to stay valid, so `--recount` did not matter here | silent misordering unless the builder handles `-`/`+` pairing; Git does not detect it | same ordering decision, but made explicitly in server code over line arrays and testable without patch syntax |
| A3 staleness | With default context, a neighbouring change staged by someone else makes `apply --cached --check` fail (`patch does not apply`); `-C1` also failed in this case. With a `-U0` patch and `--unidiff-zero`, a one-line shift was absorbed (offset search on the removed line) | full context gives a content check, but not identity pinning; `-C<n>`/`--unidiff-zero` weaken it (man: "By default no context is ever ignored") | no implicit check: see B1 below; staleness must come from pins |
| B1 no CAS | `update-index --cacheinfo` overwrote an entry that had changed after the blob was computed, exit 0 | — | must recheck the index entry OID immediately before running, as `recheckWorktree`/`recheckConflicts` do today; a small race remains |
| A4 `-3` | `apply --cached -3` on a context mismatch fell back to a 3-way merge and **left stages 1/2/3 in the index** (exit 1, `U f`) | never use `-3` | n/a |
| A5 `core.autocrlf=true` | `git diff` output is normalized (0 CR bytes); `apply --cached` stages LF lines. `apply -R` to the worktree discarded a hunk and kept `\r\n` on the remaining line. `hash-object --path=f` of `a\r\n` equals `--no-filters` of `a\n` | works in both directions | works if built from index-space content and hashed with `--no-filters` |
| A6 `text eol=crlf`; mixed EOL without attributes | Discard via `apply -R` restored `b\r\n` with CRLF kept; a file with mixed `\r\n`/`\n` and no attributes staged byte-exactly (`x\r\n Y\n z\r\n`) | works | works (bytes preserved) |
| A7 clean/smudge filter | `git diff` ran `clean` (worktree side). `apply --cached` ran **no filter**; `hash-object --no-filters` ran none; `hash-object --path` ran `clean` again on already-clean bytes. `apply -R` to the worktree ran `clean` then `smudge` | index side is filter-free; worktree discard runs filters (CLI parity with `git checkout -p`/`git restore -p`, unverified which one Git uses internally) | must use `--no-filters`: `--path` would re-clean index-space bytes, which is wrong for any non-idempotent clean filter |
| A8 LFS-like pointer filter | Diff shows only pointer lines (`-ptr-oid-…`/`+ptr-oid-…`) | partial selection meaningless | same; refuse partial for paths with a `filter` attribute (whole-file stage remains) |
| A9 no trailing newline | `\ No newline at end of file` markers survive line selection; staging `+A` only produced `A\nb\nc` with no final newline | works if the marker stays attached to its line | explicit flag on the last line |
| A10 mode change | Dropping `old mode`/`new mode` headers staged content only, mode kept `100644` | mode is header-only; must be a separate whole-entry action | mode is an explicit `--cacheinfo` field |
| A11/B2 intent-to-add | `git diff` shows a new-file patch; a partial `apply --cached --recount` replaced the i-t-a entry with the partial blob. `--cacheinfo` did the same and cleared the i-t-a flag (`flags: 0`) | works for stage | works for stage |
| A12 unmerged | `git diff` shows a combined `diff --cc`, which `apply` rejects (`No valid patches`). **`update-index --cacheinfo` exited 0 and silently resolved the conflict** (stages 1–3 replaced by stage 0) | refuses by accident | must refuse before running (existing `pathConflicts` check) |
| A13 binary | `Binary files … differ`; `--binary` gives a `GIT binary patch` | refuse partial | refuse partial |
| A14 Latin-1 bytes | Staged byte-exactly (`351`, `357` octets kept) | works | works (treat content as bytes; display decoding is separate) |
| A15 staged rename, unstage | `apply --cached -R` of one hunk from a `-M` diff **also reversed the rename headers**, leaving `MD old`. With `--no-renames`, the added side is a new-file patch and a partial reverse fails (`removal patch leaves file contents`). B wrote the computed blob at `new` and kept `R old -> new` | needs a synthesized modification patch against the current index blob, i.e. B's work plus patch formatting | works directly |
| A16 submodule | Diff is `Subproject commit` lines on a `160000` entry | refuse | refuse |
| A17 adjacent changes | Changes 4 lines apart form one hunk at `-U3`, two at `-U1`; the UI's hunk boundaries depend on the context width the server chose | the server must fix `-U<n>` and apply with the same | independent of display context |
| A18 100 000-line file, 10 000 hunks | Staging half: `apply --cached --unidiff-zero` 0.455 s; `hash-object` + `--cacheinfo` 0.007 s (same resulting blob, `cmp` equal) | acceptable | cheaper; cost is the in-memory selection |
| Hooks | No hook ran for `hash-object -w`. `apply --cached` and `update-index` each ran only **`post-index-change`** (githooks: "invoked when the index is written in read-cache.c do_write_locked_index"); `git add` writes the index too. No `pre-commit`, `pre-applypatch` or `post-checkout` ran. `reference-transaction` lines in the raw output come from probe setup (`commit`, `reset`), not these commands | CLI-parity: same hook as `git add -p` | same |

Other observations:

- The diffs in the probe used `i/`/`w/`/`c/` prefixes although `HOME` was isolated: a user-level `diff.mnemonicPrefix` (via XDG config) still applied, and `git apply` accepted it. Patch text depends on user diff config (`diff.noprefix`, `diff.renames`, `diff.external`, `color.diff`, `diff.algorithm`), so A must pass explicit output flags (`--no-color --no-ext-diff --no-textconv --src-prefix=a/ --dst-prefix=b/ --no-renames -U<n>` or similar) and B's display diff needs the same care only for presentation.
- `git hash-object --stdin` implies `--no-filters` unless `--path` is given (man `git-hash-object`), so B's write is explicit about filters rather than depending on attributes.
- `git apply --index` requires index and worktree to match for the path (man: "--index expects index entries and working tree copies for relevant paths to be identical"), which is never true for a partially staged file; it is not useful here.

## Staleness fingerprint

Neither approach gives a whole-selection check from Git alone: A checks context/removed lines only, B checks nothing (B1). A selection should be refused (`stale_entry`) unless all of the following still match at run time, rechecked under the writer lease immediately before the index write, as the existing pins are:

1. **Index entry:** mode + OID + stage 0 (`ls-files -s`), plus the i-t-a flag. For unstage, also the HEAD tree entry OID (or unborn).
2. **Worktree side** (stage, discard): the existing `lstat` token *and* the clean-filtered OID (`hash-object --path`, as `prepareStage` already computes). The token alone misses same-size same-second rewrites; the OID alone costs a read on every check.
3. **Diff identity:** a hash of the normalized diff bytes the client selected from, generated with pinned flags (context width, algorithm, no renames). Selections name hunk/line indices within that diff, so the diff hash binds indices to content.

With (1)–(3) equal, B's target blob is a pure function of pinned inputs, so the server can also recompute and compare the predicted OID after `update-index`, reporting a mismatch the way `staged_newer_content` does today.

## Limitations

- One Git version on one Linux host; no Windows (`core.autocrlf=input`/`true` on NTFS), macOS or older-Git checks. `core.safecrlf`, `core.eol`, `working-tree-encoding` and `ident`/`$Id$` expansion were not probed.
- Real Git LFS was not installed; A8 uses a pointer-emitting stand-in filter.
- Sparse checkout, split index, `skip-worktree`/`assume-unchanged` entries and `core.fsmonitor` were not probed.
- Unstage of a staged mode change, a staged deletion and a staged symlink retarget were not probed; B handles each only as a whole-entry `--cacheinfo` (or `--force-remove`), which is inferred, not tested.
- Discard was probed only via `apply -R`. B-style discard (compute the new clean content, then produce worktree bytes via `git cat-file --filters --path=<p> <oid>` and write them atomically) was **not** probed; it would re-smudge the whole file, which may change bytes outside the selection for files whose worktree EOLs were not produced by Git (e.g. mixed EOL under `autocrlf`).

## Unverified hypotheses

- `git apply` probably normalizes worktree reads with the clean path and writes with smudge (A5–A7 are consistent with it), which is why `apply -R` discard kept CRLF. Not confirmed from source.
- `git add -p` and `git restore -p` in 2.55.0 likely use the same `apply --cached`/`apply -R` plumbing (A's behavior would then match the CLI exactly). Not confirmed from source.
- `-U0` + `--unidiff-zero` could mis-anchor pure-addition hunks when lines shift (no removed line to match). Not reproduced.

## Recommendation (not a decision)

Use **B for stage and unstage**: compute the target index blob from the pinned index (and HEAD) blob in normalized bytes, write it with `hash-object -w --no-filters --stdin`, install it with `update-index --cacheinfo`, and verify the resulting OID against the prediction. Reasons: an exactly predictable result that extends ADR 0020's verification model (A1/B1, A18); no dependence on patch-text formatting or user diff config; correct partial unstage of renamed and added entries where A fails or silently reverts the rename (A15); line selection is ordinary code over line arrays rather than patch surgery that Git will apply in the wrong order without error (A2). Costs: the server owns a small diff/merge of line arrays and its tests; `--cacheinfo` has no compare-and-swap and resolves conflicts silently, so the index-entry pin and the conflict check must run immediately before it (B1, A12).

For **discard**, if the user approves it, use **A (`git apply -R` to the worktree)** with pinned full context (no `-C`, no `--unidiff-zero`, never `-3`), a patch generated by the server with pinned diff flags, and the ADR 0020 discard gates (`Confirmed`, pin, `beginWorktreeRewrite` buffer coordination). It preserved worktree line endings and ran the repository's clean/smudge filters as the CLI does (A5–A7); B-style discard is unprobed and risks rewriting unselected bytes.

In both cases refuse partial operations (`not_supported`, keeping whole-file actions) for unmerged paths, submodules, binary content, symlinks, paths with a `filter` attribute, and mode-only changes; treat mode changes as a separate whole-entry action. Hooks: only `post-index-change` runs, as for `git add`, which satisfies the CLI-parity policy without extra handling.

## Open questions for the user

1. Is hunk/line **discard** in scope, and with the same `Confirmed` + pin requirement as whole-file discard?
2. Accept B (server-computed blob) for stage/unstage even though `git add -p` itself goes through patch application? Visible behavior matches, but the mechanism differs from the CLI.
3. Refuse partial staging for any path with a `filter` attribute (LFS and custom filters), or only when the clean output is not line text?
4. Should line selection be offered for replacement blocks (`-`/`+` pairs), and if so, is "kept old lines follow the selected new lines of the same block" (A2) the intended ordering?
