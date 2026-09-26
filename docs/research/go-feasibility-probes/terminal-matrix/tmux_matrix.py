#!/usr/bin/env python3
"""tmux compatibility probe for the Go reference TUI (evidence, not app code).

Runs the real binary inside a PRIVATE tmux server (`tmux -L <socket> -f
/dev/null`), attaches one tmux client through an OS PTY that plays the outer
terminal (TERM=xterm-256color), and injects keyboard, paste, focus and SGR
mouse bytes into that client so they pass through tmux's own input parser and
forwarding. The application's output stream is recorded with `pipe-pane -O`
and the rendered grid with `capture-pane -e`.

Isolation follows apps/go/scripts/harness_env.py: temporary TUI_GO_HOME and
HOME, HISTFILE=/dev/null, no ENV/BASH_ENV, Git config pinned to a temporary
file, and Claude/Codex executables pointed at a path that cannot exist. It
never touches the developer's tmux server, home, shell history or Git config.

Usage: tmux_matrix.py --binary PATH --artifacts DIR [--only NAME ...]
Each scenario prints PASS/FAIL/OBSERVED lines and writes report.json.
"""
import argparse
import fcntl
import json
import os
import pty
import re
import shlex
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import unicodedata
import urllib.request
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'apps/go/scripts'))
from harness_env import isolated_env, user_home  # noqa: E402

F = {3: b'\x1bOR', 5: b'\x1b[15~', 7: b'\x1b[18~'}
RESULTS = []


def record(scenario, name, status, detail=''):
    RESULTS.append(dict(scenario=scenario, check=name, status=status, detail=detail))
    print(f'[{status}] {scenario}: {name}' + (f' -- {detail}' if detail else ''), flush=True)


def cells(text):
    return sum(2 if unicodedata.east_asian_width(c) in ('W', 'F') else
               0 if unicodedata.combining(c) else 1 for c in text)


class Probe:
    """One isolated app home, one private tmux server, one attached client."""

    def __init__(self, binary, artifacts, name, pane_env=None, tmux_opts=(), outer_term='xterm-256color',
                 columns=160, rows=45):
        self.name, self.binary, self.artifacts = name, binary, artifacts
        self.dir = Path(tempfile.mkdtemp(prefix='tui-tmux-', dir='/tmp'))
        self.socket = 'tuiprobe-' + uuid.uuid4().hex[:8]
        gitconfig = user_home(self.dir) / '.gitconfig'
        gitconfig.write_text('[user]\n\tname = Harness\n\temail = harness@example.invalid\n[init]\n\tdefaultBranch = main\n')
        self.env = isolated_env(self.dir, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=str(gitconfig),
                                XDG_CONFIG_HOME=str(self.dir / 'xdg'), SHELL='/bin/bash', PS1='term$ ',
                                COLORTERM=None, NO_COLOR=None, TMUX=None, TMUX_PANE=None)
        for k in [k for k in self.env if k.startswith('GIT_') and k not in ('GIT_CONFIG_NOSYSTEM', 'GIT_CONFIG_GLOBAL')]:
            del self.env[k]
        for k, v in (pane_env or {}).items():
            if v is None:
                self.env.pop(k, None)
            else:
                self.env[k] = v
        self.stream = self.dir / 'pane-output.bin'
        # The wrapper records the exit status so terminal-state checks can run
        # after the TUI returns, while the pane stays alive.
        wrapper = self.dir / 'run.sh'
        exports = ''.join(f'export {k}={shlex.quote(v)}\n' for k, v in self.env.items()
                          if re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', k) and k not in ('UNSET_TMUX', 'TMUX', 'TMUX_PANE'))
        # tmux itself sets TMUX/TMUX_PANE in the pane; keep them unless a
        # scenario explicitly removes them.
        unsets = ' '.join(k for k in ('COLORTERM', 'NO_COLOR', 'ENV', 'BASH_ENV') if k not in self.env)
        if 'UNSET_TMUX' in (pane_env or {}):
            unsets += ' TMUX TMUX_PANE'
        wrapper.write_text('#!/bin/bash\n' + exports + (f'unset {unsets}\n' if unsets else '') +
                           'sleep 1\n"$@"\necho "APP_EXIT=$?"\nexec sleep 3600\n')
        wrapper.chmod(0o755)
        self.tmux('new-session', '-d', '-s', 'boot', '-c', str(self.dir), '-x', '80', '-y', '24', 'sleep 3600')
        for opt in tmux_opts:
            self.tmux(*opt)
        cmd = f'{shlex.quote(str(wrapper))} {shlex.quote(str(binary))}'
        self.tmux('new-session', '-d', '-s', 'm', '-c', str(self.dir), '-x', str(columns), '-y', str(rows), cmd)
        self.tmux('pipe-pane', '-O', '-t', 'm', 'cat >> ' + shlex.quote(str(self.stream)))
        self.tmux('set-option', '-t', 'm', 'window-size', 'manual')
        self.tmux('resize-window', '-t', 'm', '-x', str(columns), '-y', str(rows))
        self.client_out = bytearray()
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.environ.clear()
            os.environ.update(dict(self.env, TERM=outer_term))
            os.environ.pop('TMUX', None)
            os.execvp('tmux', ['tmux', '-L', self.socket, '-f', '/dev/null', 'attach', '-t', 'm'])
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, columns, 0, 0))
        threading.Thread(target=self._drain, daemon=True).start()
        self.wait_for(lambda: 'Ask to do anything' in self.text() or 'Search' in self.text(), 'TUI first frame', 15)

    def _drain(self):
        while True:
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                return
            if not data:
                return
            self.client_out += data

    def tmux(self, *words, check=True):
        return subprocess.run(['tmux', '-L', self.socket, '-f', '/dev/null', *words], env=self.env,
                              capture_output=True, text=True, check=check).stdout

    def fmt(self, f):
        return self.tmux('display-message', '-p', '-t', 'm', f).strip()

    def text(self):
        return self.tmux('capture-pane', '-p', '-t', 'm')

    def ansi(self):
        return self.tmux('capture-pane', '-p', '-e', '-t', 'm')

    def lines(self):
        return self.text().split('\n')

    def out(self):
        try:
            return self.stream.read_bytes()
        except FileNotFoundError:
            return b''

    def send(self, data, delay=.4):
        os.write(self.fd, data)
        time.sleep(delay)

    def wait_for(self, cond, what, seconds=10):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if cond():
                return True
            time.sleep(.2)
        self.save('timeout-' + re.sub(r'\W+', '-', what))
        raise AssertionError('timed out: ' + what)

    def find(self, label, last=False):
        hits = []
        for y, line in enumerate(self.lines()):
            at = line.find(label)
            if at >= 0:
                hits.append((cells(line[:at]), y))
        if not hits:
            raise AssertionError('not on screen: ' + label)
        return hits[-1] if last else hits[0]

    def click(self, x, y, button=0):
        self.send(f'\x1b[<{button};{x+1};{y+1}M\x1b[<{button};{x+1};{y+1}m'.encode(), .6)

    def click_label(self, label, last=False):
        x, y = self.find(label, last)
        self.click(x, y)

    def save(self, tag):
        base = self.artifacts / f'{self.name}-{tag}'
        (base.with_suffix('.txt')).write_text(self.text())
        (base.with_suffix('.ansi')).write_text(self.ansi())

    def api(self, path, data=None):
        discovery = json.loads((self.dir / 'discovery.json').read_text())
        request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
            data=None if data is None else json.dumps(data).encode(),
            headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1',
                     'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response)

    def cli(self, *words):
        return subprocess.run([str(self.binary), *words], env=self.env, capture_output=True, text=True, timeout=30)

    def close(self):
        (self.artifacts / f'{self.name}-pane-output.bin').write_bytes(self.out())
        try:
            self.cli('server', 'stop')
        except Exception:
            pass
        self.tmux('kill-server', check=False)
        subprocess.run(['rm', '-f', f'/tmp/tmux-{os.getuid()}/{self.socket}'])
        try:
            os.kill(self.pid, 9)
            os.waitpid(self.pid, 0)
        except OSError:
            pass
        subprocess.run(['rm', '-rf', str(self.dir)])


def composer_rows(p):
    """Text inside the prompt outline, above its settings/actions row."""
    ls = p.lines()
    bottom = max(i for i, l in enumerate(ls) if 'Ctx' in l)
    top = max(i for i in range(bottom) if '╭' in ls[i])
    left = ls[top].index('╭')
    right = ls[top].index('╮', left)
    return [l[left + 1:right].strip() for l in ls[top + 1:bottom]]


def clear_draft(p, n):
    # Exactly n: a Backspace on an EMPTY composer misplaces the cursor (see
    # the research note's defect list), so never over-delete here.
    p.send(b'\x7f' * n, .6)


# ---------------------------------------------------------------- scenarios

def lifecycle(p):
    s = 'lifecycle'
    out = p.out()
    flags = {k: p.fmt('#{' + k + '}') for k in ('alternate_on', 'mouse_any_flag', 'mouse_sgr_flag', 'cursor_flag', 'pane_title')}
    record(s, 'alternate screen active while running', 'PASS' if flags['alternate_on'] == '1' else 'FAIL', str(flags))
    record(s, 'any-motion SGR mouse requested', 'PASS' if flags['mouse_any_flag'] == '1' and flags['mouse_sgr_flag'] == '1' else 'FAIL')
    requested = {name: seq in out for name, seq in (
        ('bracketed paste ?2004h', b'\x1b[?2004h'), ('focus ?1004h', b'\x1b[?1004h'),
        ('modifyOtherKeys >4;1m or >4;2m', b'\x1b[>4;'), ('kitty keyboard push >Nu / query ?u', b'\x1b[>'),
        ('XTVERSION query >q', b'\x1b[>q'), ('kitty graphics APC _G', b'\x1b_G'))}
    record(s, 'sequences requested on startup', 'OBSERVED', json.dumps(requested))
    p.save('startup')
    p.send(b'\x11', 1.5)  # Ctrl+Q detaches the TUI
    p.wait_for(lambda: 'APP_EXIT=' in p.text(), 'app exit', 10)
    code = re.search(r'APP_EXIT=(\d+)', p.text()).group(1)
    after = {k: p.fmt('#{' + k + '}') for k in ('alternate_on', 'mouse_any_flag', 'mouse_sgr_flag', 'mouse_button_flag',
                                                  'mouse_standard_flag', 'cursor_flag', 'keypad_cursor_flag', 'keypad_flag')}
    ok = code == '0' and after['alternate_on'] == '0' and after['cursor_flag'] == '1' and \
        all(after[k] == '0' for k in after if k.startswith('mouse'))
    record(s, 'Ctrl+Q exits 0 and restores main screen, cursor and mouse modes', 'PASS' if ok else 'FAIL',
           f'exit={code} {after}')
    out = p.out()
    tail = out[out.rfind(b'\x1b[?1049l') if b'\x1b[?1049l' in out else len(out):]
    record(s, 'bracketed paste, focus and keyboard enhancements disabled at exit',
           'PASS' if all(q in out for q in (b'\x1b[?2004l', b'\x1b[?1004l', b'\x1b[>4m', b'\x1b[<1u')) else 'FAIL',
           f'?2004l={b"?2004l" in out} ?1004l={b"?1004l" in out}; bytes after 1049l={tail[:80]!r}')
    p.save('after-exit')
    status = p.cli('server', 'status')
    record(s, 'server keeps running after TUI exit', 'PASS' if status.returncode == 0 else 'FAIL', status.stdout.strip()[:120])


def resize(p):
    s = 'resize'
    before = p.text()
    for w, h in ((60, 22), (35, 12), (100, 30), (160, 45)):
        p.tmux('resize-window', '-t', 'm', '-x', str(w), '-y', str(h))
        time.sleep(1.2)
        ls = p.lines()
        width_ok = all(cells(l) <= w for l in ls)
        p.save(f'{w}x{h}')
        marker = {(60, 22): 'Build the work', (35, 12): '', (100, 30): 'Ask to do anything', (160, 45): 'Search'}[(w, h)]
        # Record what is shown; the minimum-size notice or hamburger layout is expected below the wide width.
        record(s, f'{w}x{h} relayout', 'PASS' if width_ok and len(ls) >= h - 1 and (not marker or marker in p.text()) else 'FAIL',
               'first rows: ' + ' | '.join(l.strip()[:50] for l in ls[:2]))
    after = p.text()
    record(s, 'wide layout restored after shrinking', 'PASS' if 'Search' in after and 'Ask to do anything' in after else 'FAIL')
    _ = before


def keyboard(p):
    s = 'keyboard:' + p.name
    p.click_label('Ask to do anything')
    variants = (('Shift+Enter as CSI u (13;2u)', b'\x1b[13;2u'),
                ('Shift+Enter as xterm modifyOtherKeys (27;2;13~)', b'\x1b[27;2;13~'),
                ('Ctrl+J (LF)', b'\n'))
    for label, seq in variants:
        p.send(b'alpha', .3)
        p.send(seq, .4)
        p.send(b'omega', .8)
        rows = [r for r in composer_rows(p) if r]
        if rows == ['alpha', 'omega']:
            record(s, label + ' inserts a newline without sending', 'PASS')
            clear_draft(p, 11)
        else:
            sent = not any('alpha' in r for r in rows)
            record(s, label + ' inserts a newline without sending', 'FAIL',
                   ('prompt was SENT; draft now ' + repr(rows) if sent else 'draft rows=' + repr(rows)))
            p.save('newline-' + re.sub(r'\W+', '-', label)[:30])
            clear_draft(p, sum(len(r) for r in rows) + max(0, len(rows) - 1))
    p.send(b'\x1b[I', .3)
    p.send(b'\x1b[O', .3)
    p.send(b'\x1b[I', .3)
    p.send(b'xyz', .6)
    rows = [r for r in composer_rows(p) if r]
    record(s, 'focus in/out reports do not leak into the draft', 'PASS' if rows == ['xyz'] else 'FAIL', repr(rows))
    clear_draft(p, 3)
    for flag, label in (('', 'paste-buffer -p (LF sent as CR, tmux default)'), ('-r', 'paste-buffer -p -r (LF kept)')):
        p.tmux('set-buffer', '-b', 'probe', 'first line\nsecond line')
        p.tmux('paste-buffer', '-p', *([flag] if flag else []), '-b', 'probe', '-t', 'm')
        time.sleep(1)
        rows = [r for r in composer_rows(p) if r]
        record(s, f'bracketed {label}: two draft lines, unsent',
               'PASS' if rows == ['first line', 'second line'] else 'FAIL', repr(rows))
        p.save('paste' + flag)
        clear_draft(p, sum(len(r) for r in rows) + len(rows) - 1)


def mouse(p):
    s = 'mouse'
    for setting in ('on', 'off'):
        p.tmux('set-option', '-g', 'mouse', setting)
        time.sleep(.3)
        title_before = p.lines()[0]
        target = 'Review key' if 'Build the workspace' in title_before else 'Build the'
        p.click_label(target)
        time.sleep(1)
        title_after = p.lines()[0]
        changed = title_after != title_before
        record(s, f'tmux mouse {setting}: left click on a thread card selects it', 'PASS' if changed else 'FAIL',
               f'breadcrumb {title_before.strip()[:60]!r} -> {title_after.strip()[:60]!r}')
        x, y = p.find('Thinking') if 'Thinking' in p.text() else (80, 10)
        for _ in range(3):
            p.send(f'\x1b[<64;{x+1};{y+1}M'.encode(), .2)
        in_mode = p.fmt('#{pane_in_mode}')
        record(s, f'tmux mouse {setting}: wheel over the transcript goes to the app, not copy-mode',
               'PASS' if in_mode == '0' else 'FAIL', f'pane_in_mode={in_mode}')
        if in_mode == '1':
            p.tmux('send-keys', '-t', 'm', '-X', 'cancel')


def colors(p, expect):
    s = 'colors:' + p.name
    time.sleep(2.5)  # colour queries have a one-second bound
    out = p.out()
    rgb = len(re.findall(rb'\x1b\[[0-9;:]*[34]8[;:]2[;:]', out))
    idx = len(re.findall(rb'\x1b\[[0-9;:]*[34]8[;:]5[;:]', out))
    basic = len(re.findall(rb'\x1b\[(?:[0-9;]*;)?(?:3[0-7]|4[0-7]|9[0-7]|10[0-7])m', out))
    queried = {'XTVERSION': b'\x1b[>q' in out, 'XTGETTCAP': b'\x1bP+q' in out}
    got = 'truecolor' if rgb else '256' if idx else 'basic' if basic else 'none'
    record(s, f'emitted color depth (expect {expect})', 'PASS' if got == expect else 'FAIL',
           f'rgb={rgb} idx256={idx} basic={basic} queries={queried}')
    p.save('frame')


def graphics(p):
    s = 'graphics:' + p.name
    time.sleep(2)
    out = p.out()
    apc = out.count(b'\x1b_G')
    record(s, 'kitty graphics APC sequences emitted', 'OBSERVED', f'count={apc}; queries={out.count(b"a=q")}')
    return apc


def surfaces(p, repo):
    s = 'surfaces'
    # Embedded terminal in the bottom panel.
    p.send(F[5], 2)
    try:
        p.wait_for(lambda: 'term$' in p.text(), 'embedded shell prompt', 10)
        p.click_label('term$', last=True)
        p.send(b'echo hello-$((6*7)) $TERM\r', 1.5)
        ok = any(re.search(r'^\W*hello-42 ', l) for l in p.lines())
        term_line = next((l.strip() for l in p.lines() if 'hello-42 ' in l and 'echo' not in l), '')
        record(s, 'embedded bash in the bottom panel runs a command inside tmux', 'PASS' if ok else 'FAIL', term_line[:80])
        p.send(b'printf "\\e[31mred\\e[0m \\e[38;2;1;2;3mrgb\\e[0m\\n"\r', 1.2)
        p.save('terminal')
        p.send(b'\x1d', .6)  # Ctrl+] leaves the terminal
        p.send(b'zz', .6)
        p.save('after-ctrl-bracket')
        status = p.lines()[-2] if not p.lines()[-1].strip() else p.lines()[-1]
        leaked = any('zz' in l for l in p.lines())
        record(s, 'Ctrl+] (0x1d through tmux) leaves terminal input; later keys do not reach the shell',
               'PASS' if 'click or Enter to type' in p.text() and not leaked else 'FAIL', status.strip()[:80])
    except AssertionError as error:
        record(s, 'embedded bash in the bottom panel runs a command inside tmux', 'FAIL', str(error))
    p.send(F[5], .8)  # hide the panel (session kept)
    p.api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind='project.add', Path=str(repo)))
    time.sleep(1)
    p.click_label('tui-go')  # application title: new thread in the project chooser
    time.sleep(1)
    p.tmux('set-buffer', '-b', 'q', 'repo')
    p.tmux('paste-buffer', '-p', '-b', 'q', '-t', 'm')
    time.sleep(.5)
    p.send(b'\r', 1.2)
    p.save('draft')
    # Files editor: type and autosave.
    p.send(F[3], 1)
    try:
        p.wait_for(lambda: 'ADD SURFACE' in p.text(), 'surface chooser')
        p.click_label('Files')
        p.wait_for(lambda: 'notes.txt' in p.text(), 'Files tree')
        p.send(F[7], .8)
        p.click_label('notes.txt')
        p.wait_for(lambda: 'hello notes' in p.text(), 'editor open')
        p.click_label('hello notes')
        p.send(b'\x1b[F', .3)  # End
        p.send(' typed-in-tmux'.encode(), 3)
        disk = (repo / 'notes.txt').read_text()
        p.save('files')
        record(s, 'Files editor typing autosaves to disk through tmux', 'PASS' if 'typed-in-tmux' in disk else 'FAIL', repr(disk))
    except AssertionError as error:
        record(s, 'Files editor typing autosaves to disk through tmux', 'FAIL', str(error))
    # Git: stage and commit.
    git = lambda *w: subprocess.check_output(['git', *w], cwd=repo, env=p.env, text=True).strip()  # noqa: E731
    try:
        p.send(b'\x1b', .5)
        # The right host keeps Files; its tab row's add control (U+EA60) opens the chooser.
        x, y = p.find('\uea60', last=False)
        p.click(x, y)
        p.wait_for(lambda: 'ADD SURFACE' in p.text(), 'chooser for Git')
        p.click_label('Git', last=True)
        p.wait_for(lambda: 'RECENT COMMITS' in p.text(), 'Git surface', 10)
        p.wait_for(lambda: 'notes.txt' in p.text(), 'modified file listed', 10)
        p.click_label('notes.txt')
        p.wait_for(lambda: 'typed-in-tmux' in p.text(), 'diff viewer')
        p.send(b's', 1)
        staged = git('diff', '--cached', '--name-only')
        record(s, 'Git diff viewer `s` stages the file', 'PASS' if 'notes.txt' in staged else 'FAIL', staged)
        p.send(b'\x1b', .6)
        p.wait_for(lambda: 'Commit message' in p.text(), 'commit composer')
        p.click_label('Commit message')
        p.tmux('set-buffer', '-b', 'c', 'Commit from tmux')
        p.tmux('paste-buffer', '-p', '-b', 'c', '-t', 'm')
        time.sleep(.5)
        p.send(b'\r', 2)
        subject = git('log', '-1', '--format=%s')
        p.save('git')
        record(s, 'Git commit from the message editor', 'PASS' if subject == 'Commit from tmux' else 'FAIL', subject)
    except (AssertionError, subprocess.CalledProcessError) as error:
        p.save('git-fail')
        record(s, 'Git stage/commit flow', 'FAIL', str(error))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--only', nargs='*')
    args = parser.parse_args()
    binary, art = args.binary.resolve(), args.artifacts.resolve()
    art.mkdir(parents=True, exist_ok=True)
    want = lambda n: not args.only or n in args.only  # noqa: E731
    base_opts = [('set-option', '-g', 'default-terminal', 'tmux-256color'),
                 ('set-option', '-g', 'focus-events', 'on'),
                 ('set-option', '-g', 'extended-keys', 'on'),
                 ('set-option', '-g', 'mouse', 'on'),
                 ('set-option', '-g', 'escape-time', '10')]

    def run(name, fn, **kw):
        if not want(name):
            return
        p = None
        try:
            p = Probe(binary, art, name, **kw)
            fn(p)
        except Exception as error:  # record and continue the matrix
            record(name, 'scenario error', 'FAIL', repr(error))
            if p:
                p.save('error')
        finally:
            if p:
                p.close()

    run('lifecycle', lifecycle, tmux_opts=base_opts)
    run('resize', resize, tmux_opts=base_opts)
    run('keyboard', keyboard, tmux_opts=base_opts)
    run('keyboard-csiu', keyboard, tmux_opts=base_opts + [('set-option', '-g', 'extended-keys-format', 'csi-u')])
    run('keyboard-noext', keyboard, tmux_opts=[o for o in base_opts if o[2] != 'extended-keys'] +
        [('set-option', '-g', 'extended-keys', 'off')])
    run('mouse', mouse, tmux_opts=base_opts)
    run('color-256', lambda p: colors(p, '256'), tmux_opts=base_opts)
    run('color-rgb-feature', lambda p: colors(p, '256'),
        tmux_opts=base_opts + [('set-option', '-sa', 'terminal-features', ',xterm-256color:RGB')])
    run('color-colorterm', lambda p: colors(p, 'truecolor'), tmux_opts=base_opts, pane_env={'COLORTERM': 'truecolor'})
    run('color-nocolor', lambda p: colors(p, 'none'), tmux_opts=base_opts, pane_env={'NO_COLOR': '1'})
    run('graphics-tmux', graphics, tmux_opts=base_opts, )
    run('graphics-tmux-unset', graphics, tmux_opts=base_opts, pane_env={'UNSET_TMUX': '1'})

    if want('surfaces'):
        p = None
        try:
            p = Probe(binary, art, 'surfaces', tmux_opts=base_opts)
            repo = p.dir / 'repo'
            repo.mkdir()
            g = lambda *w: subprocess.check_output(['git', *w], cwd=repo, env=p.env, text=True)  # noqa: E731
            g('init', '-q', '-b', 'main')
            (repo / 'notes.txt').write_text('hello notes\n')
            g('add', '.')
            g('commit', '-qm', 'Initial')
            surfaces(p, repo)
        except Exception as error:
            record('surfaces', 'scenario error', 'FAIL', repr(error))
            if p:
                p.save('error')
        finally:
            if p:
                p.close()

    version = subprocess.run(['tmux', '-V'], capture_output=True, text=True).stdout.strip()
    (art / 'report.json').write_text(json.dumps({'tmux': version, 'results': RESULTS}, indent=1) + '\n')


if __name__ == '__main__':
    main()
