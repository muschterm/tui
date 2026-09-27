#!/usr/bin/env python3
"""Git surface via OS PTY against the real server: stage and commit, merge a
conflicting branch and resolve it through the operation panel, then pull a
fast-forward from a local bare remote, Fetch & prune a deleted branch, run
an interactive rebase (reorder, squash, reword) from the plan editor, soft
reset from the commit menu and continue an edit stop. Byte-level evidence only, not GUI
terminal compatibility. Everything runs in temporary homes; git is invoked
with the isolated environment, so no user or system Git configuration applies
beyond what the harness sets in the repository."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unicodedata
import urllib.request
import uuid

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
    # Developer Git variables (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
    # GIT_CONFIG_COUNT/KEY_n/VALUE_n, GIT_CONFIG_PARAMETERS, alternates, ...)
    # would redirect or reconfigure every git the harness, server and TUI run.
    # Scrub them process-wide so all children inherit a clean environment.
    for key in [k for k in os.environ if k.startswith('GIT_')]:
        del os.environ[key]
    report = {'harness': 'OS PTY + isolated server; Git surface write, merge and pull flows', 'checks': []}

    def check(condition, description):
        if not condition:
            raise AssertionError(description)
        report['checks'].append(description)

    with tempfile.TemporaryDirectory(prefix='tui-git-home-', dir='/tmp') as directory:
        home = Path(directory)
        # GIT_CONFIG_NOSYSTEM and the temporary HOME keep user and system Git
        # configuration out; identity comes from the environment.
        # The identity lives in the temporary user HOME: the server's Git
        # reads (git var) use HOME, not GIT_CONFIG_GLOBAL.
        from harness_env import user_home
        gitconfig = user_home(directory) / '.gitconfig'
        gitconfig.write_text('[user]\n\tname = Harness\n\temail = harness@example.invalid\n[init]\n\tdefaultBranch = main\n')
        environment = isolated_env(directory, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=str(gitconfig),
                                   XDG_CONFIG_HOME=str(home / 'xdg'), GIT_AUTHOR_NAME='Harness',
                                   GIT_AUTHOR_EMAIL='harness@example.invalid', GIT_COMMITTER_NAME='Harness',
                                   GIT_COMMITTER_EMAIL='harness@example.invalid')
        repo, bare, other = home / 'repo', home / 'remote.git', home / 'other'
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

        def wait(condition, what, seconds=15):
            end = time.monotonic() + seconds
            while time.monotonic() < end:
                t.pump(.2)
                if condition():
                    return
            (artifacts / 'last-screen.txt').write_text('\n'.join(t.screen()) + '\n')
            raise AssertionError('timed out: ' + what)

        def screen():
            return '\n'.join(t.screen()[:-1])

        def visible(text):
            return text in screen()

        def row_of(text):
            for y, line in enumerate(t.screen()[:-1]):
                if text in line:
                    return y, cells(line[:line.index(text)])
            raise AssertionError('not on screen: ' + text)

        def right_click(text):
            y, x = row_of(text)
            t.send(f'\x1b[<2;{x+1};{y+1}M\x1b[<2;{x+1};{y+1}m'.encode(), .5)

        def heading_glyph():
            # The GIT heading's Refresh glyph (cod-refresh).
            for y, line in enumerate(t.screen()[:-1]):
                if line.lstrip().startswith('GIT') and '\ueb37' in line:
                    return y, cells(line[:line.index('\ueb37')])
            raise AssertionError('Refresh control not on screen')

        def refresh():
            y, glyph = heading_glyph()
            t.click(glyph, y)
            t.pump(.8)

        def paste(text):
            t.send(b'\x1b[200~' + text.encode() + b'\x1b[201~', .3)

        try:
            # A repository with one commit, pushed to a local bare remote.
            git('init', '-q', '--bare', '-b', 'main', str(bare), cwd=home)
            repo.mkdir()
            git('init', '-q', '-b', 'main')
            (repo / 'a.txt').write_text('one\n')
            (repo / 'b.txt').write_text('base\n')
            (repo / 'p.txt').write_text(''.join(f'line {i}\n' for i in range(1, 21)))
            git('add', '.')
            git('commit', '-qm', 'Initial')
            git('remote', 'add', 'origin', str(bare))
            git('push', '-qu', 'origin', 'main')

            cli('server', 'start')
            api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind='project.add', Path=str(repo)))
            t = Terminal(binary, home, 'git', artifacts, environment={'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': str(gitconfig),
                                                                     'XDG_CONFIG_HOME': str(home / 'xdg')})
            terminals.append(t)
            t.pump(1)
            # A new-thread draft in the repository's project; the Git surface
            # follows it.
            t.click(20, 2)
            paste('repo')
            t.send(b'\r', .8)
            t.key(3)
            wait(lambda: visible('ADD SURFACE'), 'surface chooser')
            t.click_label('Git')
            wait(lambda: visible('RECENT COMMITS'), 'Git surface')
            t.key(7)  # maximize the surface for room
            check(visible('main') and visible('Initial'), 'Git surface opens on the registered repository')

            # Stage a modified file from its diff viewer and commit.
            (repo / 'a.txt').write_text('one\ntwo\n')
            refresh()
            wait(lambda: visible('a.txt'), 'modified file listed')
            t.click_label('a.txt')
            wait(lambda: visible('+two') and visible('Stage hunk'), 'selectable diff viewer (hunks loaded)')
            t.send(b's', .8)
            wait(lambda: 'a.txt' in git('diff', '--cached', '--name-only'), 'staged through the viewer key', 10)
            check(git('diff', '--name-only') == '',
                  's in the diff viewer stages the hunk under the cursor, here all of a.txt (git diff --cached lists it)')
            t.send(b'\x1b', .5)
            wait(lambda: visible('Commit message'), 'composer')
            wait(lambda: visible('Harness <harness@example.invalid>'), 'isolated identity')
            check(True, 'the composer shows the isolated Git identity, not the developer\'s')
            t.click_label('Commit message')
            paste('Add two')
            t.send(b'\r', 1)
            wait(lambda: git('log', '-1', '--format=%s') == 'Add two', 'commit made')
            check(git('status', '--porcelain') == '' and git('log', '-1', '--format=%an') == 'Harness',
                  'Enter in the message commits; HEAD is the new commit, authored with the isolated identity')

            # Partial staging: stage one hunk, then one line of another hunk,
            # from the selectable diff viewer (ADR 0025).
            lines = [f'line {i}\n' for i in range(1, 21)]
            lines[1] = 'TWO\n'
            lines[15:15] = ['new-a\n', 'new-b\n']
            (repo / 'p.txt').write_text(''.join(lines))
            refresh()
            wait(lambda: visible('p.txt'), 'partially changed file listed')
            t.click_label('p.txt')
            # The hint row's "Stage hunk" and the headers' "Select hunk" exist
            # only once the hunks loaded; before that s is refused.
            wait(lambda: visible('+TWO') and visible('Stage hunk') and visible('Select hunk'), 'selectable diff')
            t.send(b's', .8)  # the cursor starts on the first hunk header
            wait(lambda: '+TWO' in git('diff', '--cached'), 'hunk staged', 10)
            cached = git('diff', '--cached')
            check('+new-a' not in cached and '+new-b' not in cached,
                  's with no selection stages only the hunk under the cursor (git diff --cached)')
            wait(lambda: not visible('+TWO') and not visible('Changed since shown'), 'patch and hunks reread after staging')
            t.send(b'\x1b[B', .3)  # to +new-a
            t.send(b' ', .3)
            wait(lambda: visible('1 selected'), 'line selected')
            t.send(b's', .8)
            wait(lambda: '+new-a' in git('diff', '--cached'), 'line staged', 10)
            cached, unstaged = git('diff', '--cached'), git('diff')
            check('+TWO' in cached and '+new-b' not in cached and '+new-b' in unstaged and '+new-a' not in unstaged,
                  'Space then s stages exactly the selected line; the rest stays unstaged')
            wait(lambda: not visible('+new-a'), 'hunks refetched after line staging')
            t.send(b'\x1b', .5)
            # Restore the harness file (temporary repository) so the later
            # flows see the same history and a clean tree.
            git('reset', '-q', '--', 'p.txt')
            git('checkout', '--', 'p.txt')
            refresh()

            # A conflicting branch, merged from its branch-row menu.
            git('checkout', '-qb', 'side')
            (repo / 'b.txt').write_text('side\n')
            git('commit', '-qam', 'Side change')
            git('checkout', '-q', 'main')
            (repo / 'b.txt').write_text('main\n')
            git('commit', '-qam', 'Main change')
            refresh()
            wait(lambda: visible('BRANCHES'), 'branches heading')
            t.click_label('BRANCHES')
            wait(lambda: visible('side'), 'branch rows')
            right_click(' side')
            wait(lambda: visible('Merge side into main…'), 'branch menu')
            t.click_label('Merge side into main…')
            wait(lambda: visible('Creates a merge commit'), 'merge preview')
            check(visible('Conflicts are not predicted'), 'merge preview confirms with the merge-commit outcome')
            t.send(b'\x1b[B\r', .8)  # from the focused Cancel to the confirm
            wait(lambda: visible('Merge in progress'), 'operation panel')
            wait(lambda: visible('UU b.txt'), 'conflict row')
            check((repo / '.git' / 'MERGE_HEAD').exists(), 'the merge stopped with a conflict and the panel shows it')

            right_click('UU b.txt')
            wait(lambda: visible('Choose theirs'), 'conflict menu')
            t.click_label('Choose theirs')
            t.pump(1.5)
            if visible('Replace with theirs'):
                t.send(b'\x1b[B\r', .8)
            wait(lambda: (repo / 'b.txt').read_text() == 'side\n', 'theirs written')
            check(True, 'Choose theirs writes the branch side into the working file')
            wait(lambda: visible('UU b.txt'), 'row after choose')
            right_click('UU b.txt')
            wait(lambda: visible('Mark resolved'), 'conflict menu again')
            t.click_label('Mark resolved')
            wait(lambda: git('diff', '--name-only', '--diff-filter=U') == '', 'resolved')
            check(True, 'Mark resolved stages the chosen content')
            wait(lambda: visible('Continue…'), 'continue offered')
            t.click_label('Continue…')
            wait(lambda: visible('Continue the merge'), 'continue confirmation')
            t.send(b'\x1b[B\r', .8)
            wait(lambda: not (repo / '.git' / 'MERGE_HEAD').exists(), 'merge finished')
            parents = git('rev-list', '--parents', '-n', '1', 'HEAD').split()
            check(len(parents) == 3 and (repo / 'b.txt').read_text() == 'side\n',
                  'Continue creates the merge commit with both parents')

            # A fast-forward from the bare remote.
            git('push', '-q', 'origin', 'main')
            git('clone', '-q', str(bare), str(other), cwd=home)
            (other / 'c.txt').write_text('remote\n')
            git('add', '.', cwd=other)
            git('commit', '-qm', 'Remote change', cwd=other)
            git('push', '-q', 'origin', 'main', cwd=other)
            remote_head = git('rev-parse', 'HEAD', cwd=other)
            refresh()  # focuses the heading's controls; f and p act there
            t.send(b'f', .5)
            wait(lambda: visible('Fetched origin'), 'fetch result')
            check(git('rev-parse', 'origin/main') == remote_head, 'Fetch updates the remote-tracking branch')
            t.send(b'p', .5)
            wait(lambda: git('rev-parse', 'HEAD') == remote_head, 'fast-forward')
            wait(lambda: visible('Fast-forwarded main'), 'pull result')
            check(True, 'Pull fast-forwards main to the remote commit and says so')

            # Fetch & prune: a branch deleted on the remote loses its
            # remote-tracking ref, and the notice names it.
            git('push', '-q', 'origin', 'main:gone', cwd=other)
            git('fetch', '-q', 'origin')
            check(git('for-each-ref', 'refs/remotes/origin/gone') != '', 'origin/gone fetched before pruning')
            git('update-ref', '-d', 'refs/heads/gone', cwd=bare)
            refresh()
            t.send(b'F', .5)
            wait(lambda: visible('pruned origin/gone'), 'fetch & prune result')
            check(git('for-each-ref', 'refs/remotes/origin/gone') == '',
                  'Fetch & prune removes origin/gone and the notice names it')
            # Interactive rebase (ADR 0026) through the plan editor: three
            # local commits are reordered, two squashed and one reworded.
            for name, subject in (('alpha', 'Alpha rb'), ('beta', 'Beta rb'), ('gamma', 'Gamma rb')):
                (repo / (name + '.txt')).write_text(name + '\n')
                git('add', name + '.txt')
                git('commit', '-qm', subject)
            base = git('rev-parse', 'HEAD~3')
            refresh()
            wait(lambda: visible('Gamma rb'), 'new commits listed')
            right_click('Alpha rb')
            wait(lambda: visible('Interactive rebase from'), 'commit menu presets')
            check(visible('Squash') and visible('into parent') and visible('Edit message of'),
                  'the commit menu offers the interactive rebase presets')
            t.click_label('Interactive rebase from')
            wait(lambda: visible('INTERACTIVE REBASE') and visible('Newest first'), 'plan editor')
            check(visible('pick') and git('rev-parse', 'HEAD~3') == base,
                  'Interactive rebase from here opens the plan editor; nothing ran yet')
            # Focus starts on the chosen commit (Alpha, the oldest row): go up
            # to Gamma, the newest, and move it below Beta.
            t.send(b'\x1b[A\x1b[A', .3)
            t.send(b'J', .5)
            t.send(b'\x1b[A', .3)   # up to Beta, now the newest row
            t.send(b's', .5)       # squash Beta into Gamma (the row below)
            t.send(b'm', .6)       # write the combined message (an older
            wait(lambda: visible('COMBINED MESSAGE'), 'combined message editor')  # server sends no exact originals)
            t.send(b'\x7f' * 40, .4)
            paste('Gamma rb')
            t.send(b'\n', .2)  # Ctrl+J: newline
            t.send(b'\n', .2)
            paste('Beta rb squashed')
            t.send(b'\r', .6)
            t.send(b'\x1b[B\x1b[B', .3)  # down to Alpha
            t.send(b'r', .6)       # reword: the message editor opens prefilled
            wait(lambda: visible('REWORD'), 'reword editor')
            t.send(b'\x7f' * 40, .4)
            paste('Alpha reworded')
            t.send(b'\r', .6)
            wait(lambda: visible('INTERACTIVE REBASE') and visible('Start rebase'), 'editor after reword')
            check(visible('squash') and visible('reword'), 'the editor shows the squash and reword actions')
            t.click_label('Start rebase')
            wait(lambda: visible('Rewrite main at'), 'rebase confirmation')
            check(visible('3 commits replayed') and visible('1 reworded') and visible('1 squashed or fixed up'),
                  'the confirmation summarises the rewrite')
            t.send(b'\x1b[B\r', 1)  # from the focused Cancel to the confirm
            wait(lambda: git('log', '-1', '--format=%s') == 'Gamma rb' and git('log', '-1', '--skip=1', '--format=%s') == 'Alpha reworded', 'rebase finished', 20)
            head_message = git('log', '-1', '--format=%B')
            files = git('show', '--name-only', '--format=', 'HEAD').split()
            check(git('rev-parse', 'HEAD~2') == base and 'Beta rb' in head_message and sorted(files) == ['beta.txt', 'gamma.txt']
                  and not (repo / '.git' / 'rebase-merge').exists(),
                  'git log: Alpha reworded, then Gamma with Beta squashed into it (combined message), on the same base')

            # Soft reset from the commit menu.
            refresh()
            wait(lambda: visible('Alpha reworded'), 'rebased history listed')
            target = git('rev-parse', 'HEAD~1')
            right_click('Alpha reworded')
            wait(lambda: visible('Soft reset main to'), 'soft reset item')
            t.click_label('Soft reset main to')
            wait(lambda: visible('Their changes stay staged'), 'soft reset confirmation')
            t.send(b'\x1b[B\r', 1)
            wait(lambda: git('rev-parse', 'HEAD') == target, 'soft reset')
            staged = git('diff', '--cached', '--name-only').split()
            check(sorted(staged) == ['beta.txt', 'gamma.txt'] and git('diff', '--name-only') == '',
                  'Soft reset from the commit menu moves main back one commit and keeps its changes staged')
            git('commit', '-qm', 'Gamma again')

            # An edit stop, continued from the operation panel.
            refresh()
            wait(lambda: visible('Gamma again'), 'recommitted')
            before = git('rev-parse', 'HEAD~1')
            right_click('Gamma again')
            wait(lambda: visible('Edit contents of'), 'edit preset')
            t.click_label('Edit contents of')
            wait(lambda: visible('INTERACTIVE REBASE') and visible('edit'), 'editor with the edit preset')
            t.click_label('Start rebase')
            wait(lambda: visible('Rewrite main at'), 'edit confirmation')
            t.send(b'\x1b[B\r', 1)
            wait(lambda: (repo / '.git' / 'rebase-merge').exists(), 'stopped for editing', 20)
            refresh()
            wait(lambda: visible('Stopped to edit'), 'edit stop in the panel')
            check(visible('Commit staged') and visible('Continue with message'),
                  'the operation panel shows the edit stop with Commit staged and Continue with message')
            t.click_label('Continue…')
            wait(lambda: visible('Continue the rebase'), 'continue confirmation')
            t.send(b'\x1b[B\r', 1)
            wait(lambda: not (repo / '.git' / 'rebase-merge').exists(), 'rebase finished after the edit stop', 20)
            check(git('log', '-1', '--format=%s') == 'Gamma again' and git('rev-parse', 'HEAD~1') == before and git('status', '--porcelain') == '',
                  'Continue at the edit stop recommits the staged changes with the original message')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            if terminals:
                (artifacts / 'failure-screen.txt').write_text('\n'.join(terminals[-1].screen()) + '\n')
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            try:
                cli('server', 'stop')
            except Exception as error:
                # Never mask the original failure (e.g. a failed start).
                report.setdefault('cleanup_error', str(error))
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
