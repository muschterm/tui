# Built-in bridge permission modes — 2026-09-22

## Scope and evidence

This note records the permission controls used by the built-in Go bridges. It
does not establish a filesystem boundary around Claude or Codex, and it does
not report a paid provider tool-action test.

- Installed CLIs inspected locally: Claude Code `2.1.280` (`claude --help`) and
  Codex CLI `0.155.1` (`codex app-server generate-json-schema --experimental`).
- [Claude's permission reference](https://code.claude.com/docs/en/permissions)
  describes `default` (shown as Manual), `acceptEdits`, `auto`, and
  `bypassPermissions`. It says `manual` is an alias in recent versions. Claude
  `--help` exposes `--allow-dangerously-skip-permissions`, which enables the
  bypass choice without activating it at startup. The installed CLI's
  no-prompt `initialize` response includes per-model `supportsAutoMode`; the
  bridge omits Auto for a selected model that does not report support.
- The [Codex App Server protocol](https://learn.chatgpt.com/docs/app-server)
  documents `approvalPolicy`, `sandboxPolicy`, and the turn-level overrides.
  The installed v2 schema additionally includes `approvalsReviewer` on
  `thread/settings/update` and `turn/start`, with `user` and `auto_review`.
  Its `ThreadStartResponse` reports the effective approval policy, sandbox,
  and reviewer. `ThreadSettingsUpdateResponse` is empty: a successful RPC
  acknowledges acceptance of requested settings, but does not echo the
  resulting effective policy.
- The local T3 Code checkout (`1ba471a37cd6b0f18820795f4505206f4723a0e3`) maps Auto to Codex `on-request` plus
  `workspace-write` plus `auto_review`, and Full access to `never` plus
  `danger-full-access`. Its Claude mapping uses `auto`, `acceptEdits`, and
  `bypassPermissions`. Source:
  `apps/server/src/provider/Layers/CodexSessionRuntime.ts` and
  `apps/server/src/provider/Layers/ClaudeAdapter.ts` in that checkout.

## Bridge mapping

| Display | Claude control | Codex policy / sandbox / reviewer |
| --- | --- | --- |
| Supervised | `default` | `on-request` / `workspaceWrite` / `user` |
| Auto-accept edits | `acceptEdits` | Not offered: no equivalent distinct policy |
| Auto | `auto` | `on-request` / `workspaceWrite` / `auto_review` |
| Full access | `bypassPermissions` | `never` / `dangerFullAccess` / `user` |

Codex explicitly establishes **Supervised** when a thread is opened or resumed.
This narrows a wider personal CLI default and avoids treating the mutable
inherited policy as a stable option after a Full access turn or restart. Claude
starts in its existing `default` mode. Merely displaying Claude's options
does not change its permission policy.

The server captures the selected option per prompt, validates it against the
current session catalogue, and applies it before dispatch. A no-prompt live
control probe on Claude 2.1.280 rejected an invalid mode and returned
`response.mode` for `auto` and `default`. The bridge updates its reported
option only when that native value matches the request.
Codex sends the policy to `thread/settings/update`, waits for its response,
then repeats the same values in `turn/start`. Unsupported IDs and rejected
updates stop before prompt dispatch. Old captured `permissions=unavailable`
resolves to explicit Supervised/`default` before dispatch, so it cannot inherit
an earlier Full access selection. On restart, the server drops saved ACP
session option catalogues so the fresh probe supplies the current selector;
captured prompt settings remain intact.

## Limits

Claude's `--effort` is a process-launch flag, and the CLI says available levels
depend on the model. A no-prompt `initialize` on installed Claude 2.1.280
reported `supportedEffortLevels` per model (including `high` for its Opus
option). A no-prompt `apply_flag_settings` with `effortLevel: "high"` returned
generic success, but so did an intentionally invalid effort value; neither
response reported effective state. The persistent stream bridge therefore
still reports effort as unavailable until a verified per-prompt control exists.
The `auto` modes can also be
disabled or constrained by provider or managed settings; a rejected mode is
shown as a failed settings application, never silently replaced.

Tests use fake installed CLI processes to check option discovery, native mode
translation, setting acknowledgment order, rejected selections, turn
overrides, and restoration of Supervised after Full access. They do not prove
that provider tools honor every selected restriction in a live account.
