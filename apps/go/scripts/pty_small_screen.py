#!/usr/bin/env python3
"""Small-screen OS-PTY regression with synthetic activity, not iPhone evidence."""
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
    parser.add_argument('--artifacts', type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix='tui-small-screen-', dir='/tmp'))
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'harness': 'OS PTY; synthetic fixture; isolated server per size', 'checks': []}

    def check(ok, message):
        if not ok:
            raise AssertionError(message)
        report['checks'].append(message)

    try:
        for width in (47, 40):
            with tempfile.TemporaryDirectory(prefix='tui-small-home-', dir='/tmp') as directory:
                home = Path(directory)
                env = isolated_env(directory)
                identity = f'small-{width}'
                term = None

                def get(path):
                    discovery = json.loads((home / 'discovery.json').read_text())
                    request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                        headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
                    with urllib.request.urlopen(request, timeout=3) as response:
                        return json.load(response)

                def state():
                    view = get('views/' + identity)['data']
                    return view['Threads'][view['Active']]

                def request():
                    return next(r for t in get('snapshot')['threads'] if t['ID'] == 'thread-shell'
                                for r in t['Requests'] if r['ID'] == 'question-async')

                def resize(columns, rows):
                    term.resize(columns, rows)
                    term.pump(1.1)

                try:
                    subprocess.run([str(binary), 'server', 'start'], env=env,
                                   check=True, capture_output=True, text=True)
                    term = Terminal(binary, home, identity, artifacts)
                    resize(width, 22)
                    prompt = f'Keep {width} column draft'
                    term.send(prompt.encode(), 1.1)
                    screen = term.screen()
                    check(any('Reference' in row and '\uf0aa' in row for row in screen),
                          f'{width}x22: composer retains model and Send')
                    check(state()['Draft'] == prompt, f'{width}x22: composer accepts input')
                    term.click_label('Submit')
                    check(request()['State'] == 'pending' and 'answer is required' in '\n'.join(term.screen()),
                          f'{width}x22: Submit mouse target validates incomplete answers')
                    # Seed structured answers at the established wide size; exercise
                    # their preservation and explicit delivery at the small size.
                    resize(160, 50)
                    term.click_label('Keyboard flow')
                    term.click_label('Clarity')
                    term.click_label('▶')
                    # Next keeps focus on navigation; Down enters the answer field.
                    term.send(b'\x1b[B', .4)
                    term.send(b'Preserve answer draft', 1.1)
                    before = state()
                    resize(width, 22)
                    resize(35, 12)
                    resize(width, 22)
                    after = state()
                    check(after['Draft'] == prompt and after['QuestionDrafts'] == before['QuestionDrafts']
                          and after['QuestionIndex'] == before['QuestionIndex'] and request()['State'] == 'pending',
                          f'{width}x22: resize below minimum and back preserves prompt and pending answers')
                    term.key(4)
                    check('Choose column' in '\n'.join(term.screen()), f'{width}x22: F4 exposes column command')
                    term.send(b'\r', 1.1)
                    check('Projects & threads' in '\n'.join(term.screen()),
                          f'{width}x22: keyboard opens column menu')
                    term.send(b'\x1b')
                    layout = get('views/' + identity)['data']['Layout']

                    def column(label, expected, visible):
                        term.click(2, 0)
                        term.click_label(label)
                        term.pump(1.1)
                        check(state()['CompactColumn'] == expected and visible in '\n'.join(term.screen()),
                              f'{width}x22: hamburger selects {label}')
                        check(state()['Draft'] == prompt and state()['QuestionDrafts'] == before['QuestionDrafts'],
                              f'{width}x22: {label} preserves composer and answer drafts')
                        (artifacts / f'{identity}-column-{expected}.screen.txt').write_text('\n'.join(term.screen()) + '\n')

                    column('Projects & threads', 1, 'CLOSED')
                    check('Search' in '\n'.join(term.screen()),
                          f'{width}x22: navigation exposes inline thread search')
                    check('Submit' not in '\n'.join(term.screen()),
                          f'{width}x22: navigation uses available space without question card')
                    column('Surfaces', 2, 'ADD SURFACE')
                    column('Terminal', 3, 'No terminal sessions')
                    resize(160, 50)
                    check(get('views/' + identity)['data']['Layout'] == layout,
                          f'{width}x22: wide resize restores original pane preferences')
                    resize(width, 22)
                    check(state()['CompactColumn'] == 3 and 'No terminal sessions' in '\n'.join(term.screen()),
                          f'{width}x22: narrowing resumes selected Terminal column')
                    column('Conversation', 0, 'Submit')
                    if width == 47:
                        term.click_label('Submit')
                    else:
                        # F6 leaves text entry; Tab traverses the actual rendered
                        # controls. The bottom status reports the focused label.
                        term.key(6)
                        for _ in range(80):
                            term.send(b'\t', .08)
                            if 'Submit' in term.screen()[-1]:
                                break
                        else:
                            raise AssertionError('40x22: keyboard cannot reach Submit')
                        term.send(b'\r', 1.1)
                    term.pump(1.1)
                    check(request()['State'] == 'resolved'
                          and request()['QuestionAnswers'][2]['Text'] == 'Preserve answer draft'
                          and state()['Draft'] == prompt,
                          f'{width}x22: explicit {"mouse" if width == 47 else "keyboard"} Submit sends answers and preserves prompt')
                    term.click_label('\uf0aa')
                    term.pump(1.1)
                    thread = next(t for t in get('snapshot')['threads'] if t['ID'] == 'thread-shell')
                    check(state()['Draft'] == '' and any(item['Text'] == prompt
                          for item in (thread['Queue'] or []) + (thread['Activity'] or [])),
                          f'{width}x22: Send mouse target submits exact prompt')
                    (artifacts / f'{identity}-completed.screen.txt').write_text('\n'.join(term.screen()) + '\n')
                    term.send(b'\x11', .7)
                    check(b'\x1b[?1049l' in term.output and b'\x1b[?1006l' in term.output
                          and 'revision' in get('snapshot'),
                          f'{width}x22: Ctrl+Q restores terminal modes and leaves server available')
                finally:
                    if term is not None:
                        term.close()
                    subprocess.run([str(binary), 'server', 'stop'], env=env,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        report['result'] = 'PASS'
    except Exception as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
        report.update(result='FAIL', error=detail)
        raise
    finally:
        (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
