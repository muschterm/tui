# Claude effort selection and model rows — 2026-09-23

Status: verified against the installed Claude Code CLI 2.1.281 on macOS through
its `-p --input-format stream-json` host protocol. Initialization and settings
controls only; no prompt was sent, so no provider request or cost was incurred.

## Problem

The Go Claude bridge offered no effort control because earlier records found
that `apply_flag_settings` acknowledges any `effortLevel` without confirming
the effective state. Model rows also read `Name (resolved-id)`, and each alias
was repeated as a `<resolved-id> (pinned)` row alongside the documented legacy
list, so Opus 5.5 appeared several times under different labels.

## Evidence

The `initialize` control response lists models with per-model effort data:

| `value` | `resolvedModel` | `displayName` | `supportedEffortLevels` |
| --- | --- | --- | --- |
| `default` | `claude-opus-5-5` | Default (recommended) | low, medium, high, xhigh, max |
| `opus` | `claude-opus-5-5` | Opus 5.5 | low, medium, high, xhigh, max |
| `claude-fable-5-1[1m]` | `claude-fable-5-1` | Fable 5.1 | low, medium, high, xhigh, max |
| `sonnet` | `claude-sonnet-5` | Sonnet 5 | low, medium, high, xhigh, max |
| `haiku` | `claude-haiku-4-5-20251001` | Haiku 4.5 | none reported |
| `claude-opus-5`, `claude-fable-5`, `claude-opus-4-8`, `claude-opus-4-7` | same | Opus 5, Fable 5, Opus 4.8, Opus 4.7 | five levels |
| `claude-opus-4-6`, `claude-sonnet-4-6` | same | Opus 4.6, Sonnet 4.6 | low, medium, high, max |

`initialize` carries no effort field on this host. The `get_settings` control
returns `applied.effort`, documented in the CLI's own schema as "the effort
level the session will send on its next request — after env overrides, session
state, org caps and model-support downgrades". A scripted control sequence
observed:

| Control | `applied.model` | `applied.effort` |
| --- | --- | --- |
| startup | `claude-fable-5-1` | `high` |
| `apply_flag_settings {effortLevel: low}` | same | `low` |
| `apply_flag_settings {effortLevel: max}` | same | `max` |
| `apply_flag_settings {effortLevel: bogus}` (acknowledged) | same | `max` (unchanged) |
| `apply_flag_settings {effortLevel: null}` | same | `high` (model default) |
| `set_model haiku` | `claude-haiku-4-5-20251001` | `null` |
| `apply_flag_settings {effortLevel: high}` on Haiku | same | `null` |
| `set_model claude-opus-4-6` | `claude-opus-4-6` | `high` |
| `apply_flag_settings {effortLevel: xhigh}` on Opus 4.6 | same | `high` (downgraded) |

The CLI binary's `/effort` command sends the same
`apply_flag_settings {effortLevel}` control, and its settings schema lists
`effortLevel` values `low`–`xhigh` for persisted settings while the session
control also accepts `max`.

## Catalogue shape varies between launches

Two launches of the same CLI, same flags, same working directory and same
environment, returned different catalogues within the hour. One shape lists
versioned rows (the table above). The other lists only aliases:

| `value` | `displayName` | `description` |
| --- | --- | --- |
| `default` | Default (recommended) | Opus 5.5 with 1M context · Best for everyday, complex tasks |
| `opus[1m]` | Opus (1M context) | Opus 5.5 with 1M context · Best for everyday, complex tasks |
| `claude-fable-5-1[1m]` | Fable | Fable 5.1 · Most capable for your hardest and longest-running tasks |
| `sonnet` | Sonnet | Sonnet 5 · Efficient for routine tasks |
| `haiku` | Haiku | Haiku 4.5 · Fastest for quick answers |

Stripping every `CLAUDE_CODE_*` variable or launching from an empty directory
made no difference, so the cause was not identified; it is likely a remotely
gated flag. The alias shape is what produced the reported "weird" names such as
"Opus (1M context)" and the duplicated legacy rows. Both shapes must be handled.

The local T3 Code checkout (HEAD `1ba471a37`, read-only) does not use the CLI
picker names at all: its bundled `model-manifest.json` names Claude rows by
version ("Claude Opus 5", "Claude Sonnet 5") keyed by canonical slug with
aliases. This project keeps native discovery as the source, so the versioned
identity is taken from Claude's own strings instead of a bundled manifest.

## Bridge behaviour

- Effort is an ACP `thought_level` select named `Effort`, offered only when
  `get_settings` returned an `applied` block at startup. Its values are the
  union of catalogue-reported levels in documented order, each scoped through
  `tui-go.models` to the models (alias and resolved ID) that report it.
  Documented legacy IDs have no catalogue entry and so get no effort control.
- A change requires a selected model that reports the level, sends
  `apply_flag_settings {effortLevel}`, then reads `get_settings`. The change is
  accepted only when `applied.effort` equals the request; otherwise the bridge
  records the applied level, rejects the request naming it, and keeps the
  session usable. A missing readback after a native change retires the session.
- A model change re-reads the applied effort, so a model without effort reports
  none and a capped model reports its downgrade.
- Model rows are named from Claude's own strings. When the description starts
  with a versioned identity segment before ` · ` that differs from the alias
  `displayName`, that segment is the name ("Opus 5.5 with 1M context",
  "Fable 5.1", "Sonnet 5", "Haiku 4.5") and the alias label moves into the
  description; `default` keeps "Default (recommended)" because it follows
  Claude's own default. Versioned catalogue rows keep their `displayName`. The
  resolved ID is appended to the description. Pinned resolved-ID rows are no
  longer listed (captured resolved IDs are still accepted), and legacy entries
  the runtime lists by value or resolved ID are not repeated.
- Documented legacy IDs are listed under every catalogue level because the
  runtime does run them with effort (a live switch to `claude-opus-4-6`
  reported the session's `max`). Their selections are accepted only when the
  readback matches; a downgrade is rejected naming the applied level.

## Limits

- Verified with fake runtimes in `apps/go/internal/acpbridge`, the control
  probe above and a live bridge session (initialize, `session/new`, model and
  effort changes only): Sonnet 5 accepted `max`, Haiku reported no effort, and
  a level a model does not report was rejected before any control was sent.
  No live prompt confirmed that a selected effort reached a provider request;
  `applied.effort` is the CLI's own statement of what it will send.
- `CLAUDE_CODE_EFFORT_LEVEL` in the server's environment overrides session
  effort inside the CLI; the readback reports the override, so a selection that
  differs is rejected rather than claimed.
- Older CLIs without `get_settings` show no effort control.
