#!/usr/bin/env python3
"""Opt-in live ACP OS-PTY validation; consumes the logged-in account's quota.

Requires pyte==0.8.2 for static colored frame reconstruction. Raw PTY output
stays in --artifacts; only redacted static frames belong in published evidence.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.request
import uuid

from pty_smoke import Terminal


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--agent', choices=['claude', 'codex'], default='claude')
    parser.add_argument('--model', help='model ID from the live catalogue')
    parser.add_argument('--unavailable-only', action='store_true')
    parser.add_argument('--exercise-controls', action='store_true', help='also exercise Stop/Resume and a Claude file approval')
    args = parser.parse_args()
    if os.environ.get('TUI_GO_LIVE_ACP') != '1':
        print('SKIP: set TUI_GO_LIVE_ACP=1 to use real adapters and account quota')
        return
    import pyte

    binary, artifacts = args.binary.resolve(), args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': '120x40 OS PTY; real ACP; isolated application home',
              'agent': args.agent, 'no_color': bool(os.environ.get('NO_COLOR')),
              'checks': [], 'frames': []}

    def check(ok, label):
        if not ok:
            raise AssertionError(label)
        report['checks'].append(label)

    with tempfile.TemporaryDirectory(prefix='tui-live-acp-', dir='/tmp') as directory:
        home = Path(directory)
        project = home / 'ACP validation'
        project.mkdir()
        subprocess.run(['git', 'init', '-q', str(project)], check=True)
        environment = dict(os.environ, TUI_GO_HOME=directory)
        if args.unavailable_only:
            environment['TUI_GO_AGENT_CODEX_COMMAND'] = '/nonexistent/tui-validation-codex-acp'
            environment['TUI_GO_AGENT_CLAUDE_COMMAND'] = '/nonexistent/tui-validation-claude-acp'
        terminal = None

        def cli(*words):
            return subprocess.check_output([str(binary), *words], env=environment, text=True, timeout=30)

        def api(path, data=None):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization': 'Bearer ' + discovery['Token'],
                         'X-TUI-Protocol': '1', 'Content-Type': 'application/json'})
            with urllib.request.urlopen(request, timeout=5) as response:
                return json.load(response)

        def command(kind, **values):
            return api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind=kind, **values))

        def wait(predicate, label, seconds=60):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                result = predicate()
                if result:
                    return result
                if terminal:
                    terminal.pump(.2)
                else:
                    time.sleep(.2)
            raise AssertionError('timed out: ' + label)

        def current_thread():
            return next((t for t in api('snapshot')['threads'] if t.get('ProjectID') == project_id), None)

        def paste(text):
            terminal.send(b'\x1b[200~' + text.encode() + b'\x1b[201~')

        def capture(name):
            terminal.pump(.3)
            screen = pyte.Screen(120, 40)
            pyte.Stream(screen).feed(terminal.output.decode('utf-8', 'replace'))
            rows = []
            # pyte preserves cell colors; replay is an approximation, not a
            # native terminal screenshot. Remove sensitive paths before export.
            named = {'black': 0, 'red': 1, 'green': 2, 'brown': 3, 'blue': 4,
                     'magenta': 5, 'cyan': 6, 'white': 7}
            def color(value, background):
                if value == 'default':
                    return str(49 if background else 39)
                if value in named:
                    return str((40 if background else 30) + named[value])
                if len(value) == 6:
                    return ('48' if background else '38') + ';2;' + ';'.join(str(int(value[i:i+2], 16)) for i in (0, 2, 4))
                raise ValueError('unsupported captured color: ' + value)
            for y in range(40):
                row = ''
                for x in range(120):
                    cell = screen.buffer[y][x]
                    if not cell.data:
                        continue
                    codes = ['0', color(cell.fg, False), color(cell.bg, True)]
                    codes += ['1'] if cell.bold else []
                    codes += ['4'] if cell.underscore else []
                    codes += ['7'] if cell.reverse else []
                    row += '\x1b[' + ';'.join(codes) + 'm' + cell.data
                rows.append(row + '\x1b[0m')
            # Replace sensitive text cell-by-cell, retaining the existing style
            # and cell count so redaction does not alter layout geometry.
            for y, line in enumerate(screen.display):
                spans = []
                for sensitive in (str(home), str(home.resolve()), str(Path.home())):
                    start = line.find(sensitive)
                    if start >= 0:
                        spans.append((start, start + len(sensitive), '<PATH>'))
                spans += [(m.start(), m.end(), '<EMAIL>') for m in re.finditer(r'[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}', line)]
                if spans:
                    # Sensitive path lines are retained as plain padded text;
                    # the rest of the frame keeps the captured styling.
                    for start, end, replacement in sorted(spans, reverse=True):
                        line = line[:start] + replacement.ljust(end-start) + line[end:]
                    rows[y] = '\x1b[0m' + line
            stem = '120x40-lightfalse-' + name
            (artifacts / (stem + '.ansi')).write_text('\n'.join(rows))
            (artifacts / (stem + '.screen.txt')).write_text('\n'.join(screen.display) + '\n')
            report['frames'].append(stem)

        try:
            cli('server', 'start')
            wait(lambda: all(a['State'] != 'probing' for a in api('snapshot')['agents']), 'startup probes')
            project_id = command('project.add', Path=str(project))['TargetID']
            terminal = Terminal(binary, home, 'acp-live', artifacts, environment, columns=120, rows=40)
            terminal.resize(120, 40)
            terminal.pump(.5)
            terminal.click(20, 2)
            paste('ACP validation')
            terminal.send(b'\r', .6)
            capture('01-draft')
            terminal.click_label('Demo')
            capture('02-unavailable' if args.unavailable_only else '02-agents')
            if args.unavailable_only:
                check('Codex · unavailable' in '\n'.join(terminal.screen()), 'missing executable is shown as unavailable in agent menu')
            else:
                agent = next(a for a in api('snapshot')['agents'] if a['ID'] == args.agent)
                check(agent['State'] == 'ready', 'selected adapter reports ready')
                terminal.click_label(agent['Name'] + ' · ready')
                option = next(o for o in agent['Options'] if o['ID'] == agent['Fields']['Model'])
                current = next((v for v in option['Values'] if v['Value'] == option['Current']), None)
                model = args.model or option['Current'] or option['Values'][0]['Value']
                selected = next(v for v in option['Values'] if v['Value'] == model)
                terminal.click_label(current['Name'] if current else 'Choose model')
                capture('03-models')
                terminal.click_label(selected['Name'])
                terminal.click(30, 34)
                paste('Reply with exactly pong. Do not use tools or change files.')
                capture('04-prompt')
                terminal.send(b'\x13', .1)
                capture('05-sent')
                wait(lambda: current_thread() and (current_thread().get('StopReason') or current_thread()['State'] == 'failed'), 'pong outcome', 90)
                t = current_thread()
                capture('06-reply')
                if t['State'] != 'idle':
                    report['outcome'] = {'state': t['State'], 'error': t.get('Error'), 'queued': len(t.get('Queue') or [])}
                    raise AssertionError('pong turn did not complete: ' + t.get('Error', ''))
                check(True, 'pong turn completed')
                check(any(i['Role'] == 'agent' and i.get('Text', '').strip() == 'pong' for i in (t['Activity'] or [])), 'real agent reply is pong')
                check('pong' in '\n'.join(terminal.screen()), 'pong appears in TUI transcript')
                check(not any('not connected yet' in row for row in terminal.screen()), 'no stale disconnected-provider copy')
                check('Initial prompt accepted · Demo' not in '\n'.join(terminal.screen()), 'real-agent acceptance has no fixture label')
                report['effective'] = t['Effective']
                report['usage'] = t.get('Usage')
                if args.exercise_controls:
                    terminal.click(30, 34)
                    paste('Write a very long numbered list of 1000 imaginary planets. Do not use tools.')
                    terminal.send(b'\x13', .1)
                    wait(lambda: current_thread()['State'] == 'running', 'running turn')
                    terminal.key(4)
                    terminal.click_label('Stop')
                    wait(lambda: current_thread().get('StopReason') == 'cancelled', 'cancel acknowledgement')
                    capture('07-cancelled')
                    before = len(current_thread()['Activity'])
                    terminal.click_label('Resume')
                    wait(lambda: not current_thread()['NeedsResume'], 'resume')
                    check(len(current_thread()['Activity']) == before, 'Resume sends no duplicate prompt')
                    capture('08-resumed')
                    if args.agent == 'claude':
                        terminal.click(30, 34)
                        paste('Use the Write tool to create hello.txt containing exactly hi. Do not run other tools.')
                        terminal.send(b'\x13', .1)
                        wait(lambda: any(r['State'] == 'pending' for r in current_thread().get('Requests', [])), 'permission card', 90)
                        capture('09-permission')
                        terminal.click_label('Yes')
                        terminal.click_label('Submit')
                        wait(lambda: current_thread()['State'] in ('idle', 'failed'), 'approved turn outcome', 90)
                        check((project / 'hello.txt').read_text().strip() == 'hi', 'explicit approval permits requested scratch file write')
                        capture('10-approved')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', str(error)
            if terminal:
                capture('failure')
            raise
        finally:
            if terminal:
                terminal.close()
            try:
                cli('server', 'stop')
                report['shutdown'] = 'confirmed'
                log = (home / 'server.log').read_text()
                pids = set(int(pid) for pid in re.findall(r'agent process started[^\n]*pid=(\d+)', log))
                if not args.unavailable_only and not pids:
                    raise AssertionError('no adapter PID records available to verify shutdown')
                for pid in pids:
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        continue
                    raise AssertionError('adapter survived confirmed server stop')
                report['adapter_processes_reaped'] = len(pids)
            except Exception as error:
                report['result'], report['shutdown'] = 'FAIL', 'unconfirmed'
                report['shutdown_error'] = str(error)
                raise
            finally:
                (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
                print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
