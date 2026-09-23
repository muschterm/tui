# Question card refinement — 2026-09-23

Implements the [2026-09-23 question-card refinement](../design/questions.md#question-card-refinement--2026-09-23)
in the Go reference TUI (`apps/go/internal/tui`). Deterministic captures are in
[go-question-captures-2026-09-23](../research/go-question-captures-2026-09-23/);
they are renderer output, not terminal screenshots.

## What changed

Presentation:

- `question_card.go`: one `requestLayout` measurement now serves both the footer
  height (`requestHeight`) and painting/hit testing (`renderRequest`). The header
  is the first interior row (icon, agent, mode) and an unfilled text control for
  request details; the top border is an unbroken rounded run. Option rows keep a
  gutter cell for the focus mark, then glyph, gap and hanging-indent label;
  hover/focus fill and the hit target cover only the glyph-to-label extent. The
  answer field is indented under Other's label (or the question text) and shares
  the body scrollbar column. The notice sits beside Submit when it fits and on
  its own row otherwise. Submit renders disabled (muted, no hover fill, not
  primary) while Resume is needed, the client is disconnected or an answer is
  in flight, with the reason from `requestCardNotice`. `maxQuestionCardRows` is
  13.
- `question_tabs.go`: `questionTabPlan` measures the tab row once (also used for
  the header's `n of N` overflow counter). Each tab reserves a trailing marker
  cell and uses the `check` icon, so answering does not move its label.
- `icons.go`: `question` (cod-question, `?`) and `approval` (cod-shield, `!`)
  header glyphs.
- `question_history.go`, `render.go`: answered cards span the prompt outline's
  extent (`contentLine.outset`); the unused `rightAligned` field is removed.
- `request_feedback.go`: removed `requestNoticeRows`, orphaned by the layout.

Handling:

- A: `selectQuestion` keeps focus on Back/Next/tab/header/Requests; when that
  control disappears (an end arrow), focus moves to the active tab. Focus is
  read before re-measuring, which otherwise dropped the vanished key to the
  prompt. Choices, the answer field and validation still move to the new
  question's input.
- B: Ctrl+S with focus anywhere in the card runs the validated `answer-submit`
  path (`submitFocusedRequest`); approval cards ask for an explicit choice.
  Answered-history toggles (`question-history:*`) are no longer treated as card
  controls.
- C: `answerFieldKey` — Enter is Next or validated Submit, Shift+Enter/Ctrl+J
  insert newlines, Esc returns to Other's row or the tab/header, and Up/Down at
  the field's first/last visual row leave it.
- D/E: `questionCardKey` — digits 1–9 choose from option rows, tabs and the
  header (never text fields or the composer); Other focuses its field; radio
  digits advance like clicks, and a tab-focused selection follows the active
  tab. Up from the first option reaches the tab (or header); Down past the last
  option reaches the answer field, then Submit.
- F: `questionIndex`/`activeQuestion` clamp every question lookup
  (`questionLines`, `questionInputRows`, `focusQuestionOption`, drafts, the
  Options… menu and the `question` action).
- G: `questionDraftKey` is `requestID#sha256(questions)`. A revision-only bump
  keeps the draft; the revision guards submission only. Drafts stored under the
  former revision-bound key migrate once at load for the loaded revision.
- H: `pruneQuestionDrafts` runs at load and after each snapshot reconcile,
  dropping drafts whose request is no longer live, pending or submitted (and unmigrated legacy
  keys). A changed schema of a still-pending request keeps the prior draft.
- I: `questionOptions` is the single option enumeration for rows, focus
  traversal, digits, Up/Down and the Options… menu.

Tests: `question_card_test.go` adds repeated Enter on Next/Back/tab, Ctrl+S from
five card focus targets, answer-field Enter/Shift+Enter/Ctrl+J/Esc, digits
including Other and composer isolation, Up/Down reach, stale index, header inside
an intact outline with its focus mark at `x+1`, unshifted answered tab label,
bounded selected-row fill and hit extent, disabled Submit under hover in three
states, draft survival across a revision bump plus legacy migration and pruning,
and F6 into and out of the answer field. Existing tests were updated where the
design changed (tab row now at `y+2`, answered label text, 13-row bound, notice
placement, answered-card extent, revision-only drafts, pruning after
resolution). Three unrelated layout tests needed one more terminal row because
the interior header costs a row: activity summaries (120×44), wide-text overlays
(27 rows) and divider drag (120×41). `TestQuestionReviewCaptures` gained
hover/focus, narrow Other, disabled Submit, plain-symbol header focus and
open-ended scenarios.

PTY harnesses: `pty_smoke.py` and `pty_small_screen.py` clicked Next and then
typed, relying on the old focus jump into the answer field. They now press Down
after Next, which is the new keyboard path into an open-ended field.

## Validation

Run in `apps/go` on macOS (Darwin 27.0.0), Go 1.27.1:

- `make check` (fmt-check, vet, staticcheck, race tests, build): pass.
- `PTY_ARTIFACTS=<scratch> make pty`: all harnesses pass (smoke, navigation,
  small screen, sidebar settings, steering, path completion, colors).
- `TUI_GO_CAPTURE_DIR=<scratch> go test ./internal/tui/ -run 'TestQuestionReviewCaptures|TestConversationRegressionPlacesLegacyQuestion'`
  then `scripts/render-capture.py --font ~/Library/Fonts/JetBrainsMonoNerdFont-Regular.ttf`;
  PNG and `.ansi.gz` outputs copied to the capture directory. Reviewed: wide
  multi-choice with Other text, hover plus keyboard focus, disabled Submit under
  Resume, plain-symbol header focus, open-ended, light error, narrow Other and
  narrow validation, and the 60×32 answered-history card.

The PTY runs exercise a local terminal emulator only; no SSH, tmux or other
terminal was checked for this change.

## Remaining gaps

- Decline/Cancel actions and structured option descriptions (Claude and Codex)
  were added later the same day; see
  [question actions](question-actions-2026-09-23.md). Codex questions still
  offer no Decline/Cancel because its response contract has none.
- At very short heights the interior header costs one transcript row compared
  with the previous card; the content viewport still shrinks first.
- Up/Down in the answer field use logical/soft-wrapped row position; a field
  that is scrolled internally still leaves on its first/last row only.
- The `question`/`approval` header glyphs are chosen by codepoint from the
  bundled Nerd Font; they were checked for presence in JetBrainsMono Nerd Font,
  not against another font's coverage.
