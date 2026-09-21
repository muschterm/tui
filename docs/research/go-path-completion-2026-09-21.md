# Go project destinations and file completion — 2026-09-21

Implemented the targeted [project picker](../design/projects.md), app General
[starting folder](../design/settings.md#project-starting-folder) and inline
[`@` completion](../design/activity.md#inline-file-mentions). This extends the
existing client-draft/server-command boundary without changing provider choice,
settings takeover, thread lifecycle or workspace provisioning.

## Behavior and checks

New thread always offers a searchable destination plus Add project. Registration
from that flow continues into the corresponding local draft. Folder input uses
asynchronous prefix completion from the configured server directory (home by
default). Enter/Tab enters a suggested directory; Add this folder registers it.
An incomplete or unmatched prefix cannot register its parent. Browsing never
creates folders or threads. General saves use the observed settings revision;
legacy clients omitting the new field preserve it on unrelated settings writes.

`@` keeps typing in the prompt and offers directories/files from the draft project
or existing thread checkout. Arrow keys select; Enter/Tab completes; Escape
dismisses without Send. Directory completion keeps files before Parent navigation.
Spaces are quoted; chosen files become removable attachments. The server reads
them at Send, outside the engine state lock, then rechecks the destination before
durable acceptance. Duplicate commands reuse the accepted capture. Errors identify
the source and stay beside the retained draft, with details and attachment removal.

Executed from `apps/go`:

- `GOCACHE=/tmp/tui-go-build make check` passed formatting, vet, all race tests and
  the executable build. The initial sandbox run could not bind loopback; the
  permitted run passed. Test servers used temporary application homes.
- Focused tests cover stale/cancelled results, capability fallbacks, app scope and
  revisions, legacy settings preservation, Unicode/spaces, keyboard/mouse paths,
  no accidental Send, narrow hit bounds, capture failures and retained drafts.
  Backend checks cover scan/result bounds, traversal/symlinks, FIFO/binary/large
  rejection, thread-checkout selection, concurrent retry deduplication and recovery.
- `python3 scripts/pty_path_completion.py` passed **23 OS-PTY checks** with an
  isolated home. [Report](go-path-completion-captures/pty-report.json). It covers
  General persistence through test-server restart, New thread → Add project,
  typeahead, directory/file selection, capture timing, later source changes,
  failed-capture draft retention, quoted paths and 47×22 completion/dismissal.
- Python harness syntax and `git diff --check` passed. No user server was stopped,
  no user application data changed, and no Git commit/pull/push was performed.

## Renderer evidence

Sixteen deterministic View captures were generated at 160×30 and 47×22 in both
themes; representative captures were visually reviewed. These are renderer
artifacts, **not native terminal screenshots**:

- [Destination picker](go-path-completion-captures/160x30-lightfalse-path-destination.png)
- [General](go-path-completion-captures/160x30-lightfalse-path-general.png)
- [Compact General](go-path-completion-captures/47x22-lightfalse-path-general.png)
- [Compact mentions, dark](go-path-completion-captures/47x22-lightfalse-path-mention.png)
- [Compact mentions, light](go-path-completion-captures/47x22-lighttrue-path-mention.png)

Reproduce with `TUI_GO_CAPTURE_DIR=/tmp/tui-path-captures GOCACHE=/tmp/tui-go-build
go test ./internal/tui -run TestPathCompletionCapturesAndNarrowHitBounds`, then
`scripts/render-capture.py`. The raster font was JetBrains Mono Nerd Font Mono;
it has no CJK fallback, so Unicode cell/input tests are separate from glyph coverage.
Host: Darwin 27.0.0 arm64, C.UTF-8, working tree based on `efdd17a` plus preserved
uncommitted work. PTY harness: bash, `TERM=xterm-256color`, 160×50 → 47×22.

## Limits and upgrade

Completion matches prefixes in one directory, not a recursive index. Listings
cap at 128 results and 4,096 scanned entries; truncation asks for a narrower prefix.
Hidden names require a dot prefix; `.git` context and checkout escapes are rejected.
File context is regular UTF-8 text, at most 64 KiB per file and eight attachments.
Directory attachments, images, real providers, collaborative editing and embedded
terminals remain outside this slice. Deadlines are cooperative between filesystem
syscalls; a stuck filesystem syscall cannot be forcibly cancelled. Existing project
registration/settings directory validation still uses the serialized command path.

No actual Ghostty, iTerm2, SSH, tmux or phone-device validation was performed.
The binary is rebuilt. New capabilities `path-completion` and
`workspace-file-context` require an explicit user-controlled restart of an older
server, followed by TUI relaunch with the same application home/client identity.
Restart preserves saved state and follows the existing continuation preference;
explicit Resume remains the default. Client relaunch alone cannot upgrade the
already-running backend.
