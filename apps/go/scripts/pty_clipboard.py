#!/usr/bin/env python3
"""OS-PTY clipboard context-menu checks with isolated command wrappers."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from pty_smoke import Terminal


def install_clipboard_wrappers(directory):
    """Install atotto/clipboard-compatible wrappers that never touch the OS clipboard.

    Every invocation of either wrapper is logged as "<name> <args>", regardless
    of which args it was called with, so a single wrapper directory on PATH
    covers every way the app can reach a real "wl-paste"/"wl-copy" (a plain
    text read, a `--list-types` probe, or a typed `--type <mime>` read) and
    the log lets a check assert the exact call sequence, not just the count.
    """
    data = directory / 'clipboard.data'
    log = directory / 'clipboard.log'
    names = ('pbcopy', 'pbpaste') if sys.platform == 'darwin' else ('wl-copy', 'wl-paste')
    bodies = {
        names[0]: '#!/bin/sh\nset -eu\nprintf \'%s %s\\n\' "${0##*/}" "$*" >> "$TUI_TEST_CLIPBOARD_LOG"\ncat > "$TUI_TEST_CLIPBOARD_DATA"\n',
        names[1]: '#!/bin/sh\nset -eu\nprintf \'%s %s\\n\' "${0##*/}" "$*" >> "$TUI_TEST_CLIPBOARD_LOG"\ncat "$TUI_TEST_CLIPBOARD_DATA"\n',
    }
    for name, body in bodies.items():
        path = directory / name
        path.write_text(body)
        path.chmod(0o755)
    return data, log


def event(terminal, button, x, y, final='M'):
    terminal.send(f'\x1b[<{button};{x + 1};{y + 1}{final}'.encode(), .08)


def right_click(terminal, x, y):
    event(terminal, 2, x, y)
    event(terminal, 2, x, y, 'm')


def drag_select(terminal, x0, y0, x1, y1):
    event(terminal, 0, x0, y0)
    event(terminal, 32, x1, y1)
    event(terminal, 0, x1, y1, 'm')


def locate(terminal, text, timeout=3):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        screen = terminal.screen()
        for y, line in enumerate(screen):
            x = line.find(text)
            if x >= 0:
                return x, y, screen
        terminal.pump(.1)
    raise AssertionError(f'{terminal.identity}: visible text {text!r} missing')


def require_menu(terminal, title, labels):
    _, title_y, screen = locate(terminal, title)
    rows = screen[title_y:min(len(screen), title_y + len(labels) + 5)]
    rendered = '\n'.join(rows)
    missing = [label for label in labels if not any(label in row for row in rows)]
    if missing:
        raise AssertionError(f'{terminal.identity}: {title!r} menu missing {missing}:\n{rendered}')
    return screen


def operations(log):
    """Every wrapper invocation logged so far, as "<name> <args>" strings."""
    return log.read_text().splitlines() if log.exists() else []


def op_name(line):
    return Path(line.split(' ', 1)[0]).name


# Explicit Paste now probes the isolated wrapper's offered types before
# falling back to a text read (docs/research/go-image-clipboard-2026-09-24.md),
# so one Paste costs two wrapper calls, not one. A harness clipboard with no
# recognized file/image type always takes this exact two-call path.
PASTE_LIST_TYPES = 'wl-paste --list-types'
PASTE_TEXT_READ = 'wl-paste --no-newline'


def check_paste_sequence(check, before, after, label):
    """Assert a single Paste appended exactly [list-types, text-read] to the
    isolated wrapper log, through no path but that wrapper."""
    added = after[len(before):]
    check(added == [PASTE_LIST_TYPES, PASTE_TEXT_READ],
          f'{label} invokes only the isolated wrapper, listing types before reading text '
          f'(saw {added!r})')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path)
    args = parser.parse_args()
    if sys.platform not in ('darwin', 'linux'):
        raise SystemExit('pty_clipboard.py supports macOS and Linux clipboard command routing')

    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix='tui-pty-clipboard-evidence-', dir='/tmp'))
    artifacts.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    report = {'harness': 'Python OS PTY + isolated clipboard command wrappers',
              'platform': sys.platform, 'artifacts': str(artifacts), 'checks': []}

    def check(condition, description):
        if not condition:
            raise AssertionError(description)
        report['checks'].append(description)

    with tempfile.TemporaryDirectory(prefix='tui-pty-clipboard-', dir='/tmp') as directory:
        scratch = Path(directory)
        wrapper_dir = scratch / 'bin'
        wrapper_dir.mkdir()
        data, log = install_clipboard_wrappers(wrapper_dir)
        data.write_text('CURSOR-PASTE')
        home = scratch / 'home'
        home.mkdir()
        child_env = {
            'PATH': str(wrapper_dir) + os.pathsep + os.environ.get('PATH', ''),
            'TUI_TEST_CLIPBOARD_DATA': str(data),
            'TUI_TEST_CLIPBOARD_LOG': str(log),
            # This harness models a local client, independently of the host
            # session used to launch the test runner.
            'SSH_TTY': None, 'SSH_CONNECTION': None, 'SSH_CLIENT': None,
            'HERDR_ENV': None,
        }
        if sys.platform == 'linux':
            # atotto/clipboard selects only these scratch wl-* wrappers at init.
            child_env['WAYLAND_DISPLAY'] = 'pty-clipboard-test'
        terminal = None
        try:
            terminal = Terminal(binary, home, 'pty-clipboard', artifacts, environment=child_env)
            check(not operations(log), 'startup invokes no clipboard command')

            transcript = 'calm workspace'
            tx, ty, _ = locate(terminal, transcript)
            drag_select(terminal, tx, ty, tx + len(transcript) - 1, ty)
            terminal.pump(.4)
            right_click(terminal, tx + 2, ty)
            screen = require_menu(terminal, 'SELECTED TEXT', ['Copy'])
            (artifacts / 'transcript-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            check(not operations(log), 'right-clicking selected transcript text opens its menu without copying')
            terminal.send(b'\r', .8)
            check(len(operations(log)) == 1 and op_name(operations(log)[0]) == 'wl-copy',
                  'keyboard selection of Copy invokes only the isolated copy wrapper')
            check(data.read_text() == transcript,
                  'transcript Copy writes the exact selected text to the scratch clipboard')

            # Exercise cursor insertion through the prompt context menu.
            data.write_text('CURSOR-PASTE')
            prompt = 'LEFT-right'
            terminal.click_label('Ask to do anything')
            terminal.send(prompt.encode(), .3)
            px, py, _ = locate(terminal, prompt)
            insertion_x = px + len('LEFT')
            event(terminal, 0, insertion_x, py)
            event(terminal, 0, insertion_x, py, 'm')
            log.write_text('')
            right_click(terminal, insertion_x, py)
            screen = require_menu(terminal, 'PROMPT', ['Paste'])
            (artifacts / 'prompt-paste-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            check(not operations(log), 'right-clicking an unselected prompt opens Paste without reading clipboard')
            terminal.send(b'\r', .8)
            check('LEFTCURSOR-PASTE-right' in '\n'.join(terminal.screen()),
                  'keyboard-selected Paste inserts clipboard text at the prompt cursor')
            check_paste_sequence(check, [], operations(log), 'prompt Paste')

            # Select text in the prompt to expose Copy, navigate to it, and verify
            # that a later explicit Paste replaces the selection in place.
            phrase = 'CURSOR-PASTE'
            sx, sy, _ = locate(terminal, phrase)
            # Textarea selection endpoints are exclusive; the mouse endpoint
            # must sit just after the final selected cell.
            drag_select(terminal, sx, sy, sx + len(phrase), sy)
            terminal.pump(.3)
            right_click(terminal, sx + 1, sy)
            screen = require_menu(terminal, 'PROMPT', ['Copy', 'Paste'])
            _, menu_y, _ = locate(terminal, 'PROMPT')
            paste_row = next(i for i, line in enumerate(screen[menu_y:], menu_y) if 'Paste' in line)
            copy_row = next(i for i, line in enumerate(screen[menu_y:], menu_y) if 'Copy' in line)
            check(copy_row < paste_row, 'selected prompt menu presents Copy then Paste for keyboard navigation')
            after_first_paste = operations(log)
            check(len(after_first_paste) == 2,
                  'right-clicking selected prompt text opens Copy without reading or writing clipboard')
            (artifacts / 'prompt-selection-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            terminal.send(b'\r', .8)
            after_copy = operations(log)
            check(data.read_text() == phrase and len(after_copy) == 3
                  and op_name(after_copy[-1]) == 'wl-copy',
                  'Enter copies the selected prompt text through the isolated wrapper')

            data.write_text('REPLACED')
            sx, sy, _ = locate(terminal, phrase)
            drag_select(terminal, sx, sy, sx + len(phrase), sy)
            terminal.pump(.3)
            right_click(terminal, sx + 1, sy)
            require_menu(terminal, 'PROMPT', ['Copy', 'Paste'])
            before = operations(log)
            check(before == after_copy, 'opening replacement menu does not access the clipboard')
            terminal.send(b'\x1b[B\r', .8)
            rendered = '\n'.join(terminal.screen())
            check('LEFTREPLACED-right' in rendered,
                  'Paste replaces the selected prompt text without changing surrounding text')
            after = operations(log)
            check_paste_sequence(check, before, after, 'replacement Paste')

            # Plain Ctrl+V and enhanced forwarded shortcuts must take the same
            # guarded path as context-menu Paste, without inserting a literal v.
            for sequence, label in (
                (b'\x16', 'Ctrl+V'),
                (b'\x1b[118;6u', 'Ctrl+Shift+V'),
                (b'\x1b[118;9u', 'Super+V'),
            ):
                marker = 'KEYBOARD-' + label
                data.write_text(marker)
                before = operations(log)
                terminal.send(sequence, .8)
                check_paste_sequence(check, before, operations(log), label + ' Paste')
                check(marker in '\n'.join(terminal.screen()),
                      label + ' inserts clipboard text into the prompt')

            # Reproduce a persistent herdr pane with no inherited SSH markers.
            # We inspect the OSC 52 output; there is no real desktop clipboard.
            terminal.close()
            child_env['HERDR_ENV'] = '1'
            terminal = Terminal(binary, home, 'pty-herdr-clipboard', artifacts,
                                environment=child_env)
            before = operations(log)
            tx, ty, _ = locate(terminal, transcript)
            drag_select(terminal, tx, ty, tx + len(transcript) - 1, ty)
            right_click(terminal, tx + 2, ty)
            require_menu(terminal, 'SELECTED TEXT', ['Copy'])
            offset = len(terminal.output)
            terminal.send(b'\r', .8)
            expected = b'\x1b]52;c;' + base64.b64encode(transcript.encode())
            check(expected in terminal.output[offset:],
                  'herdr pane Copy emits exact OSC 52 text without SSH markers')
            check(operations(log) == before,
                  'herdr pane Copy never calls the Mac host clipboard writer')
            terminal.click_label('Ask to do anything')
            terminal.send(b'\x16', .4)
            check(operations(log) == before,
                  'herdr pane forwarded Paste never reads the Mac host clipboard')
            terminal.send(b'\x1b[200~VIEWER-PASTE\x1b[201~', .5)
            check('VIEWER-PASTE' in '\n'.join(terminal.screen()),
                  'herdr pane accepts clipboard text delivered by the viewing terminal')

            report['result'] = 'PASS'
        except Exception as error:
            report['result'] = 'FAIL'
            report['error'] = str(error)
            if terminal is not None:
                (artifacts / 'failure.screen.txt').write_text('\n'.join(terminal.screen()) + '\n')
            raise
        finally:
            if terminal is not None:
                terminal.close()
            env = dict(os.environ, TUI_GO_HOME=str(home))
            subprocess.run([str(binary), 'server', 'stop'], env=env,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            report['clipboard_operations'] = operations(log)
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
