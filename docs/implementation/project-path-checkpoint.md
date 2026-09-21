# Project destinations and path completion checkpoint

Accepted targeted slice, 2026-09-21. Preserve existing uncommitted changes; no
remote Git operations and no automatic restart of the user's backend.

## Scope

- New thread always opens a searchable destination picker, regardless of filter
  or project count. Its Add project flow registers an existing folder and opens
  or restores the per-client project draft. No authoritative thread before Send.
- Add project offers bounded asynchronous folder typeahead/navigation. App General
  owns revisioned, persisted Project starting folder, defaulting to server home.
  Absolute and `~/` paths remain available; registration never creates a folder.
- Inline `@` completes directory/file prefixes under the draft project or thread
  checkout. Enter/Tab browses/selects; arrows navigate; Escape dismisses without
  sending. Hidden entries require an explicit dot prefix. Cancel superseded work
  and isolate late results from changed text or destinations.
- File selection inserts a relative mention (quoted for spaces) and removable
  attachment. Server capture at Send supports regular UTF-8 files ≤64 KiB and
  eight attachments, rejects root escape and preserves drafts on failure.
  Accepted retry/queue/recovery content remains immutable.
- Capabilities: `path-completion`, `workspace-file-context`. Old servers show
  explicit upgrade guidance. Client rebuild/relaunch plus user-controlled backend
  restart is required; no new provider/editor/terminal integration is implied.

## Validation

Complete: `GOCACHE=/tmp/tui-go-build make check` passed formatting, vet, race
tests and build. Loopback tests used permitted execution and isolated homes.
The new `scripts/pty_path_completion.py` passed 23 OS-PTY checks; representative
dark/light and 47×22 deterministic captures were visually reviewed. Python syntax,
local links and diff whitespace passed. [Evidence and limits](../research/go-path-completion-2026-09-21.md).

The binary is rebuilt; the user needs to restart their older server and relaunch
the client to load the new capabilities. Their server/data were left intact.
Actual Ghostty/iTerm2/phone/SSH/tmux behavior remains unverified. No dependencies,
commits or remote Git operations were added. Existing working-tree changes remain.

## Documentation

Updated [projects](../design/projects.md), [settings](../design/settings.md),
[prompt context](../design/activity.md#inline-file-mentions),
[quality](../design/quality.md) and [Go slice](../design/go-slice.md).
This extends the existing server ownership and capture-at-Send contracts;
no independent architectural decision is introduced by these specifications.
