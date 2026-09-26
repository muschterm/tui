#!/usr/bin/env python3
"""Explicit worktrees via OS PTY against the real server: choose New worktree
in a new-thread draft's Workspace row, name the branch, Send, see the thread
attached to the created worktree, close the thread (through the API), then
Remove the worktree from the checkout details after its preview (verified with
`git`; the branch is kept). Byte-level evidence only, not GUI terminal
compatibility. Everything runs in temporary homes with a scrubbed Git
environment."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request
import uuid

from pty_smoke import Terminal
from harness_env import isolated_env, user_home


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, required=True)
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    for key in [k for k in os.environ if k.startswith('GIT_')]:
        del os.environ[key]
    report = {'harness': 'OS PTY + isolated server; explicit worktree create and remove', 'checks': []}

    def check(condition, description):
        if not condition:
            raise AssertionError(description)
        report['checks'].append(description)

    with tempfile.TemporaryDirectory(prefix='tui-worktree-home-', dir='/tmp') as directory:
        home = Path(directory)
        gitconfig = user_home(directory) / '.gitconfig'
        gitconfig.write_text('[user]\n\tname = Harness\n\temail = harness@example.invalid\n[init]\n\tdefaultBranch = main\n')
        environment = isolated_env(directory, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=str(gitconfig),
                                   XDG_CONFIG_HOME=str(home / 'xdg'))
        repo = home / 'repo'
        terminals = []

        def git(*words, cwd=repo):
            return subprocess.check_output(['git', *words], cwd=cwd, env=environment, text=True).strip()

        def cli(*words):
            return subprocess.check_output([str(binary), *words], env=environment, text=True, timeout=30)

        def api(path, data=None):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1',
                         'Content-Type': 'application/json'})
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response)

        def wait(condition, what, seconds=20):
            end = time.monotonic() + seconds
            while time.monotonic() < end:
                t.pump(.2)
                if condition():
                    return
            (artifacts / 'last-screen.txt').write_text('\n'.join(t.screen()) + '\n')
            raise AssertionError('timed out: ' + what)

        def visible(text):
            return text in '\n'.join(t.screen()[:-1])

        def paste(text):
            t.send(b'\x1b[200~' + text.encode() + b'\x1b[201~', .3)

        def worktree_thread():
            snap = api('snapshot')
            for w in snap.get('worktrees') or []:
                for th in snap['threads']:
                    if th.get('worktree_id') == w['id']:
                        return w, th
            return None, None

        try:
            repo.mkdir()
            git('init', '-q', '-b', 'main')
            (repo / 'a.txt').write_text('one\n')
            git('add', '.')
            git('commit', '-qm', 'Initial')
            head = git('rev-parse', 'HEAD')

            cli('server', 'start')
            api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind='project.add', Path=str(repo)))
            t = Terminal(binary, home, 'worktree', artifacts, environment={'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': str(gitconfig),
                                                                          'XDG_CONFIG_HOME': str(home / 'xdg')})
            terminals.append(t)
            t.pump(1)
            t.click(20, 2)
            paste('repo')
            t.send(b'\r', .8)
            wait(lambda: visible('New worktree'), 'draft Workspace row')
            check(visible('Checkout') and visible('New worktree'), 'the draft shows the Workspace choice')
            t.click_label('Choose model')
            t.click_label('Reference · Demo model')
            t.pump(.5)
            t.click_label('New worktree')
            wait(lambda: visible('Start · main @ ' + head[:7]), 'observed start commit')
            check(True, 'New worktree shows the observed start main @ ' + head[:7])
            t.click_label('Name the new branch…')
            wait(lambda: visible('NEW WORKTREE BRANCH'), 'branch dialog')
            paste('feature/harness')
            t.send(b'\r', .8)
            wait(lambda: visible('feature/harness') and not visible('NEW WORKTREE BRANCH'), 'branch set')
            t.click_label('Ask to do anything')
            paste('Work in the worktree')
            t.send(b'\r', 1)
            wait(lambda: worktree_thread()[0] is not None and worktree_thread()[0]['state'] == 'present', 'worktree thread attached', 40)
            record, thread = worktree_thread()
            listed = git('worktree', 'list', '--porcelain')
            check(record['branch'] == 'feature/harness' and record['start_oid'] == head and record['path'] in listed,
                  'Send created the worktree on a new branch at the observed commit, registered with Git')
            wait(lambda: visible('Worktree · feature/harness'), 'checkout row names the worktree')
            check(True, 'the thread\'s checkout row reads Worktree · feature/harness')

            def idle():
                th = next(x for x in api('snapshot')['threads'] if x['ID'] == thread['ID'])
                return th['State'] not in ('running', 'waiting') and not th.get('Queue') and \
                    not any(r.get('State') == 'pending' for r in th.get('Requests') or [])
            wait(idle, 'thread idle', 60)
            th = next(x for x in api('snapshot')['threads'] if x['ID'] == thread['ID'])
            api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind='thread.close', ThreadID=th['ID'],
                                Revision=th.get('LifecycleRevision', 0)))
            wait(lambda: next(x for x in api('snapshot')['threads'] if x['ID'] == thread['ID']).get('Closed'), 'thread closed')
            t.pump(1)
            t.click_label('Worktree · feature/harness')
            wait(lambda: visible('Remove worktree…'), 'checkout details offer Remove')
            t.click_label('Remove worktree…')
            wait(lambda: visible('Branch feature/harness is kept'), 'removal preview')
            lines = t.screen()[:-1]
            title = next(y for y, line in enumerate(lines) if 'REMOVE WORKTREE · feature/harness' in line)
            # Title, rule, then the first item: Cancel.
            check(any('Cancel' in line for line in lines[title + 1:title + 4]) and visible('Branch feature/harness is kept'),
                  'the removal confirmation, titled for the branch, offers Cancel as its first item and says the branch is kept')
            t.click_label('Remove worktree directory')
            wait(lambda: not Path(record['path']).exists(), 'worktree directory removed', 30)
            check(record['path'] not in git('worktree', 'list', '--porcelain') and
                  git('rev-parse', '--verify', 'refs/heads/feature/harness') == head,
                  'Remove deletes the worktree and its registration; branch feature/harness is kept')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            try:
                cli('server', 'stop')
            except Exception as error:
                report.setdefault('cleanup_error', str(error))
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
