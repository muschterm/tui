#!/usr/bin/env python3
"""Native app/OS-PTY color negotiation with synthetic terminal replies.

This verifies byte parsing and renderer behavior, not GUI terminal, SSH or tmux
compatibility. Uses an isolated application home and leaves existing servers alone.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.request

from pty_smoke import Terminal


VERSION = b'\x1b[>q'
RGB = b'\x1bP+q524742\x1b\\'
TC = b'\x1bP+q5463\x1b\\'


def has_rgb(output):
    return re.search(rb'\x1b\[[0-9;:]*[34]8[;:]2[;:]', output) is not None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path)
    args = parser.parse_args()
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix='tui-color-evidence-', dir='/tmp'))
    artifacts.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    base = dict.fromkeys(['COLORTERM', 'TERM_PROGRAM', 'TERM_PROGRAM_VERSION', 'NO_COLOR',
                         'CLICOLOR', 'CLICOLOR_FORCE', 'TTY_FORCE', 'TMUX', 'WT_SESSION',
                         'GOOGLE_CLOUD_SHELL'])
    base['TERM'] = 'xterm-256color'
    cases = [
        ('silent-256', {}, None, None),
        ('silent-16', {'TERM': 'tui-color-probe'}, None, None),
        ('ghostty', {}, 'ghostty 1.2.0', b'\x1bP1+r5463\x1b\\'),
        ('kitty', {}, 'kitty(0.43.0)', b'\x1bP1+r5463\x1b\\'),
        ('iterm2', {}, 'iTerm2 3.6.11', b'\x1bP1+r524742=38\x1b\\'),
        ('negative', {}, 'ghostty 1.2.0', b'\x1bP0+r524742\x1b\\\x1bP0+r5463\x1b\\'),
        ('tmux-fallback', {}, 'tmux 3.5a', None),
        ('unknown', {}, 'unknown 1.0', None),
        ('no-color', {'NO_COLOR': '0'}, None, None),
        ('apple', {'TERM_PROGRAM': 'Apple_Terminal'}, None, None),
        ('env-truecolor', {'COLORTERM': 'truecolor'}, None, None),
    ]
    report = {'harness': 'OS PTY + native binary + synthetic terminal responses', 'cases': []}
    with tempfile.TemporaryDirectory(prefix='tui-color-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = dict(os.environ, TUI_GO_HOME=directory)
        try:
            subprocess.run([str(binary), 'server', 'start'], env=env, check=True, capture_output=True)
            for name, overrides, version, reply in cases:
                terminal = Terminal(binary, home, name, artifacts, dict(base, **overrides))
                draft = 'draft survives ' + name
                try:
                    initially_true = name == 'env-truecolor'
                    should_probe = name not in ('no-color', 'apple', 'env-truecolor')
                    assert terminal.output.count(VERSION) == int(should_probe), (name, 'version query count')
                    assert has_rgb(terminal.output) == initially_true, (name, 'initial color profile')
                    assert RGB not in terminal.output and TC not in terminal.output, (name, 'ungated DCS query')
                    terminal.send(draft.encode(), .15)
                    if version:
                        response = b'\x1bP>|' + version.encode() + b'\x1b\\'
                        if name == 'ghostty':
                            terminal.send(response[:5], .08)
                            terminal.send(response[5:], .2)
                        else:
                            terminal.send(response, .2)
                        allowed = name in ('ghostty', 'kitty', 'iterm2', 'negative')
                        assert terminal.output.count(RGB) == int(allowed), (name, 'RGB query gate')
                        assert terminal.output.count(TC) == int(allowed), (name, 'Tc query gate')
                        assert not has_rgb(terminal.output), (name, 'version alone upgraded color')
                        if reply:
                            # Exercise fragmented input through the real decoder.
                            terminal.send(reply[:5], .08)
                            terminal.send(reply[5:] + b' next', .25)
                            draft += ' next'
                            expected = name != 'negative'
                            assert has_rgb(terminal.output) == expected, (name, 'capability result')
                        terminal.send(response, .1)
                        assert terminal.output.count(RGB) == int(allowed) and terminal.output.count(TC) == int(allowed), (name, 'repeated negotiation')
                    else:
                        # Unsolicited positive replies must not bypass policy.
                        terminal.send(b'\x1bP1+r524742=38\x1b\\\x1bP1+r5463\x1b\\', .2)
                        assert has_rgb(terminal.output) == initially_true, (name, 'unsolicited upgrade')
                    assert any('Submit' in row for row in terminal.screen()), (name, 'question control disappeared')
                    if name == 'silent-16':
                        assert not re.search(rb'\x1b\[[0-9;:]*[34]8[;:]5[;:]', terminal.output), '16-color fallback used indexed colors'
                    if name == 'no-color':
                        assert b'\x1b]10;' not in terminal.output and b'\x1b]11;' not in terminal.output, 'NO_COLOR changed terminal default colors'
                    if name == 'silent-256':
                        # An abandoned header followed by ordinary typing must
                        # replay that typing after the absolute deadline.
                        terminal.send(b'\x1bP>|', .08)
                        terminal.send(b' preserved', 1.2)
                        draft += ' preserved'
                    if name == 'unknown':
                        # Ctrl+Q in close() aborts an incomplete response and
                        # saves buffered user typing before detaching.
                        terminal.send(b'\x1bP>|', .08)
                        terminal.send(b' saved', .02)
                        draft += ' saved'
                finally:
                    terminal.close()
                assert b'\x1b[?1049l' in terminal.output, (name, 'alternate screen not restored')
                discovery = json.loads((home / 'discovery.json').read_text())
                request = urllib.request.Request(discovery['URL'] + '/v1/views/' + name,
                    headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
                with urllib.request.urlopen(request, timeout=3) as response:
                    state = json.load(response)['data']
                assert state['Threads'][state['Active']]['Draft'] == draft, (name, 'terminal replies altered draft')
                report['cases'].append(name)
                print(name + ': passed', flush=True)
        finally:
            subprocess.run([str(binary), 'server', 'stop'], env=env, capture_output=True, timeout=5)
    (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(str(artifacts / 'report.json'))


if __name__ == '__main__':
    main()
