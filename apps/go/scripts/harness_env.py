"""Environment isolation shared by the PTY harnesses.

Every server, TUI and shell a harness starts runs with its own application
home (TUI_GO_HOME), a temporary user HOME, no shell history file and no shell
startup files named by ENV or BASH_ENV, so harness runs never read or write
the developer's real home (for example ~/.bash_history). Embedded terminals
inherit the server's environment, so this applies to their shells too.

Unless a harness opts into real provider CLIs (keep_home=True, only for the
live-agent harnesses gated on TUI_GO_LIVE_ACP=1), the server's built-in
Claude/Codex adapters must never reach the developer's installed `claude`/
`codex` on PATH: their configured executables are pointed at a path that
cannot exist, so agent discovery fails closed regardless of what else is on
PATH or installed as an ACP adapter/node/npx.
"""
import os
from pathlib import Path


def user_home(tui_home):
    """The temporary user HOME for an application home; created on demand."""
    path = Path(tui_home) / 'user-home'
    path.mkdir(parents=True, exist_ok=True)
    return path


def isolated_env(tui_home, home=None, keep_home=False, **overrides):
    """Return os.environ adjusted for a harness run.

    home replaces the default temporary user HOME. keep_home keeps the real
    HOME and leaves real agent CLI discovery on PATH untouched, only for
    live-agent harnesses whose provider CLIs need the user's credentials;
    history and startup files stay disabled either way. overrides set
    variables, and None removes one.
    """
    env = dict(os.environ, TUI_GO_HOME=str(tui_home), HISTFILE='/dev/null')
    if not keep_home:
        env['HOME'] = str(home or user_home(tui_home))
    for key in ('ENV', 'BASH_ENV'):
        env.pop(key, None)
    if not keep_home:
        missing = Path(tui_home) / 'no-agent-cli'
        env['CLAUDE_CODE_EXECUTABLE'] = str(missing / 'claude')
        env['CODEX_PATH'] = str(missing / 'codex')
    for key, value in overrides.items():
        if value is None:
            env.pop(key, None)
        else:
            env[key] = value
    return env
