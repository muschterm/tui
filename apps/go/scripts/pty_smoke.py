#!/usr/bin/env python3
"""OS-PTY smoke, not a GUI terminal compatibility claim. No third-party modules."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import signal
import struct
import subprocess
import tempfile
import termios
import time
import urllib.request
import unicodedata

KEYS = {2: b'\x1bOQ', 3: b'\x1bOR', 4: b'\x1bOS', 5: b'\x1b[15~',
        6: b'\x1b[17~', 7: b'\x1b[18~', 8: b'\x1b[19~'}


class Terminal:
    def __init__(self, binary, home, identity, artifacts, environment=None, columns=160, rows=50):
        self.identity = identity
        self.path = artifacts / (identity + '.pty')
        self.output = bytearray()
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.environ.update(TUI_GO_HOME=str(home), TERM='xterm-256color', PS1='PTY_READY> ')
            for key, value in (environment or {}).items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value
            os.execv('/bin/bash', ['bash', '--noprofile', '--norc', '-i'])
        self.resize(columns, rows)
        self.pump(.3)
        self.send((shlex.quote(str(binary)) + ' --client ' + identity + '\n').encode(), 2)

    def pump(self, seconds=.3):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if select.select([self.fd], [], [], max(0, end-time.monotonic()))[0]:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.output.extend(data)
        self.path.write_bytes(self.output)

    def send(self, data, delay=.3):
        os.write(self.fd, data)
        self.pump(delay)

    def key(self, n):
        self.send(KEYS[n])

    def resize(self, columns, rows):
        self.columns, self.rows = columns, rows
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, columns, 0, 0))

    def click(self, x, y):
        self.send(f'\x1b[<0;{x+1};{y+1}M\x1b[<0;{x+1};{y+1}m'.encode())

    def screen(self):
        """Read the fixture renderer's cursor-addressed ANSI output, not a full emulator."""
        grid = [[' '] * self.columns for _ in range(self.rows)]
        x = y = 0
        top, bottom = 0, self.rows-1
        tokens = re.findall(r'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1bP[^\x1b]*\x1b\\|\x1b.|[^\x1b]', self.output.decode('utf-8', 'replace'))
        for token in tokens:
            if token.startswith('\x1b['):
                code = token[-1]
                raw = token[2:-1]
                if raw.startswith('?'):
                    if raw == '?1049' and code == 'h':
                        grid = [[' '] * self.columns for _ in range(self.rows)]
                        x = y = 0
                    continue
                parts = [int(v) if v.isdigit() else 0 for v in raw.split(';')]
                n = parts[0] or 1
                if code in 'Hf':
                    y, x = n - 1, (parts[1] or 1) - 1 if len(parts) > 1 else 0
                elif code == 'A': y = max(0, y-n)
                elif code == 'B': y = min(self.rows-1, y+n)
                elif code == 'C': x = min(self.columns-1, x+n)
                elif code == 'D': x = max(0, x-n)
                elif code == 'G': x = n-1
                elif code == 'r':
                    # Replaying earlier, larger frames after a resize must not
                    # create a scrolling region outside the current grid.
                    top = min(self.rows-1, max(0, n-1))
                    requested_bottom = (parts[1] or self.rows)-1 if len(parts)>1 else self.rows-1
                    bottom = max(top, min(self.rows-1, requested_bottom))
                    x = y = 0
                elif code in ('S', 'T'):
                    for _ in range(min(n, bottom-top+1)):
                        if code == 'T':
                            grid.insert(top, [' '] * self.columns)
                            del grid[bottom+1]
                        else:
                            del grid[top]
                            grid.insert(bottom, [' '] * self.columns)
                elif code == 'Z': x = max(0, ((x-1)//8-n+1)*8)
                elif code == 'L':
                    grid[y:y] = [[' '] * self.columns for _ in range(min(n, self.rows-y))]
                    del grid[self.rows:]
                elif code == 'M':
                    del grid[y:min(self.rows, y+n)]
                    grid.extend([[' '] * self.columns for _ in range(self.rows-len(grid))])
                elif code == 'd': y = n-1
                elif code == 'J' and parts[0] in (2, 3):
                    grid = [[' '] * self.columns for _ in range(self.rows)]
                elif code == 'K' and 0 <= y < self.rows:
                    start, end = (0, self.columns) if parts[0] == 2 else ((0, x+1) if parts[0] == 1 else (x, self.columns))
                    grid[y][start:end] = [' '] * (end-start)
                elif code == 'X' and 0 <= y < self.rows:
                    grid[y][x:min(self.columns, x+n)] = [' '] * min(n, self.columns-x)
            elif token == '\x1bM':
                if y == top:
                    grid.insert(top, [' '] * self.columns)
                    del grid[bottom+1]
                else:
                    y = max(0, y-1)
            elif token.startswith('\x1b'):
                continue
            elif token == '\r': x = 0
            elif token == '\n':
                if y == bottom:
                    del grid[top]
                    grid.insert(bottom, [' '] * self.columns)
                else:
                    y = min(self.rows-1, y+1)
            elif token == '\b': x = max(0, x-1)
            elif token == '\t': x = min(self.columns-1, (x//8+1)*8)
            elif ord(token) >= 32 and not unicodedata.combining(token):
                width = 2 if unicodedata.east_asian_width(token) in ('W', 'F') else 1
                if 0 <= y < self.rows and 0 <= x < self.columns:
                    grid[y][x] = token
                    if width == 2 and x+1 < self.columns:
                        grid[y][x+1] = ''
                x += width
        lines = [''.join(row) for row in grid]
        (self.path.with_suffix('.screen.txt')).write_text('\n'.join(lines)+'\n')
        return lines

    def click_label(self, label):
        for y, line in enumerate(self.screen()[:-1]):  # Exclude the focus-status echo.
            at = line.find(label)
            if at >= 0:
                x = sum(2 if unicodedata.east_asian_width(c) in ('W', 'F') else
                        0 if unicodedata.combining(c) else 1 for c in line[:at])
                self.click(x, y)
                return
        raise AssertionError(f'{self.identity}: visible control {label!r} missing')

    def command(self, index):
        self.key(4)
        self.send(b'\x1b[B' * index + b'\r', .6)

    def close(self):
        try:
            self.send(b'\x11', .5)
            self.send(b'exit\n', .2)
        finally:
            os.close(self.fd)
            os.waitpid(self.pid, 0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path)
    args = parser.parse_args()
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix='tui-pty-evidence-', dir='/tmp'))
    artifacts.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    report = {'harness': 'Python OS PTY + bash job control', 'artifacts': str(artifacts), 'checks': []}

    def check(condition, message):
        if not condition:
            raise AssertionError(message)
        report['checks'].append(message)

    with tempfile.TemporaryDirectory(prefix='tui-pty-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = dict(os.environ, TUI_GO_HOME=directory)
        terminals = []

        def cli(*words):
            return subprocess.check_output([str(binary), *words], env=env, text=True)

        def get(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                headers={'Authorization': 'Bearer '+discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(request, timeout=3) as response:
                return json.load(response)

        def view(identity):
            return get('views/' + identity)['data']

        try:
            a = Terminal(binary, home, 'pty-a', artifacts)
            terminals.append(a)
            check((home / 'discovery.json').exists(), 'TUI automatically starts server')
            check(b'\x1b[?1049h' in a.output, 'alternate screen entered')
            a.send(b'client A draft')
            a.key(2)
            a.key(3)
            a.key(5)
            a.key(8)
            a.key(6)
            a.command(22)  # Open Files, following fixed commands.
            a.key(7)
            a.pump(1.2)
            av = view('pty-a')
            check(not av['Layout']['Left'] and av['Layout']['Bottom'] and av['Light'], 'F2/F5/F8 update persisted layout/theme')
            check(av['Layout']['Maximized'], 'F4 menu opens Files and F7 maximizes it')
            check(av['Threads'][av['Active']]['Draft'] == 'client A draft', 'draft survives focus and surface changes')
            a.key(7)
            a.click(2, 0)
            a.pump(1.1)
            check(view('pty-a')['Layout']['Left'], 'SGR pointer click activates navigation toggle')
            a.key(4)
            # Use keyboard to select theme and pointer activate its selected row.
            a.send(b'\x1b[B' * 4)
            # Locate the actual control; project/lifecycle entries change the
            # menu height without changing this action's behavior.
            a.click_label('Switch dark / light theme')
            a.pump(1.1)
            check(not view('pty-a')['Light'], 'SGR pointer activates command menu item')
            a.send(b'\x1b[<0;118;11M\x1b[<32;113;11M\x1b[<0;113;11m', 1.2)
            check(view('pty-a')['Layout']['RightWidth'] == 47, 'SGR divider drag updates remembered right width')
            a.command(24)  # Open a synthetic Terminal, not a real child shell.
            a.pump(1.1)
            terminals_before = get('snapshot')['terminals']
            terminal_id = view('pty-a')['Threads'][av['Active']]['Host']['ActiveID']
            check(any(t['ID'] == terminal_id and t['State'] == 'running' for t in terminals_before), 'Terminal menu opens server-owned fixture session')
            a.key(3)
            check(get('snapshot')['terminals'] == terminals_before, 'F3 hides terminal without closing its session')
            a.key(3)
            # One row per opened surface; Delete closes the selected row.
            a.key(4)
            a.send(b'\x1b[B' * (28 + len(get('snapshot')['threads']) + 1) + b'\x1b[3~', .6)
            a.pump(1.1)
            check(any(t['ID'] == terminal_id and t['State'] == 'ended' for t in get('snapshot')['terminals']), 'Terminal close command ends selected fixture session')
            before = view('pty-a')['Layout']
            a.resize(48, 24)
            a.pump(.5)
            narrow = a.screen()
            check(any('Reference' in row and '\uf0aa' in row for row in narrow),
                  'narrow composer keeps model and Send on the same row')
            question_y, question_row = next((i, row) for i, row in enumerate(narrow) if '▶' in row)
            a.click(question_row.index('\ueb10'), question_y)
            check('QUESTIONS' in '\n'.join(a.screen()) and '3 Notes' in '\n'.join(a.screen()),
                  'question overflow opens direct access to hidden pages in the narrow header')
            a.send(b'\x1b')
            footer_y, footer_row = next((i, row) for i, row in enumerate(a.screen()) if '\uf0aa' in row)
            a.click(footer_row.index('\ueb10'), footer_y)  # Scope to settings, not question overflow.
            check('MORE SETTINGS' in '\n'.join(a.screen()) and any(re.search(r'Effort\s{2,}Medium', row) for row in a.screen()),
                  'SGR pointer opens hidden composer settings from vertical ellipsis')
            (artifacts / 'composer-overflow.screen.txt').write_text('\n'.join(a.screen())+'\n')
            a.resize(160, 50)
            a.pump(.5)
            a.send(b'\r')  # First overflow setting remains effort across resize.
            check('SETTINGS · EFFORT' in '\n'.join(a.screen()),
                  'Enter routes the stable overflow selection to its settings picker after resize')
            a.send(b'\x1b')
            a.pump(1.1)
            check(view('pty-a')['Threads'][av['Active']]['Draft'] == 'client A draft',
                  'composer overflow and resize preserve the unsent prompt')
            a.resize(48, 24)
            a.pump(.5)
            screen = a.screen()
            row_index, row = next((i, line) for i, line in enumerate(screen) if '\uf0aa' in line)
            check('╰' in screen[row_index+1] and '╯' in screen[row_index+1] and '│' in row,
                  'one prompt outline encloses typing and composer controls')
            a.click(row.index('Ctx') if 'Ctx' in row else row.rfind('\ueb10'), row_index)  # Independent usage control.
            usage_screen = '\n'.join(a.screen())
            check(re.search(r'Context percentage\s{2,}unavailable', usage_screen) and re.search(r'API cost\s{2,}unavailable', usage_screen)
                  and not re.search(r'Effort(:|\s{2,})\s*Medium', usage_screen),
                  'separate usage ellipsis opens context percentage and cost without settings')
            (artifacts / 'composer-usage.screen.txt').write_text(usage_screen+'\n')
            a.send(b'\x1b')
            a.resize(60, 22)
            a.pump(.5)
            a.resize(35, 12)
            a.pump(.5)
            a.resize(160, 50)
            a.pump(1.1)
            check(view('pty-a')['Layout'] == before, 'wide/narrow/tiny/wide resize preserves layout preferences')
            a.send(b'\x1a', .6)
            check(b'Stopped' in a.output, 'Ctrl+Z stops TUI and returns to job-control shell')
            a.send(b'fg\n', .8)
            a.key(8)
            a.pump(1.1)
            check(view('pty-a')['Light'], 'resumed TUI accepts input and persists changes')
            b = Terminal(binary, home, 'pty-b', artifacts)
            terminals.append(b)
            b.send(b'client B draft', 1.2)
            bv = view('pty-b')
            check(bv['Layout']['Left'] and not bv['Light'] and bv['Threads'][bv['Active']]['Draft'] == 'client B draft', 'second client has independent view and draft')
            check(view('pty-a')['Threads'][av['Active']]['Draft'] == 'client A draft', 'second client does not overwrite first client draft')
            c = Terminal(binary, home, 'pty-send', artifacts)
            terminals.append(c)
            c.send(b'first\x1b[13;2usecond\nthird', 1.2)
            cv = view('pty-send')
            check(cv['Threads'][cv['Active']]['Draft'] == 'first\nsecond\nthird', 'Shift+Enter CSI-u and Ctrl+J insert newlines without submitting')
            c.send(b'\r', 1.2)
            cv = view('pty-send')
            thread = next(t for t in get('snapshot')['threads'] if t['ID'] == cv['Active'])
            captured = any(item['Text'] == 'first\nsecond\nthird' for item in (thread['Queue'] or []) + (thread['Activity'] or []))
            check(cv['Threads'][cv['Active']]['Draft'] == '' and captured, 'Enter sends the exact multiline prompt and clears it after acceptance')
            c.send(b'\x11', .6)
            a.send(b'\x11', .8)
            check(b'\x1b[?1049l' in a.output and b'\x1b[?1006l' in a.output, 'detach restores alternate screen and disables SGR mouse')
            old = get('snapshot')
            b.pump(1.3)
            new = get('snapshot')
            check(new['revision'] > old['revision'], 'server execution advances after first TUI detaches')
            q = Terminal(binary, home, 'pty-questions', artifacts)
            terminals.append(q)
            q.send(b'question-safe prompt draft', 1.2)

            def question_state():
                state = view('pty-questions')
                return state['Threads'][state['Active']]

            def question_request():
                return next(r for t in get('snapshot')['threads'] if t['ID'] == 'thread-shell'
                            for r in t['Requests'] if r['ID'] == 'question-async')

            q.click_label('Submit')
            check(question_request()['State'] == 'pending'
                  and 'Question 1: answer is required' in '\n'.join(q.screen()),
                  'incomplete Submit shows validation in the card without sending')
            q.click_label('Keyboard flow')
            q.pump(1.2)
            check(question_state()['QuestionIndex'] == 1 and question_request()['State'] == 'pending',
                  'single radio choice advances one question without submitting')
            q.click_label('Clarity')
            q.click_label('Density')
            q.send(b' ')  # Toggle the focused checkbox off using its keyboard path.
            q.pump(1.2)
            drafts = list(question_state()['QuestionDrafts'].values())
            check(question_state()['QuestionIndex'] == 1 and drafts[0][1]['Choices'] == ['Clarity']
                  and question_request()['State'] == 'pending',
                  'checkbox pointer and Space toggle selection without advancing or submitting')
            q.click_label('▶')
            q.pump(1.2)
            check(question_state()['QuestionIndex'] == 2 and question_request()['State'] == 'pending',
                  'filled Next arrow opens optional text question without submitting')
            # Next only navigates and keeps focus on the arrow (or the active tab once
            # the arrow disappears); Down reaches the open-ended answer field.
            q.send(b'\x1b[B', .4)
            q.send(b'Native PTY review note', 1.2)
            # End arrows disappear without letting tabs fill their reserved slots.
            last_screen = q.screen()
            last_row_index, last_row = next((i, row) for i, row in enumerate(last_screen) if '3 Notes' in row and '◀' in row)
            back_x = last_row.index('◀')
            check('▶' not in last_row and '1 Focus' in last_row and '2 Priorities' in last_row,
                  'single-row question tabs preserve navigation and hide only the Next arrow')
            q.click(back_x, last_row_index)
            q.pump(1.2)
            check(question_state()['QuestionIndex'] == 1 and question_request()['State'] == 'pending',
                  'filled Back arrow revisits the previous question without submitting')
            q.click_label('1 Focus')
            q.pump(1.2)
            check(question_state()['QuestionIndex'] == 0 and question_request()['State'] == 'pending',
                  'top question tab revisits an answer without submitting')
            (artifacts / 'question-review.screen.txt').write_text('\n'.join(q.screen())+'\n')
            q.click_label('Submit')
            q.pump(1.2)
            answered = question_request()
            (artifacts / 'question-accepted.json').write_text(json.dumps(answered, indent=2)+'\n')
            check(answered['State'] == 'resolved'
                  and answered['QuestionAnswers'] == [
                      {'Choices': ['Keyboard flow'], 'Text': ''},
                      {'Choices': ['Clarity'], 'Text': ''},
                      {'Choices': None, 'Text': 'Native PTY review note'}],
                  'explicit Submit delivers the structured radio, checkbox and text answers together')
            check(question_state()['Draft'] == 'question-safe prompt draft',
                  'question navigation and Submit preserve the ordinary prompt draft')
            q.screen()
            q.send(b'\x11', .6)
            b.send(b'\x11', .8)
            cli('server', 'stop')
            cli('server', 'start')
            restarted = get('snapshot')
            time.sleep(1.2)
            stable = get('snapshot')
            # Startup provider probes may advance the global revision without
            # resuming fixture work. Compare the execution state itself.
            check(all(t['NeedsResume'] or t['State'] == 'idle' for t in restarted['threads']) and restarted['threads'] == stable['threads'], 'server restart preserves state and waits for explicit Resume of unfinished work')
            a.send((shlex.quote(str(binary)) + ' --client pty-a\n').encode(), 1.5)
            a.command(6)
            a.pump(1.2)
            resumed = get('snapshot')
            active = next(t for t in resumed['threads'] if t['ID'] == av['Active'])
            check(not active['NeedsResume'] and active['State'] in ('running', 'idle'), 'explicit Resume menu action continues selected fixture work through completion')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'] = 'FAIL'
            report['error'] = str(error)
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            subprocess.run([str(binary), 'server', 'stop'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
