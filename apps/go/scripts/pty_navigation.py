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
            if y >= 5 and 'New thread' in line[:24]:
                return y
        raise AssertionError('New thread navigation row missing')

    def quick_action(term, row):
        # Both hover actions are centered in the slot immediately left of the
        # trailing ⋮ (open) or Reopen (closed) icon.
        line = term.screen()[row]
        menu_x = next(line.index(g) for g in ('\ueb10', '\U000f17b3') if g in line)
        quick_x = menu_x - 3
        term.send(f'\x1b[<35;{quick_x+1};{row+1}M'.encode())
        assert term.screen()[row][quick_x] in ('\ueab2', '\uea81'), 'quick icon missing beside menu'
        term.click(quick_x, row)

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
            # Default 24-cell sidebar has a single search/filter/add/new header.
            a.click(17, 2)
            paste(a, str(marker) + '/')
            a.pump(1.1)
            capture(a, '01-invalid-folder')
            screen = '\n'.join(a.screen())
            # Folder typeahead offers registration only for a listed directory.
            check('Add this folder' not in screen and marker.name in screen,
                  'regular file offers no registration and retains folder input')
            check(len(snapshot()['projects']) == 1, 'regular file registers no project')
            a.send(b'\x1b')
            a.click(17, 2)
            paste(a, str(project) + '/')
            a.pump(.8)
            a.click_label('Add this folder')
            a.pump(1.2)
            projects = snapshot()['projects']
            selected = next(p for p in projects if p['Name'] == 'Navigation project')
            check(view('navigation-a')['ProjectFilter'] == selected['ID'] and view('navigation-a')['Active'] == original,
                  'adding folder selects project filter without changing active thread')
            check(not any(t.get('ProjectID') == selected['ID'] for t in snapshot()['threads']),
                  'adding project creates no thread or execution')
            a.click(14, 2)
            paste(a, str(project))
            capture(a, '02-project-path-search')
            check('Navigation project' in '\n'.join(a.screen()), 'project picker searches server paths')
            a.send(b'\r', .8)
            # Compose icon at the right of the inline search header.
            a.click(20, 2)
            # New thread always asks for its destination project first.
            paste(a, 'Navigation project')
            a.send(b'\r', 1.2)
            draft_view = view('navigation-a')
            check(draft_view['Active'] == '' and draft_view['DraftProjectID'] == selected['ID']
                  and not any(t.get('ProjectID') == selected['ID'] for t in snapshot()['threads']),
                  'New thread opens local draft without authoritative thread')
            paste(a, 'Navigation start')
            a.click(14, 2)
            a.click_label('All projects')
            original_title = next(t['Title'] for t in snapshot()['threads'] if t['ID'] == original)
            a.click_label(original_title[:8])
            a.pump(1.2)
            check(view('navigation-a')['Active'] == original, 'existing thread selection leaves local draft')
            a.click(14, 2)
            paste(a, 'Navigation project')
            a.send(b'\r', .8)
            a.click(20, 2)
            paste(a, 'Navigation project')
            a.send(b'\r', 1.2)
            check(view('navigation-a')['DraftThreads'][selected['ID']]['Draft'] == 'Navigation start'
                  and 'Navigation start' in '\n'.join(a.screen()),
                  'returning to new thread restores its project-local draft')
            a.send(b'\r', .8)
            check(not any(t.get('ProjectID') == selected['ID'] for t in snapshot()['threads'])
                  and view('navigation-a')['DraftThreads'][selected['ID']]['Draft'] == 'Navigation start',
                  'Send without model preserves local prompt and creates no thread')
            a.click_label('Choose model')
            # Outside click dismisses without activating underlying New thread.
            a.click(20, 2)
            check('Demo model' not in '\n'.join(a.screen())
                  and view('navigation-a')['DraftThreads'][selected['ID']]['Draft'] == 'Navigation start',
                  'outside click dismisses centered model modal without pass-through')
            a.click_label('Choose model')
            a.click_label('Reference · Demo model')
            a.send(b'\r', 1.1)
            current = view('navigation-a')['Active']
            thread = next(t for t in snapshot()['threads'] if t['ID'] == current)
            check(thread['Checkout'] == str(project.resolve()) and any(
                  item['Text'] == 'Navigation start' for item in (thread.get('Activity') or []) + (thread.get('Queue') or [])),
                  'first valid Send creates thread and accepts initial prompt')
            a.click_label('Reference')
            check('read-only during active work' in '\n'.join(a.screen()),
                  'active turn configuration is read-only')
            a.pump(9)
            a.click_label('Reference')
            check('Reference · Demo model' in '\n'.join(a.screen()), 'idle thread configuration remains editable')
            a.send(b'\x1b')
            a.send(b'\x1b')
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
            b.click(4, thread_row(b) + 1)
            paste(b, 'Second client draft')
            check(view('navigation-b')['Active'] == current, 'thread metadata selects same thread with local draft')
            a.click(14, 2)
            paste(a, 'Navigation')
            capture(a, '03-project-name-search')
            check('Navigation project' in '\n'.join(a.screen()), 'project picker searches names')
            a.send(b'\r', .8)
            check(view('navigation-a')['Active'] == current, 'filtering leaves center thread selected')
            row = thread_row(a)
            a.send(f'\x1b[<35;5;{row+1}M'.encode())
            capture(a, '04-close-hover')
            quick_action(a, row)
            a.pump(1.2)
            check(next(t for t in snapshot()['threads'] if t['ID'] == current).get('Closed', False),
                  'hover check closes idle thread without submitting its draft')
            a.click_label('Closed (')
            a.pump(.8)
            a.click(22, thread_row(a) + 1)
            a.pump(1.2)
            check(next(t for t in snapshot()['threads'] if t['ID'] == current).get('Closed', False)
                  and 'This thread is closed' in '\n'.join(a.screen())
                  and view('navigation-a')['Threads'][current]['Draft'] == 'Preserved navigation draft',
                  'Closed card edge views thread without reopening and restores draft')
            a.click_label('Reopen')
            a.pump(.8)
            check(not next(t for t in snapshot()['threads'] if t['ID'] == current).get('Closed', False),
                  'explicit Reopen changes lifecycle without sending draft')
            quick_action(a, thread_row(a))
            a.pump(.8)
            a.click(22, thread_row(a) + 1)
            a.send(b'\x1b')
            a.send(b'\r', 1.1)
            sent = next(t for t in snapshot()['threads'] if t['ID'] == current)
            check(not sent.get('Closed', False) and any(item['Text'] == 'Preserved navigation draft'
                  for item in (sent.get('Activity') or []) + (sent.get('Queue') or [])),
                  'Send from closed view atomically reopens and accepts preserved prompt')
            a.pump(9)
            row = thread_row(a)
            quick_action(a, row)
            a.pump(.8)
            row = thread_row(a)
            a.send(f'\x1b[<35;5;{row+1}M'.encode())
            quick_action(a, row)
            capture(a, '05-delete-confirm')
            a.send(b'\r', .8)
            check(any(t['ID'] == current for t in snapshot()['threads']), 'default Enter cancels permanent deletion')
            quick_action(a, thread_row(a))
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
