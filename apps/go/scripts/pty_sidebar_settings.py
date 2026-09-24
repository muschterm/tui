#!/usr/bin/env python3
"""Isolated OS-PTY settings review. No native terminal/SSH compatibility claim."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request
from pty_smoke import Terminal


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, default=Path('/tmp/tui-sidebar-settings-pty'))
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'OS PTY; isolated server home; fixture execution', 'checks': []}

    def check(ok, label):
        if not ok:
            raise AssertionError(label)
        report['checks'].append(label)

    def paste(term, text):
        term.send(b'\x1b[200~' + text.encode() + b'\x1b[201~', 1.1)

    with tempfile.TemporaryDirectory(prefix='tui-settings-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = dict(os.environ, TUI_GO_HOME=directory)
        project = home / 'Sidebar project'
        project.mkdir()
        marker = project / 'keep.txt'
        marker.write_text('keep this file\n')
        term = None

        def get(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            req = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(req, timeout=3) as response:
                return json.load(response)

        def snap():
            return get('snapshot')

        def capture(name):
            (artifacts / (name + '.screen.txt')).write_text('\n'.join(term.screen()) + '\n')

        def app_settings():
            term.click(3, term.rows-3)

        def category(name):
            for y, line in enumerate(term.screen()[1:-1], start=1):
                if line[2:22].strip() == name:
                    term.click(3, y)
                    return
            raise AssertionError('settings category missing: ' + name)

        def form_value(label):
            # Exclude breadcrumb/title/sidebar copies of a project name.
            for y, line in enumerate(term.screen()[4:-1], start=4):
                at = line.find(label, 25)
                if at >= 0:
                    term.click(at, y)
                    return
            raise AssertionError('settings value missing: ' + label)

        def nav_back():
            term.click_label('Back')

        def project_settings(name):
            term.click(14, 2)
            paste(term, name)
            for y, line in enumerate(term.screen()[:-1]):
                if name in line and '\ueaf8' in line:
                    term.click(line.index('\ueaf8'), y)
                    return
            raise AssertionError('project gear missing')

        try:
            subprocess.run([str(binary), 'server', 'start'], env=env, check=True, capture_output=True)
            term = Terminal(binary, home, 'sidebar-review', artifacts)
            term.click(17, 2)
            paste(term, str(project) + '/')
            term.pump(.8)
            term.click_label('Add this folder')
            term.pump(1.2)
            p = next(p for p in snap()['projects'] if p['Name'] == 'Sidebar project')
            term.click(20, 2)
            # New thread always asks for its destination project first.
            paste(term, 'Sidebar project')
            term.send(b'\r', 1.2)
            view = get('views/sidebar-review')['data']
            check(view['Active'] == '' and view['DraftProjectID'] == p['ID'], 'new thread starts as local draft')
            paste(term, 'Settings start')
            term.send(b'\x13', .8)
            check('CANNOT SEND MESSAGE' in '\n'.join(term.screen()) and get('views/sidebar-review')['data']['Active'] == '',
                  'Send without a model opens an error dialog without creating a thread')
            term.click_label('Choose model')
            term.click_label('Reference · Demo model')
            term.send(b'\r', 1.1)
            thread_id = get('views/sidebar-review')['data']['Active']
            term.pump(9)
            term.send(b'\x1b')
            paste(term, 'Keep this unsent draft')
            term.click_label('Search')
            paste(term, 'not-a-match')
            check('No matching open' in '\n'.join(term.screen()), 'thread search filters list')
            check(get('views/sidebar-review')['data']['Threads'][thread_id]['Draft'] == 'Keep this unsent draft',
                  'thread search preserves prompt draft')
            term.send(b'\x01\x0b', .8)
            app_settings()
            check(not snap()['app_settings']['ContinueAfterRestart'], 'restart continuation defaults off')
            term.click_label('Off')
            term.pump(.8)
            check(snap()['app_settings']['ContinueAfterRestart'], 'restart toggle persists through server command')
            # Workspace default is a segmented choice; each segment sets its value.
            term.click_label('Worktree')
            term.pump(.8)
            check(snap()['app_settings']['WorkspaceDefault'] == 'worktree', 'app workspace preference persists')
            capture('01-general')
            category('Agents')
            term.click_label('Agent: built-in default')
            term.click_label('Fixture agent')
            term.click_label('Effort: Medium')
            term.click_label('High')
            term.pump(.8)
            defaults = snap()['app_settings']['NewThreadDefaults']
            check(defaults['AgentID'] == 'fixture' and defaults['Settings']['Effort'] == 'high',
                  'Agents category saves provider settings as revisioned new-thread defaults')
            check(get('views/sidebar-review')['data']['Threads'][thread_id]['Draft'] == 'Keep this unsent draft',
                  'saving new-thread defaults preserves existing prompt drafts')
            capture('01-agents')
            category('General')
            nav_back()
            term.click(20, 2)
            paste(term, 'Sidebar project')
            term.send(b'\r', 1.2)
            check(get('views/sidebar-review')['data']['DraftProjectID'] == p['ID'] and len(snap()['threads']) == 3,
                  'opening another local draft does not create a thread under a worktree default')
            project_settings('Sidebar project')
            screen = '\n'.join(term.screen()[:-1])
            check('Workspace default' not in screen and 'Folder on server' not in screen and 'Appearance' not in screen and 'About' not in screen,
                  'Project category exposes identity and removal without execution or app categories')
            category('General')
            screen = '\n'.join(term.screen()[:-1])
            check('Settings / General / Sidebar project' in screen and 'Continue threads after restart' not in screen,
                  'project General has named scope and excludes app-only restart setting')
            app_before = snap()['app_settings']
            check('Using app default: Worktree' in '\n'.join(term.screen()), 'project General shows the inherited effective value')
            term.click_label('Current checkout')
            term.pump(.8)
            p_id = p['ID']
            check(next(p for p in snap()['projects'] if p['ID'] == p_id)['WorkspaceDefault'] == 'checkout',
                  'project workspace override persists')
            check(snap()['app_settings'] == app_before, 'project override leaves app settings unchanged')
            capture('02-project-general')
            term.click_label('App default')
            term.pump(.8)
            check(next(p for p in snap()['projects'] if p['ID'] == p_id)['WorkspaceDefault'] == '' and
                  'Using app default: Worktree' in '\n'.join(term.screen()), 'reset clears override and displays inherited effective value')
            term.click_label('Current checkout')
            term.pump(.8)
            category('Keybindings')
            check('Project keybinding overrides are unavailable' in '\n'.join(term.screen()),
                  'project Keybindings does not imply implemented overrides')
            category('Project')
            form_value('Sidebar project')
            term.send(b'\x01\x0b')
            paste(term, 'Renamed workspace')
            term.click_label('Save name')
            term.pump(.8)
            check(next(p for p in snap()['projects'] if p['ID'] == p_id)['Name'] == 'Renamed workspace', 'project rename persists')
            term.click_label('Name initials')
            term.click_label('Rocket')
            term.pump(.8)
            check(next(p for p in snap()['projects'] if p['ID'] == p_id)['Icon'] == 'rocket', 'project icon persists')
            check('Settings / Project / Renamed workspace' in term.screen()[0], 'breadcrumb follows accepted project rename')
            term.click_label('Rocket')
            term.click_label('Icon color')  # A menu pair row: label left, value right.
            term.click_label('Teal')
            term.pump(.8)
            check(next(p for p in snap()['projects'] if p['ID'] == p_id)['Color'] == 'teal', 'icon menu retains color customization')
            capture('02-project')
            # The same settings page survives a narrow layout; Back stays pinned.
            term.resize(47, 22)
            term.pump(.8)
            check('Project settings' in '\n'.join(term.screen()), '47x22 navigation column restores project settings')
            term.click(10, 8)
            term.send(b'\x1b[F', .8)
            check('\uea76' in term.screen()[0], '47x22 settings keeps workspace return control visible')
            capture('03-small-settings')
            term.key(2)
            check('General' in '\n'.join(term.screen()) and 'Back' in '\n'.join(term.screen()), '47x22 F2 exposes categories and Back')
            category('General')
            check('Current checkout' in '\n'.join(term.screen()) and 'Continue threads after restart' not in '\n'.join(term.screen()),
                  '47x22 project General preserves project scope')
            capture('03-small-general')
            term.key(2)
            term.click_label('Back')
            check('Conversation' in term.screen()[0], 'small-screen Back restores previous workspace column')
            term.resize(160, 50)
            term.pump(.8)
            project_settings('Renamed workspace')
            term.click_label('Remove project')
            capture('04-removal-confirm')
            check('1 threads permanently' in '\n'.join(term.screen()) and 'Files on disk will be kept' in '\n'.join(term.screen()),
                  'removal confirmation identifies thread count and file preservation')
            term.send(b'\r', .8)
            check(any(p['ID'] == p_id for p in snap()['projects']), 'default Enter cancels project removal')
            term.click_label('Remove project')
            term.click_label('Delete project and')
            term.pump(1.2)
            state = snap()
            view = get('views/sidebar-review')['data']
            check(not any(p['ID'] == p_id for p in state['projects']) and not any(t['ID'] == thread_id for t in state['threads']),
                  'confirmed removal deletes project and its thread')
            check(view['ProjectFilter'] == '' and thread_id not in view['Threads'], 'removal clears stale client filter and thread view')
            check(marker.read_text() == 'keep this file\n', 'project removal preserves checkout files')
            capture('05-removed')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            if term:
                capture('failure')
            raise
        finally:
            if term:
                term.close()
            subprocess.run([str(binary), 'server', 'stop'], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
