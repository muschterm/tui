# Go notification and question polish — 2026-09-20

The user's live review settles compact notification/activity controls and a
contained question flow. The accepted behavior is in
[questions](../design/questions.md), [layout](../design/layout.md) and
[the interview record](../design/interview.md#interactive-polish-notifications-activity-and-questions).

## Implemented

- Attention is a neutral bell at zero, or a yellow/orange bell with a count badge
  when requests/failures need attention. It packs beside the visible pane controls;
  a hidden maximize control no longer reserves space. Recents is a section heading.
- Agents stays simply Agents. Hover/focus help retains the state and working count.
  Completed Agents and Plan replace the leading green circle with X on hover/focus;
  only that icon dismisses. Their labels still open history. Running summaries
  cannot be dismissed; existing per-client/thread restoration is preserved.
- Questions use an outlined card with top tabs and answered markers. Back/Next
  appear only for real adjacent questions. A request selector appears only for
  multiple pending requests. Choice/tab overflow controls appear only as needed.
- Vertical single-choice radios advance to the next question without submitting.
  Checkboxes remain on the page, permit multiple selections, and support Other
  text where supplied. Other focuses its input before any advance. Open-ended and
  optional answers are supported. Submit remains explicit for the whole request.
- The card is capped at 12 rows, including borders/navigation/actions. Its body
  scrolls and the text field grows to three rows before scrolling; short windows
  reduce the body first while retaining the composer. Arrow focus reveals choices
  outside the viewport. Native Space selects/toggles without typing into the prompt.

## Contract and preservation

The fixture protocol adds question kind, short tab label, optional required flag,
Other support, and structured answers containing choices plus text. Shared
validation rejects invalid shapes, unknown/duplicate choices, unsupported Other
text and required blanks, with a combined 4096-byte per-answer limit. A checked
but empty Other field is a local incomplete draft. Legacy string questions/answers
and approvals remain readable; absent new command fields use omitempty so stored
legacy retry identities retain their original encoding. No dependency or protocol
version changed; this is a backward-compatible extension on the updated server.
An old running binary must be restarted before accepting structured answers.

Draft keys include request identity, revision and question schema. Older drafts
remain in client storage when a request changes; they are not silently copied into
its new answer. Legacy text drafts migrate once at load. An interactive picker
for previous revisions is still outstanding. Existing named-view size limits
continue to apply. Selection/navigation never writes an authoritative answer;
submission retains request revision checks, command deduplication and explicit
Resume gating.

The fixture seeds a radio question, checkbox question with Other, and optional
text question. Existing saved requests keep their original shapes. Real provider
forms, asynchronous answer delivery and source capability negotiation remain
integration work. No provider execution, clipboard intake or graphics protocol
support is implied here.

Nerd Fonts v3.4.0's verified fa-circle_o, fa-dot_circle_o, fa-square_o and
fa-square_check provide the choice controls, with ( )/(*) and [ ]/[x] fallbacks.
The plain Unicode checkbox glyphs failed the installed capture font's visual
check and were replaced. Native Space exposed Ultraviolet's named key string
space in the pinned source; control routing now handles it. No font is bundled.

## Validation

Executed on macOS darwin/arm64 with pinned Go 1.27.1:

| Check | Result |
| --- | --- |
| `GOCACHE=/tmp/tui-go-build make check` | PASS: formatting, vet, all race tests and native build |
| UI regression tests | PASS: bell states/packing, icon-slot dismissal, radio advance, checkbox/Other drafts, required/optional answers, explicit structured Submit, tabs/overflow, revision/schema isolation, narrow geometry, independent scrolling and measurement/paint agreement |
| Native input regression | PASS: arrows reveal offscreen choices; Space selects/deselects; full-row pointer activation preserves transcript position |
| Protocol/server tests | PASS: structured response persistence, byte bounds, malformed answers, legacy encoding, deduplication, competing/stale response rejection and Resume gating |
| `python3 scripts/pty_smoke.py --artifacts /private/tmp/tui-question-pty-check-4` | PASS: 28 native PTY checks, including radio auto-advance, pointer/Space checkbox toggles, top-tab navigation, explicit Submit and preserved prompt draft |
| `python3 scripts/scroll_benchmark.py --events 500 --output /private/tmp/tui-question-scroll` | PASS: 62.5 ms to alternate-screen exit; 150.8 ms total TUI-child CPU; theme saved and server survived detach |
| Visual review | Dark/light radio, checkbox/Other and 48×22 layouts; [capture provenance](go-question-captures/README.md) |

PTY checks used isolated temporary application homes and servers. The existing
user server/home was not stopped or altered. Timing is one local sample, not a
latency guarantee. Native Ghostty/iTerm2, SSH/tmux and provider integrations were
not exercised in this review.

## Run

Rebuild and restart the background server before using structured answers:

```sh
cd /Users/muschterm/Developer/git/github.com/muschterm/tui/apps/go
make build
./bin/tui-go server stop
./bin/tui-go --client desk
```

Explicitly Resume unfinished saved work. To see all new mixed-question fixtures
without changing existing sessions, use a fresh home in a separate shell:

```sh
export TUI_GO_HOME="$(mktemp -d /tmp/tui-go-questions.XXXXXX)"
./bin/tui-go --client review
# After exiting this demo, stop its background server from the same shell:
./bin/tui-go server stop
```

The next requested UI slice remains clipboard attachments, read-only previews,
and brief unsupported-feature notices; real ACP integration is still outstanding.
