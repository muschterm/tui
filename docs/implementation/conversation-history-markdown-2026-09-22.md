# Existing-thread history and Markdown — 2026-09-22

The user reported an answered question following all newer messages in an
existing `test-tui-go` thread, and Markdown emphasis appearing as literal
`**word**` in replies.

Read-only inspection of the saved application state confirmed an older
submitted question without an activity marker, with its originating turn still
retained. A later question had a proper marker and subsequent agent activity.
The TUI's older-record fallback appended missing-marker cards after the entire
transcript on every render. This was a presentation defect; no stored history
needs rewriting, and uncertain delivery must remain uncertain.

The recovery policy is to display an unanchored card after its retained
originating turn, with an explicit unknown-position label. If that turn is no
longer retained, show the card before the retained conversation. A recorded
submission marker continues to take precedence. Exact ordering within an older
turn cannot be recovered from a turn identity alone.

Conversation replies previously used only plain-text wrapping. Markdown parsing
uses Goldmark v1.8.5 with a terminal-specific renderer, retaining the original source
for storage and transcript copying. This adds one parser dependency rather than
implementing Markdown syntax locally. Rendering remains a client-only operation.

Assistant messages render emphasis, headings, lists, quotes, inline/fenced code,
links, strike-through, task lists and basic tables. Links show their destination;
HTML stays inert text and images use a text label. Tables wrap as readable rows,
without aligned columns; code has a distinct style but no language-specific
syntax highlighting. User messages and tool output keep their literal text.
Untrusted text is sanitized both before parsing and after entity decoding; only
renderer-generated styling bypasses the ordinary plain-text frame boundary.

Validation:

- Focused question-history and Markdown tests cover placement, delivery labels,
  retained markers, syntax, code literals, widths and terminal-escape filtering.
- The combined conversation regression checks final-frame emphasis, stable cell
  widths, unchanged stored source and question placement before later messages.
- Luna visually reviewed deterministic dark/light renders at 60×32 and 160×50,
  including an earlier-history viewport at the narrow size. These are rendered
  fixtures, not live provider or native-terminal compatibility evidence.
- `make check` passed: formatting, vet, staticcheck, all race tests and build.
  Go/staticcheck caches used writable temporary directories; the suite needed
  sandbox escalation for its isolated loopback test servers.
- Final conversation captures were regenerated after the wrapped-style fix;
  PNGs and compressed ANSI sources are retained in
  [conversation-markdown-captures](../research/conversation-markdown-captures/).

The actual application home was inspected read-only. No question was resubmitted,
no stored timeline was rewritten, and no background server was restarted. The
existing transcript-copy command still lists unanchored legacy cards at the end
of its plain-text export; this change fixes the on-screen conversation placement.
