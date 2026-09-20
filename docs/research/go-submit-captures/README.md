# Question Submit feedback captures

Actual Go View output rasterized in JetBrainsMono Nerd Font Mono, 15 px in
10×20 px cells; these are synthetic fixtures, not screenshots from the user's
SSH session. The mouse hover remains on Submit. ANSI (compressed), SVG and PNG
are retained; no font is bundled.

- [Required-answer feedback](160x50-lightfalse-question-invalid-submit.png)
- [Server rejection, light theme](160x50-lighttrue-question-light-error.png)
- [48×22 validation feedback](48x22-lightfalse-question-narrow-error.png)

From apps/go, generate with `TUI_GO_CAPTURE_DIR=/tmp/tui-submit-captures go test
./internal/tui -run TestQuestionReviewCaptures -count=1`, then use
`scripts/render-capture.py` and the locally installed font as in previous captures.
[PTY report](pty-report.json) records the 29 native interaction checks.
