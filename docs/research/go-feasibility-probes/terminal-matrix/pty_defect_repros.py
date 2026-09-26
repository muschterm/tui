#!/usr/bin/env python3
"""Minimal plain-OS-PTY repros for defects first seen in the tmux matrix
(evidence, not app code). No tmux involved: shows the defects are in the app.

1. Backspace on an EMPTY composer, then typing "ghijkl", then five
   Backspaces leaves "l" (expected "g"): the cursor is misplaced.
2. A bracketed paste whose line break is a lone CR ("d1\\rd2") lands as
   "d1d2" on one line; LF ("e1\\ne2") keeps two lines.

Usage: pty_defect_repros.py --binary PATH
Uses apps/go/scripts/pty_smoke.Terminal and harness_env isolation.
"""
import argparse
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'apps/go/scripts'))
from pty_smoke import Terminal  # noqa: E402
from harness_env import isolated_env  # noqa: E402


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='tui-repro-', dir='/tmp') as d:
        t = Terminal(args.binary.resolve(), Path(d), 'repro', Path(d), columns=160, rows=45)
        t.pump(2)

        def draft():
            ls = t.screen()
            bottom = max(i for i, l in enumerate(ls) if 'Ctx' in l)
            top = max(i for i in range(bottom) if '╭' in ls[i])
            left = ls[top].index('╭')
            right = ls[top].index('╮', left)
            return [l[left + 1:right].strip(' │') for l in ls[top + 1:bottom] if l[left + 1:right].strip(' │')]
        try:
            t.send(b'abcdef', .8)
            t.send(b'\x7f' * 6, 1)
            t.send(b'\x7f', .6)  # one Backspace on the empty composer
            t.send(b'ghijkl', 1)
            t.send(b'\x7f' * 5, 1)
            got = draft()
            print('1 backspace-on-empty:', 'DEFECT' if got != ['g'] else 'ok', got)
            t.send(b'\x1b[F' + b'\x7f' * 8, .6)  # End, then Backspace clears the leftover wherever the cursor is
            print('  after clearing:', draft())
            t.send(b'\x1b[200~d1\rd2\x1b[201~', 1)
            got = draft()
            print('2 bracketed paste with CR:', 'DEFECT' if got != ['d1', 'd2'] else 'ok', got)
            t.send(b'\x1b[F' + b'\x7f' * 8, .6)
            t.send(b'\x1b[200~e1\ne2\x1b[201~', 1)
            print('  bracketed paste with LF:', draft())
        finally:
            t.send(b'\x11', 1)
            t.close()
            subprocess.run([str(args.binary), 'server', 'stop'], env=isolated_env(d), capture_output=True)


if __name__ == '__main__':
    main()
