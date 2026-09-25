#!/usr/bin/env python3
"""Embedded terminal via OS PTY: a real shell in the bottom panel, typed through
the app. Byte-level evidence only, not GUI terminal compatibility. The child
shell runs with a temporary HOME and no history file."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unicodedata
import urllib.request

from pty_smoke import Terminal
from harness_env import isolated_env


def cells(text):
    return sum(2 if unicodedata.east_asian_width(c) in ('W', 'F') else
               0 if unicodedata.combining(c) else 1 for c in text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, required=True)
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'OS PTY + isolated server; embedded bash through the TUI', 'checks': []}
    shell = '/bin/bash' if os.path.exists('/bin/bash') else '/bin/sh'

    def check(condition, description):
        if not condition:
            raise AssertionError(description)
        report['checks'].append(description)

    with tempfile.TemporaryDirectory(prefix='tui-term-home-', dir='/tmp') as directory, \
            tempfile.TemporaryDirectory(prefix='tui-term-user-', dir='/tmp') as user_home:
        home = Path(directory)
        # The server passes its environment to shells: keep them out of the
        # real home and its shell history.
        child = {'HOME': user_home, 'HISTFILE': '/dev/null', 'SHELL': shell, 'PS1': 'term$ ',
                 'BASH_ENV': None, 'ENV': None}
        environment = isolated_env(directory, home=user_home, SHELL=shell, PS1='term$ ')
        terminals = []

        def cli(*words):
            return subprocess.check_output([str(binary), *words], env=environment, text=True)

        def api(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(request, timeout=3) as response:
                return json.load(response)

        def records():
            return api('snapshot').get('terminals') or []

        def wait(condition, what, seconds=8):
            end = time.monotonic() + seconds
            while time.monotonic() < end:
                t.pump(.2)
                if condition():
                    return
            raise AssertionError('timed out: ' + what)

        def visible(text):
            return any(text in line for line in t.screen()[:-1])

        try:
            cli('server', 'start')
            t = Terminal(binary, home, 'term', artifacts, environment=child)
            terminals.append(t)
            t.key(5)  # show the bottom panel: opens the first session
            wait(lambda: any(r['State'] == 'running' for r in records()), 'terminal record running')
            record = records()[0]
            check(record['Dir'] == user_home and record['Shell'] == shell and record['ControlGen'] == 1,
                  'F5 opens one server terminal in the checkout fallback directory with this client in control')
            wait(lambda: visible('term$'), 'shell prompt painted')
            check(visible('Terminal 1'), 'bottom panel shows the terminal tab and the live prompt')
            prompt = next(y for y, line in enumerate(t.screen()[:-1]) if 'term$' in line)
            line = t.screen()[prompt]
            t.click(cells(line[:line.index('term$')]), prompt)
            wait(lambda: visible('Ctrl+] to leave'), 'input focus hint')
            check(True, 'clicking the grid enters terminal input focus with the Ctrl+] hint')
            t.send(b'echo hello-$((6*7))\r', .5)
            wait(lambda: visible('hello-42'), 'typed command output')
            check(visible('hello-42'), 'typed keys reach the shell and its output is painted')
            t.send(b'\x1d', .4)  # Ctrl+]
            wait(lambda: not visible('Ctrl+] to leave'), 'focus left')
            t.send(b'zz-not-typed', .4)
            t.send(b'\r', .4)  # Enter on the focused pane re-enters focus
            wait(lambda: visible('Ctrl+] to leave'), 'focus re-entered')
            t.send(b'\x1d', .4)
            check(not any('zz-not-typed' in line for line in t.screen()[:-1]),
                  'Ctrl+] leaves terminal focus; later keys stay with the app')
            # Hiding and showing the panel keeps the same session.
            t.key(5)
            t.key(5)
            wait(lambda: visible('hello-42'), 'screen after re-show')
            check(len(records()) == 1 and records()[0]['State'] == 'running',
                  'hiding and showing the panel preserves the session and its screen')
            # Close the tab through its icon slot (three cells before its name).
            row = next(y for y, line in enumerate(t.screen()[:-1]) if 'Terminal 1' in line)
            line = t.screen()[row]
            t.click(cells(line[:line.index('Terminal 1')]) - 3, row)
            wait(lambda: records() and records()[0]['State'] == 'ended', 'terminal ended after close')
            ended = records()[0]
            check(ended['EndReason'] == 'closed' and ended.get('Exit') is not None,
                  'closing the tab ends the shell with a confirmed exit')
            wait(lambda: not visible('Terminal 1'), 'tab removed')
            check(True, 'the closed tab is removed and the panel hides')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            cli('server', 'stop')
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
