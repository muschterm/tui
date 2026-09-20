#!/usr/bin/env python3
"""Isolated navigation OS-PTY review; not a GUI terminal compatibility claim."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import urllib.request
from pty_smoke import Terminal


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, default=Path('/tmp/tui-navigation-pty'))
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'OS PTY; SGR pointer and keyboard; isolated server home', 'checks': []}
    terminals = []

    def check(ok, label):
        if not ok:
            raise AssertionError(label)
        report['checks'].append(label)

    def capture(term, name):
        (artifacts / (name + '.screen.txt')).write_text('\n'.join(term.screen()) + '\n')

    def paste(term, text):
        term.send(b'\x1b[200~' + text.encode() + b'\x1b[201~', 1.1)

    def thread_row(term):
        for y, line in enumerate(term.screen()):
            if y >= 6 and 'New thread' in line[:24]:
                return y
        raise AssertionError('New thread navigation row missing')

    with tempfile.TemporaryDirectory(prefix='tui-navigation-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = dict(os.environ, TUI_GO_HOME=directory)
        project = home / 'Navigation project'
        project.mkdir()
        marker = project / 'keep.txt'
        marker.write_text('preserved file\n')

        def get(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            req = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(req, timeout=3) as response:
                return json.load(response)

        def view(who):
            return get('views/' + who)['data']

        def snapshot():
            return get('snapshot')

        try:
            subprocess.run([str(binary), "server", "start"], env=env, check=True, capture_output=True, text=True)
            a = Terminal(binary, home, 'navigation-a', artifacts)
            terminals.append(a)
            a.key(2)
            a.key(2)
            a.pump(1.2)
            original = view('navigation-a')['Active']
            # Default left pane is 24 cells: its visible folder-plus is at x=19.
            a.click(19, 3)
            paste(a, str(marker))
            a.click_label('Add folder as project')
            a.pump(1.1)
            capture(a, '01-invalid-folder')
            screen = '\n'.join(a.screen())
            check('existing directory' in screen and str(marker) in screen,
                  'regular-file rejection retains folder input and visible error')
            a.send(b'\x1b')
            a.click(19, 3)
            paste(a, str(project))
            a.send(b'\r', 1.2)
            projects = snapshot()['projects']
            selected = next(p for p in projects if p['Name'] == 'Navigation project')
            check(view('navigation-a')['ProjectFilter'] == selected['ID'] and view('navigation-a')['Active'] == original,
                  'adding folder selects project filter without changing active thread')
            check(not any(t.get('ProjectID') == selected['ID'] for t in snapshot()['threads']),
                  'adding project creates no thread or execution')
            a.click(3, 3)
            paste(a, str(project))
            capture(a, '02-project-path-search')
            check('Navigation project' in '\n'.join(a.screen()), 'project picker searches server paths')
            a.send(b'\r', .8)
            a.click_label('New thread')
            a.pump(1.2)
            current = view('navigation-a')['Active']
            thread = next(t for t in snapshot()['threads'] if t['ID'] == current)
            check(thread['State'] == 'idle' and thread['Checkout'] == str(project.resolve())
                  and not any(thread.get(k) for k in ['Activity', 'Queue', 'Requests', 'Children', 'Plan']),
                  'New thread creates empty idle Demo thread in selected server folder')
            # Creation focuses the prompt. Input is a bracketed paste, never Send.
            paste(a, 'Preserved navigation draft')
            check(view('navigation-a')['Threads'][current]['Draft'] == 'Preserved navigation draft',
                  'bracketed paste persists unsent thread draft')
            b = Terminal(binary, home, 'navigation-b', artifacts)
            terminals.append(b)
            b.key(2)
            b.key(2)
            b.pump(1.2)
            check(view('navigation-b').get('ProjectFilter', '') == '', 'second client project filter stays independent')
            b.click(4, thread_row(b))
            paste(b, 'Second client draft')
            check(view('navigation-b')['Active'] == current, 'second client observes same thread with local draft')
            a.click(3, 3)
            paste(a, 'Navigation')
            capture(a, '03-project-name-search')
            check('Navigation project' in '\n'.join(a.screen()), 'project picker searches names')
            a.send(b'\r', .8)
            check(view('navigation-a')['Active'] == current, 'filtering leaves center thread selected')
            row = thread_row(a)
            a.send(f'\x1b[<35;5;{row+1}M'.encode())
            capture(a, '04-close-hover')
            a.click(2, row)
            a.pump(1.2)
            check(next(t for t in snapshot()['threads'] if t['ID'] == current).get('Closed', False),
                  'hover check closes idle thread without submitting its draft')
            a.click(4, thread_row(a))
            a.pump(1.2)
            check(not next(t for t in snapshot()['threads'] if t['ID'] == current).get('Closed', False)
                  and view('navigation-a')['Threads'][current]['Draft'] == 'Preserved navigation draft',
                  'Closed row reopens thread and restores unsent draft')
            row = thread_row(a)
            a.click(2, row)
            a.pump(.8)
            row = thread_row(a)
            a.send(f'\x1b[<35;5;{row+1}M'.encode())
            a.click(2, row)
            capture(a, '05-delete-confirm')
            a.send(b'\r', .8)
            check(any(t['ID'] == current for t in snapshot()['threads']), 'default Enter cancels permanent deletion')
            a.click(2, thread_row(a))
            a.click_label('Delete permanently')
            a.pump(1.3)
            b.pump(1.3)
            check(not any(t['ID'] == current for t in snapshot()['threads']), 'confirmed delete removes authoritative thread')
            with sqlite3.connect(f'file:{home / "state.sqlite"}?mode=ro', uri=True) as db:
                state = json.loads(db.execute('SELECT data FROM state WHERE id=1').fetchone()[0])
                views = [json.loads(row[0]) for row in db.execute('SELECT data FROM views')]
            check(not any(t['ID'] == current for t in state['threads'])
                  and all(current not in v.get('Threads', {}) for v in views),
                  'live SQLite snapshot and all saved client views remove deleted thread')
            b.key(8)
            b.pump(1.2)
            capture(b, '06-other-client-after-delete')
            check(view('navigation-b')['Light'] and current not in view('navigation-b').get('Threads', {}),
                  'other client remains usable and saves view after deletion')
            check('Draft/layout save failed' not in '\n'.join(b.screen()), 'other client shows no view CAS/save failure')
            check(marker.read_text() == 'preserved file\n' and list(project.iterdir()) == [marker],
                  'project addition and thread lifecycle preserve project files')
            capture(a, '07-deleted-empty-project')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            for terminal in terminals:
                capture(terminal, terminal.identity + '-failure')
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            subprocess.run([str(binary), 'server', 'stop'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
