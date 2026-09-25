#!/usr/bin/env python3
"""Project/file completion checks through an isolated OS PTY, not a GUI terminal."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request
from pty_smoke import Terminal
from harness_env import isolated_env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, default=Path('/tmp/tui-path-completion-pty'))
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'Isolated OS PTY; real filesystem reads; Demo execution', 'checks': []}

    def check(ok, label):
        if not ok:
            raise AssertionError(label)
        report['checks'].append(label)

    with tempfile.TemporaryDirectory(prefix='tui-path-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = isolated_env(directory)
        start = home / 'projects'
        project = start / 'Sample project'
        (project / 'src').mkdir(parents=True)
        (project / 'space folder').mkdir()
        source = project / 'src' / 'hello.txt'
        source.write_text('before selection\n')
        (project / 'space folder' / 'notes.txt').write_text('spaced path\n')
        (project / 'bad.bin').write_bytes(b'\x00binary')
        term = None

        def get(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            req = urllib.request.Request(discovery['URL'] + '/v1/' + path, headers={
                'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(req, timeout=3) as response:
                return json.load(response)

        def snap():
            return get('snapshot')

        def view():
            term.pump(1.15)
            return get('views/path-review')['data']

        def paste(text):
            term.send(b'\x1b[200~' + text.encode() + b'\x1b[201~', .6)

        def capture(name):
            (artifacts / (name + '.screen.txt')).write_text('\n'.join(term.screen()) + '\n')

        try:
            subprocess.run([str(binary), 'server', 'start'], env=env, check=True, capture_output=True)
            term = Terminal(binary, home, 'path-review', artifacts)
            baseline = len(snap()['threads'])
            term.click(3, term.rows-3)
            term.click_label('Home directory (~)')
            term.send(b'\x01\x0b')
            paste(str(start) + '/')
            term.click_label('Save starting folder')
            term.pump(.6)
            check(snap()['app_settings']['ProjectDirectory'] == str(start.resolve()), 'General saves project starting folder')
            term.send(b'\x1b')
            term.click(20, 2)
            check('NEW THREAD IN PROJECT' in '\n'.join(term.screen()), 'New thread opens destination picker')
            term.click_label('Add project')
            paste('Sam')
            check('Sample project/' in '\n'.join(term.screen()), 'relative typeahead begins at configured folder')
            term.send(b'\t', .6)
            check(str(project.resolve()) in '\n'.join(term.screen()), 'Tab enters suggested folder without registering')
            check(len(snap()['projects']) == 1, 'browsing creates no project')
            capture('01-folder')
            term.click_label('Add this folder')
            term.pump(.8)
            p = next(p for p in snap()['projects'] if p['Name'] == 'Sample project')
            v = view()
            check(v['DraftProjectID'] == p['ID'] and v['Active'] == '', 'add-from-New-thread continues to its local draft')
            check(len(snap()['threads']) == baseline, 'destination selection creates no authoritative thread')
            term.click_label('Choose model')
            term.click_label('Reference · Demo model')
            paste('Review @s')
            check('src/' in '\n'.join(term.screen()), '@ searches project root')
            term.click_label('src/')
            check('hello.txt' in '\n'.join(term.screen()), 'directory click browses files without Send')
            term.send(b'\r', .6)
            check('Review @src/hello.txt' in '\n'.join(term.screen()), 'Enter inserts selected relative file reference')
            v = view()
            check(v['DraftThreads'][p['ID']]['Attachments'][0]['Source'] == 'src/hello.txt', 'selected file is removable draft context')
            source.write_text('captured at Send\n')
            term.send(b'\r', .8)
            v = view()
            thread_id = v['Active']
            th = next(t for t in snap()['threads'] if t['ID'] == thread_id)
            accepted = next(a['Prompt'] for a in th['Activity'] if a.get('Prompt') and a['Prompt']['Text'].startswith('Review @'))
            check(accepted['Attachments'][0]['Content'] == 'captured at Send\n', 'server captures real contents at Send, not selection')
            source.write_text('changed after acceptance\n')
            th = next(t for t in snap()['threads'] if t['ID'] == thread_id)
            accepted = next(a['Prompt'] for a in th['Activity'] if a.get('Prompt') and a['Prompt']['Text'].startswith('Review @'))
            check(accepted['Attachments'][0]['Content'] == 'captured at Send\n', 'accepted capture survives later file changes')
            paste('Check @bad')
            term.send(b'\r', .5)
            term.send(b'\r', .8)
            v = view()
            check(v['Threads'][thread_id]['Draft'] == 'Check @bad.bin ' and len(v['Threads'][thread_id]['Attachments']) == 1,
                  'binary capture failure preserves text and selected file')
            check(v['Pending'] is None, 'rejected capture clears command uncertainty')
            check('CANNOT SEND MESSAGE' in '\n'.join(term.screen()), 'rejected capture opens a visible error dialog')
            term.send(b'\x1b')
            # The composer strip's chip removes bad.bin from its icon slot
            # (two cells before the name), separate from its viewer label.
            chip = next((y, line) for y, line in enumerate(term.screen()) if ' bad.bin ' in line and '@bad.bin' not in line)
            term.click(chip[1].find('bad.bin') - 2, chip[0])
            term.pump(.5)
            v = view()
            check(v['Threads'][thread_id]['Draft'] == 'Check @bad.bin ' and len(v['Threads'][thread_id]['Attachments']) == 0,
                  'chip icon slot removes only the attachment and keeps the draft')
            term.send(b'\x01\x0b')
            paste('Read @spa')
            term.send(b'\t', .6)
            check('@"space folder/' in '\n'.join(term.screen()), 'directory completion quotes spaces')
            term.send(b'\r', .6)
            check('@"space folder/notes.txt"' in '\n'.join(term.screen()), 'file completion preserves quoted path')
            term.send(b'\x1b')
            term.send(b'\x01\x0b')
            paste('@')
            term.resize(47, 22)
            term.pump(.7)
            check('PROJECT FILES' in '\n'.join(term.screen()) and 'src/' in '\n'.join(term.screen()), '@ popup remains usable at 47x22')
            capture('02-narrow-mention')
            count = len(snap()['threads'])
            term.send(b'\x1b', .4)
            check('PROJECT FILES' not in '\n'.join(term.screen()), 'Escape dismisses suggestions and keeps prompt')
            check(len(snap()['threads']) == count and view()['Threads'][thread_id]['Draft'] == '@', 'dismiss does not send')
            term.resize(160, 50)
            term.pump(.5)
            term.click(20, 2)
            check('NEW THREAD IN PROJECT' in '\n'.join(term.screen()), 'filtered project still opens destination picker')
            term.send(b'\x1b')
            term.close()
            term = None
            subprocess.run([str(binary), 'server', 'stop'], env=env, check=True, capture_output=True)
            subprocess.run([str(binary), 'server', 'start'], env=env, check=True, capture_output=True)
            check(snap()['app_settings']['ProjectDirectory'] == str(start.resolve()), 'starting folder persists across isolated server restart')
            check(source.read_text() == 'changed after acceptance\n', 'application did not modify project files')
        finally:
            if term is not None:
                term.close()
            subprocess.run([str(binary), 'server', 'stop'], env=env, capture_output=True)
    (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
