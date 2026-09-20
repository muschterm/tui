# Question Submit compatibility and feedback — 2026-09-20

The user reported that question Submit appeared unresponsive from an Omarchy
machine over SSH, while other controls remained clickable. The exact terminal,
SSH path and application client were not reproduced. A read-only check of the
local default application's server found the original capability list and untyped
legacy question schemas; neither pending thread required Resume. No live question
was submitted, changed or resumed, and the user's server was not stopped.

Two defects matched the report. The current TUI always sent QuestionAnswers,
whereas the original server contract accepts Answers strings. Separately, both
local validation and server rejection messages appeared only in the status bar,
where the Submit hover label could conceal them. This is a verified compatibility
and feedback defect, not proof that every SSH click issue has the same cause.

Untyped requests now send lossless legacy strings (one selected choice or free
text per question). Explicitly typed requests retain structured responses,
including multiple selections. Encoding is selected before the initial dispatch;
no accepted or uncertain command is silently rewritten or resubmitted. Request
identity/revision and pending command identity remain intact.

Validation, submission progress, server rejection and recovery requirements now
appear above the card's fixed actions. Errors remain visible under hover. They
are scoped to thread/request/revision, not the ordinary composer. Editing an
answer clears validation feedback; unknown delivery preserves explicit Retry,
and Submit cannot implicitly Resume interrupted work. The bounded card retains
its scrollable body and keeps Submit and the ordinary prompt reachable.

Validation on macOS darwin/arm64:

- `GOCACHE=/tmp/tui-go-build make check`: PASS formatting, vet, all race tests and
  native build.
- A mouse-to-HTTP client test uses a strict original command schema, accepts the
  exact legacy choice/Other strings and rejects unsupported new fields. PASS.
- UI tests: validation under Submit hover at 160×50, 80×30 and 48×22; progress,
  rejected and uncertain delivery; revision/thread isolation; no implicit Resume
  or offline dispatch; prompt preservation and paint/hit agreement. PASS.
- `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-submit-pty`: PASS
  29 OS-PTY checks, including visible incomplete-Submit feedback and successful
  explicit structured submission with the preserved ordinary draft.
- [Dark/light/narrow captures](go-submit-captures/README.md) regenerated and visually
  inspected. They are deterministic Go frames, not native SSH screenshots.

The updated binary is built. Relaunch `./bin/tui-go` with the existing application
home and client identity to load this fix. The older running server can continue
serving legacy questions. Confirmation in the user's exact Omarchy/SSH session
remains outstanding; no universal remote-terminal compatibility is claimed.
