# HTTP shutdown deadline regression — 2026-09-22

The user reported `server stop` failing with `server shutdown failed: context
deadline exceeded`, wrapped by a misleading “not reachable” CLI message.
Read-only inspection found a failed shutdown outcome, no discovery file and no
process holding files in the personal application home. No personal state was
changed and no existing process was killed.

## Reproduction and cause

An isolated regression opens an authenticated command request with
`Expect: 100-continue`, waits until the server starts reading its body, and then
leaves that body unfinished. Before the fix, `lifecycle.Stop` reproduced the
exact recorded error in **5.07 seconds**. The final snapshot was already saved;
the HTTP server's graceful drain expired waiting for the body reader.

Fresh connections and incomplete headers have a related race: both the old
read-header timeout and HTTP shutdown deadline were five seconds. Installed Go
1.27.1 `net/http/server.go:3309–3312` only treats a new connection as idle after
more than five seconds, with additional shutdown polling. `Shutdown` does not
itself cancel active handlers or close hijacked WebSockets.

This establishes reproducible causes of the reported failure class. The old
personal outcome contains no connection or phase information, so it cannot
identify the particular connection involved, nor prove the cause of the older
unreproduced ACP shutdown capture.

## Fix

- Track ordinary HTTP connection states. On shutdown close new/idle connections
  immediately and expire active read deadlines, leaving response writes usable.
- Give requests the server's lifetime context so cancellable background reads
  end when shutdown begins. Existing WebSocket teardown remains explicit.
- Start HTTP draining alongside owned-agent shutdown, then save final state
  and close storage. On a genuine drain timeout, close remaining HTTP sockets
  and retain the failed outcome; do not silently report success.
- Gate client-view writes under the same engine shutdown lock as commands.
  Already-admitted writes complete before final storage close; a late body
  reader cannot overwrite saved drafts even if draining times out. This does
  not claim arbitrary handler goroutines can be forcibly joined.
- Label failures by stage (`drain HTTP requests`, `save final state`, or
  `close storage`). Stop errors no longer receive the generic “not reachable”
  wrapper, while missing-server reporting stays compatible.

No timeout was simply increased. No dependency or wire-schema change was added.

## Validation

Regression coverage includes new connections, partial headers, stalled bodies,
preservation of an accepted response during drain, durable interrupted state,
rejection of late view writes without changing the accepted draft, and accurate
CLI error classification.

On Darwin 27.0.0 arm64 / Go 1.27.1:

- `GOCACHE=/tmp/tui-go-build STATICCHECK_CACHE=/tmp/tui-request-staticcheck make check`
  passed, including formatting, vet, staticcheck, the full race suite and build.
- The isolated `pty_smoke.py` run against the rebuilt `bin/tui-go` passed all
  38 checks, including detach, independent client drafts, server stop,
  restart persistence and explicit Resume. Both provider executable overrides
  were disabled. [Retained report](go-shutdown-pty-2026-09-22.json); private raw
  artifacts remain in `/tmp/tui-shutdown-pty-20260922`.
- Independent review checked connection-state locking and active-response
  preservation, and prompted the late-view-write admission guard. Documentation
  links and diff whitespace checks passed.

No live provider prompt or personal-server restart was performed. The rebuilt
`apps/go/bin/tui-go` supplies the corrected behavior on the next launch.
