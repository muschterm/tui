#!/usr/bin/env python3
"""OS-PTY clipboard context-menu checks with isolated command wrappers."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from pty_smoke import Terminal


def install_clipboard_wrappers(directory):
    """Install atotto/clipboard-compatible wrappers that never touch the OS clipboard."""
    data = directory / 'clipboard.data'
    log = directory / 'clipboard.log'
    names = ('pbcopy', 'pbpaste') if sys.platform == 'darwin' else ('wl-copy', 'wl-paste')
    bodies = {
        names[0]: '#!/bin/sh\nset -eu\nprintf \'%s\\n\' "${0##*/}" >> "$TUI_TEST_CLIPBOARD_LOG"\ncat > "$TUI_TEST_CLIPBOARD_DATA"\n',
        names[1]: '#!/bin/sh\nset -eu\nprintf \'%s\\n\' "${0##*/}" >> "$TUI_TEST_CLIPBOARD_LOG"\ncat "$TUI_TEST_CLIPBOARD_DATA"\n',
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
    return log.read_text().splitlines() if log.exists() else []


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
            screen = require_menu(terminal, 'Selected text', ['Copy'])
            (artifacts / 'transcript-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            check(not operations(log), 'right-clicking selected transcript text opens its menu without copying')
            terminal.send(b'\r', .8)
            check(len(operations(log)) == 1 and 'copy' in Path(operations(log)[0]).name,
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
            screen = require_menu(terminal, 'Prompt', ['Paste'])
            (artifacts / 'prompt-paste-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            check(not operations(log), 'right-clicking an unselected prompt opens Paste without reading clipboard')
            terminal.send(b'\r', .8)
            check('LEFTCURSOR-PASTE-right' in '\n'.join(terminal.screen()),
                  'keyboard-selected Paste inserts clipboard text at the prompt cursor')
            check(len(operations(log)) == 1 and 'paste' in Path(operations(log)[0]).name,
                  'prompt Paste invokes only the isolated paste wrapper')

            # Select text in the prompt to expose Copy, navigate to it, and verify
            # that a later explicit Paste replaces the selection in place.
            phrase = 'CURSOR-PASTE'
            sx, sy, _ = locate(terminal, phrase)
            # Textarea selection endpoints are exclusive; the mouse endpoint
            # must sit just after the final selected cell.
            drag_select(terminal, sx, sy, sx + len(phrase), sy)
            terminal.pump(.3)
            right_click(terminal, sx + 1, sy)
            screen = require_menu(terminal, 'Prompt', ['Copy', 'Paste'])
            _, menu_y, _ = locate(terminal, 'Prompt')
            paste_row = next(i for i, line in enumerate(screen[menu_y:], menu_y) if 'Paste' in line)
            copy_row = next(i for i, line in enumerate(screen[menu_y:], menu_y) if 'Copy' in line)
            check(copy_row < paste_row, 'selected prompt menu presents Copy then Paste for keyboard navigation')
            check(len(operations(log)) == 1,
                  'right-clicking selected prompt text opens Copy without reading or writing clipboard')
            (artifacts / 'prompt-selection-menu.screen.txt').write_text('\n'.join(screen) + '\n')
            terminal.send(b'\r', .8)
            check(data.read_text() == phrase and len(operations(log)) == 2
                  and 'copy' in Path(operations(log)[1]).name,
                  'Enter copies the selected prompt text through the isolated wrapper')

            data.write_text('REPLACED')
            sx, sy, _ = locate(terminal, phrase)
            drag_select(terminal, sx, sy, sx + len(phrase), sy)
            terminal.pump(.3)
            right_click(terminal, sx + 1, sy)
            require_menu(terminal, 'Prompt', ['Copy', 'Paste'])
            before = operations(log)
            check(len(before) == 2, 'opening replacement menu does not access the clipboard')
            terminal.send(b'\x1b[B\r', .8)
            rendered = '\n'.join(terminal.screen())
            check('LEFTREPLACED-right' in rendered,
                  'Paste replaces the selected prompt text without changing surrounding text')
            after = operations(log)
            check(len(after) == 3 and 'paste' in Path(after[-1]).name,
                  'replacement Paste invokes only the isolated paste wrapper')

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
