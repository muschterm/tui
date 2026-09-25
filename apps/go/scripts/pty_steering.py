#!/usr/bin/env python3
"""Isolated fixture steering via OS PTY; not provider or GUI compatibility evidence."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import urllib.request
import uuid

from pty_smoke import Terminal
from harness_env import isolated_env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, required=True)
    args = parser.parse_args()
    binary, artifacts = args.binary.resolve(), args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'OS PTY + isolated fixture server; SGR pointer and keyboard', 'checks': []}

    def check(condition, description):
        if not condition:
            raise AssertionError(description)
        report['checks'].append(description)

    with tempfile.TemporaryDirectory(prefix='tui-steer-home-', dir='/tmp') as directory:
        home = Path(directory)
        environment = isolated_env(directory)
        terminals = []

        def cli(*words):
            return subprocess.check_output([str(binary), *words], env=environment, text=True)

        def api(path, data=None):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization': 'Bearer ' + discovery['Token'],
                         'X-TUI-Protocol': '1', 'Content-Type': 'application/json'})
            with urllib.request.urlopen(request, timeout=3) as response:
                return json.load(response)

        def thread():
            return next(t for t in api('snapshot')['threads'] if t['ID'] == 'thread-review')

        def enqueue(text):
            return api('command', {'Version': 1, 'ID': uuid.uuid4().hex, 'Kind': 'prompt.send',
                'ThreadID': 'thread-review', 'Text': text, 'Settings': thread()['Effective'],
                'Attachments': [{'Kind': 'file', 'Name': 'captured.txt', 'Source': '/fixture/source',
                                 'Content': 'captured bytes, not a live path read'}]})['TargetID']

        try:
            cli('server', 'start')
            first = enqueue('Use this captured message now')
            second = enqueue('Keyboard steering input')
            before = thread()
            a = Terminal(binary, home, 'steer-a', artifacts)
            terminals.append(a)
            a.click_label('Review key')
            a.click_label('Ask to do anything')
            a.send(b'\x1b[200~Retain my ordinary draft\x1b[201~')
            b = Terminal(binary, home, 'steer-b', artifacts)
            terminals.append(b)
            b.click_label('Review key')
            a.click_label('Steer')
            a.pump(.6)
            after = thread()
            check([p['ID'] for p in after['Queue']] == [second], 'pointer Steer removes only the selected queued message')
            delivered = [i for i in after['Activity'] if i['ID'] == first]
            check(len(delivered) == 1 and delivered[0]['TurnID'] == before['TurnID'] and
                  delivered[0]['Prompt'] == before['Queue'][0], 'steered conversation input retains its turn, settings and captured attachment')
            check(after['State'] == before['State'] == 'waiting' and after['Tick'] == before['Tick'] and
                  after['Requests'] == before['Requests'], 'steering does not restart work or answer blocking requests')
            b.pump(.5)
            check('Use this captured message now' in '\n'.join(b.screen()), 'second client catches up with the steered conversation message')
            a.resize(48, 22)
            a.pump(.5)
            check(any('Steer' in line for line in a.screen()[:-1]), 'narrow queue keeps labelled Steer reachable')
            a.click_label('QUEUED')
            a.send(b'\r', .7)
            after = thread()
            check(not after['Queue'] and len([i for i in after['Activity'] if i['ID'] == second]) == 1,
                  'Enter on the queue menu steers the remaining message once')
            a.pump(.7)
            saved = api('views/steer-a')['data']
            check(saved['Threads']['thread-review']['Draft'] == 'Retain my ordinary draft',
                  'pointer and keyboard steering preserve the ordinary composer draft')
            a.close()
            terminals.remove(a)
            b.close()
            terminals.remove(b)
            check(thread()['State'] == 'waiting', 'detaching clients leaves the same server-owned turn waiting')
            cli('server', 'stop')
            cli('server', 'start')
            after = thread()
            check(after['NeedsResume'] and after['TurnID'] == before['TurnID'] and
                  all(len([i for i in after['Activity'] if i['ID'] == ident]) == 1 for ident in (first, second)),
                  'restart retains each accepted input once and requires explicit Resume')
            third = enqueue('Keep this queued until Resume')
            a = Terminal(binary, home, 'steer-a', artifacts)
            terminals.append(a)
            a.click_label('Steer')
            check([p['ID'] for p in thread()['Queue']] == [third] and 'Resume' in a.screen()[-1],
                  'Steer before Resume explains unavailability and retains the queued input')
            check(api('views/steer-a')['data']['Threads']['thread-review']['Draft'] == 'Retain my ordinary draft',
                  'reconnect and unavailable steering preserve the restored draft')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            raise
        finally:
            for terminal in terminals:
                terminal.close()
            cli('server', 'stop')
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
