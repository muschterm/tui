# Go slice code review and fixes — 2026-09-21

Hardening pass over the first Go slice at `276f685` (`feature/init`), requested
before any further implementation. Three independent read-only reviews covered
the backend (`server`, `storage`, `protocol`, `lifecycle`, `client`, `cmd`), TUI
state/input/effects, and TUI rendering/layout/hit-testing against the
[quality contract](../design/quality.md). Findings marked confirmed were
reproduced with throwaway tests or traced end to end; each was then fixed with a
regression test. Work is uncommitted; no remote Git operations; no user server or
application home was touched. No dependencies were added.

Two behavior choices (R1, R7) were informed by a source inspection of the local
T3 Code checkout; see [T3 reference](t3-code-design.md#narrow-right-panel-maximize-and-request-height--2026-09-21).

## Backend

| # | Defect | Fix | Tests |
| --- | --- | --- | --- |
| B1 | The 4 MiB snapshot bound was checked only at command acceptance; dispatch stored each prompt twice (`Prompt` and a JSON copy in `Detail`), so accepted queues grew to ~7.9 MB and every later command, including interrupt, failed with `capacity`. | `Detail` is a compact summary; `Activity.Prompt` keeps the full capture. Acceptance projects dispatch growth. A command is rejected only when the projected snapshot is over the bound **and** grew. Restart compacts a legacy `Detail` only when byte-identical to its prompt. The Activity inspector renders attachment captures from `Prompt`. | `capacity_test.go`, `TestActivityDetailShowsRetainedCapture` |
| B2 | `.git` exclusion was a case-sensitive lexical match: `.GIT/config` on case-insensitive APFS and in-repo symlinks into `.git` were captured into the durable snapshot and listed by browse. | `EqualFold` lexical check plus a resolved-path `os.SameFile` ancestry check against the root's `.git` for capture, browsed directories and entries; handles absent `.git` and `.git` files. | `git_exclusion_test.go` |
| B3 | One stored view with a malformed known field aborted the `thread.delete` / `project.remove` transaction for every client. | `pruneViews` leaves an unprojectable row untouched and continues; PUT validates known field shapes unconditionally. | `malformed_view_test.go`; rollback tests now inject failure through a SQLite trigger |
| B4 | `project.add` and `settings.update` ran `EvalSymlinks`/`Stat`/`Readdirnames` on a client path while holding the engine mutex. | Resolution runs unlocked with a 1 s bound and at most eight lingering resolvers, then the receipt and state are rechecked under the lock; dedup stays under the lock on the canonical path. | `path_resolution_test.go` |
| B6 | Capture timeout/cancel was reported as `409 storage`. | Reported as an `attachment` failure: capture timed out / cancelled. | `capture_interrupt_test.go` |

## TUI state and input

| # | Defect | Fix |
| --- | --- | --- |
| S1 | The Approval choices menu resolved its target by position at Enter time; a remote resolution made “Allow once” approve a different request. | Request ID and revision are bound into request-scoped menu actions; mismatches are rejected; such menus close when their request changes. |
| S6 | The selected request was a positional index, so a remote resolution redirected live typing to another request. | Selection is identity-based (`RequestID`, index retained as legacy fallback). If the selected request disappears, focus leaves the answer input. Nothing auto-submits. |
| S2 | Reopening another thread during a queued edit switched threads; cancel-edit then overwrote that thread's draft. | Thread switches and draft starts are refused while an edit is open; cancel/save restore into the edit's own thread. |
| S3 | A committed view PUT with a lost acknowledgment and failed probe wedged every later save (and therefore every command) on `stale_view`. | The writer remembers up to three unconfirmed payloads and treats a stored view equal to one (or its deletion projection) as the missing acknowledgment. Foreign writes remain conflicts. |
| S8 | A failed pre-dispatch view save was treated as uncertain delivery; `busy` could never clear. | A distinct not-sent outcome clears the pending state and keeps the draft. A sent command with no receipt keeps its identity for explicit Retry. |
| S4 | Below the minimum size, F4 → Send submitted the hidden draft. | Submitting paths refuse while the composer is hidden; the too-small Commands menu offers only Detach, Suspend and Theme. |
| S5 | Paste edited the prompt behind an open menu or the too-small notice. | Paste is ignored with a brief notice in those states. |
| S7 | A queue edit ending in whitespace could never leave edit mode. | Receipt comparison uses trimmed text on both sides. |

Tests: `state_review_{requests,commands,persistence}_test.go`.

## Rendering, layout and hit testing

| # | Defect | Fix |
| --- | --- | --- |
| R1 | A maximized right host could be 1–5 rows tall; its tab/close hits landed on the activity strip (clicking “Thinking…” closed a tab). | Surface rows and hits are clipped to the host. While a surface fills the center, the request card is capped near 40% of the body (≤ 12 rows) and the queue uses its one-row form. |
| R2 | `frame.put` was not cell-accurate: an overlay edge cutting a wide character changed the row width and shifted everything to its right off its hit rectangle. | `put` clips to the frame and pads a cut cluster with spaces in that cluster's style; rows are always exactly the terminal width. |
| R3 | Transcript selection was stored in screen coordinates and survived resize, scroll, pane toggles and thread switches. | The selection records its basis and is treated as absent when the basis differs, at paint, release and copy; the highlight is clamped to its region. |
| R4 | Copied text ignored the selection region and included navigation/divider/right-pane cells. | Every copied row is bounded by the region. |
| R5 | Dividers stayed draggable under the below-minimum notice and rewrote stored sizes. | No geometry, hits or resize below the minimum. |
| R6 | Pane resize had no upper bound; a pane could vanish with no divider to recover it. | Pointer and keyboard resize clamp against the effective geometry; drag tracks the pointer. |
| R7 | Opening a surface that did not fit stored `Maximized=true` permanently. | A non-persisted reveal presents the host full width while it cannot fit; the stored preference is never rewritten; widening restores side-by-side. While forced, the maximize control is absent and F7 shows a brief notice; F3 returns to the conversation. Thread switches drop the reveal. |
| R8 | `safe` passed several bidi controls and U+2028/2029 to the terminal. | All `Bidi_Control` characters and line/paragraph separators are dropped; ZWJ/ZWNJ/variation selectors survive. |
| R9 | Newlines in single-row text (titles from multi-line first prompts) produced unstyled gaps and dropped text. | Single-row paths collapse to one line. |
| R11 | The ASCII left-pane control overflowed its slot. | Pane icons use the shared centering helper. |
| R12 | The leading cell of a dismissible summary had no hit; activity row keys could collide. | The cell belongs to the label target; keys use a separator. |

Tests: `render_review_{cells,geometry,text}_test.go`, `shell/render_review_resize_test.go`,
`review_followup_test.go`. A new invariant sweep checks that every hit lies inside
its painted region and that rows match the terminal width across sizes.

## Validation

From `apps/go` on Darwin 27.0.0 arm64, Go 1.27.1:

- `GOCACHE=/tmp/tui-go-build make check`: PASS (gofmt, vet, race tests, build).
  The `tui` package now takes about 38 s under `-race` because of the new sweeps.
- `staticcheck` 0.8.1 and `govulncheck` 1.8.0 (run without changing `go.mod`):
  only style notes and one unused function (`navigationRows`); no vulnerabilities.
- Isolated-home OS-PTY harnesses, bash, `TERM=xterm-256color`: `pty_smoke` 38,
  `pty_navigation` 28, `pty_small_screen` 42, `pty_sidebar_settings` 27,
  `pty_steering` 11, `pty_path_completion` 23 checks, and `pty_colors`: all PASS.
- `pty_navigation.py` and `pty_sidebar_settings.py` **already failed on
  unmodified `276f685`**: the 2026-09-21 folder-typeahead/destination-picker
  slice changed Add project and New thread without updating them. They now
  follow the current flow. `make check` does not run the PTY harnesses, so this
  kind of drift is invisible to it.

Not run: native Ghostty/iTerm2, SSH, tmux or phone-device checks; a hung-mount
reproduction for B4; SQLite I/O fault injection.

## Left open

- Plausible, unfixed: a 5 s HTTP drain timeout records `Success:false` for an
  otherwise durable stop; divider/scroll/selection drag continues after a lost
  mouse release; Enter is swallowed while an `@` mention has no entries or is
  loading; a stored view that fails to decode on load is used partially and then
  overwritten without a recovery export; Stop/Resume and project rows in open
  menus resolve their target at activation.
- B3 trade-off: a pre-existing malformed view is skipped during deletion, so it
  can retain a deleted thread's draft text. New malformed views are rejected at
  PUT. `PruneThreadView` deliberately fails rather than discard drafts when a
  known field is malformed; a best-effort partial projection is a design choice.
- B2's `.git` check is path-based after open; a concurrent symlink swap remains a
  narrow window. `os.Root` confinement to the checkout is unaffected.
- An eight-row prompt while a surface fills the center can still leave the
  surface 1–2 rows at 22 rows tall; clipping keeps it correct but there is no
  minimum surface height.
- Coverage gaps: `internal/client` 0%, `lifecycle` 13%, `run.go` `Run` and
  `exportRecovery`, `Model.save`; these paths are exercised only by the PTY
  harnesses.
