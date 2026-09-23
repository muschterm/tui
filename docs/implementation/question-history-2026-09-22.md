# Question answer history — 2026-09-22

The Go slice stored accepted question answers on the server but did not put the
original questions back beside those answers in the conversation. Native ACP
can retain an accepted response without an authoritative receipt, so the card
needs to preserve the user's choice while describing delivery honestly.

At `request.answer` commit, the server now appends one transcript activity
marker linked to the canonical question request. Its ID is stable for the
request revision. The accepted question schema and answer stay on the server
request record; the marker carries only ordering and identity. Idempotent
command retries cannot append a second marker, and the card has no answer,
submit, queue or steer action.

The TUI renders every original question and available choice, marks the accepted
selections, includes accepted free text and Other values, and distinguishes
optional omissions. **Answered** requires a resolved request plus
`deliveryConfirmed`; native ACP responses remain **Submitted** with their
accepted, unconfirmed, uncertain, cancelled or undeliverable status. A compact
preview is limited by wrapped visual rows. Expand/Collapse is local to the
thread view and restores with that view. Copying the transcript uses the same
accepted snapshot and includes all original choices.

If a retained request has no activity marker because it predates this change or
its marker was trimmed by normal activity retention, the TUI places one card
after the last retained activity of its originating turn, or under Earlier
history before the retained conversation if that turn is absent. This follow-up
corrects the original fallback that appended cards after every newer message.
It says its earlier timeline position is unavailable and
does not create a timestamp or infer provider confirmation. The reference
inspection of UCF's `studio/web/src/components/Transcript.tsx` showed the
question card listing selected and unselected choices together; the Go card
follows that presentation with terminal radio/checkbox markers.

Focused validation passed:

- `GOCACHE=/private/tmp/tui-question-history-gocache go test ./internal/server -run 'FixtureQuestionAnswerPersistsOneChronologyMarker|NativeQuestionHistoryPersistsBeforeDeliveryConfirmation' -count=1`
- `GOCACHE=/private/tmp/tui-question-history-gocache go test ./internal/tui -run QuestionHistory -count=1`
- `gofmt` on the changed Go files.

The current native ACP bridge still cannot confirm that the provider resolved
an answer, so its history card stays Submitted. Preserving the user's reading
position through expansion and collapse is still open. A history card recovered
without its marker remains visible, but its exact earlier position is unknown.
