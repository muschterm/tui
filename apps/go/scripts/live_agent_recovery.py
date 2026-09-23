#!/usr/bin/env python3
"""Opt-in live HTTP native-question/recovery check using official local login.

Uses a scratch application home/checkout. No auth changes or implicit approvals.
Artifacts contain selected fields only, never raw wire traffic or credentials.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path('bin/tui-go'))
    parser.add_argument('--agent', choices=['claude', 'codex'], required=True)
    parser.add_argument('--adapter', type=Path, help='explicit external ACP adapter; omit for the shipped Go bridge')
    parser.add_argument('--model', help='explicit model ID from the live catalogue')
    parser.add_argument('--effort', help='explicit effort value from the live catalogue')
    parser.add_argument('--speed', help='explicit model-supported speed tier')
    parser.add_argument('--runtime', type=Path, required=True)
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--questions', action='store_true', help='exercise Codex native questions instead of a text-only reply')
    parser.add_argument('--permissions', help='explicit discovered permission mode for the harmless test prompts')
    args = parser.parse_args()
    question_test = args.agent == 'claude' or args.questions
    if os.environ.get('TUI_GO_LIVE_ACP') != '1':
        parser.error('set TUI_GO_LIVE_ACP=1; this check consumes provider allowance')
    binary, runtime = (p.resolve(strict=True) for p in (args.binary, args.runtime))
    adapter = args.adapter.resolve(strict=True) if args.adapter else None
    artifacts = args.artifacts.resolve()
    artifacts.mkdir(parents=True, exist_ok=True)
    report = {'agent': args.agent, 'checks': [], 'result': 'INCOMPLETE'}

    def check(ok, label):
        if not ok:
            raise AssertionError(label)
        report['checks'].append(label)

    def safe(text):
        text = str(text).replace(str(Path.home()), '<HOME>')
        return re.sub(r'/private/tmp/[^\s]+|/tmp/[^\s]+', '<SCRATCH>', text)[:4096]

    with tempfile.TemporaryDirectory(prefix='tui-agent-recovery-', dir='/tmp') as directory:
        home = Path(directory)
        project = home / 'checkout'
        project.mkdir()
        calls = home / 'runtime-calls.jsonl'
        wrapper = home / 'official-runtime'
        # Record only our selected executable and protocol flag presence. SDK
        # arguments/environment may contain secrets, so never serialize them.
        wrapper.write_text('#!/usr/bin/env python3\nimport os, sys, json\n'
                           + f'with open({str(calls)!r}, "a") as f:\n'
                           + ' f.write(json.dumps({"pid":os.getpid(),"parent":os.getppid(),'
                           + '"protocol_flags":[x for x in sys.argv[1:] if x in ["app-server","--input-format","--output-format","--resume"]]})+"\\n")\n'
                           + f'os.execv({str(runtime)!r}, [{str(runtime)!r}]+sys.argv[1:])\n')
        wrapper.chmod(0o700)
        environment = dict(os.environ, TUI_GO_HOME=str(home),
                           TUI_GO_AGENT_CLAUDE_COMMAND='/nonexistent/live-check-disabled',
                           TUI_GO_AGENT_CODEX_COMMAND='/nonexistent/live-check-disabled')
        environment['TUI_GO_AGENT_' + args.agent.upper() + '_COMMAND'] = str(adapter) if adapter else 'builtin:' + args.agent
        environment['CLAUDE_CODE_EXECUTABLE' if args.agent == 'claude' else 'CODEX_PATH'] = str(wrapper)
        # Disable adapter debug-wire logging, without altering auth/config.
        environment.pop('ACP_LOG_FILE', None)

        def cli(*words):
            return subprocess.check_output([str(binary), *words], cwd=project, env=environment,
                                           text=True, stderr=subprocess.PIPE, timeout=35)

        def api(path, data=None):
            discovery = json.loads((home / 'discovery.json').read_text())
            request = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                data=None if data is None else json.dumps(data).encode(),
                headers={'Authorization': 'Bearer ' + discovery['Token'],
                         'X-TUI-Protocol': '1', 'Content-Type': 'application/json'})
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response)

        def command(kind, **values):
            return api('command', dict(Version=1, ID=uuid.uuid4().hex, Kind=kind, **values))

        def wait(predicate, label, seconds=100):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                value = predicate()
                if value:
                    return value
                time.sleep(.2)
            raise AssertionError('timed out: ' + label)

        thread_id = None

        def thread():
            return next(t for t in api('snapshot')['threads'] if t['ID'] == thread_id)

        def terminal_or_question():
            t = thread()
            completed = t['State'] == 'idle' and not t.get('Queue') and t.get('TurnID')
            return t if completed or t['State'] in ('failed', 'interrupted') or any(r['State'] == 'pending' for r in t.get('Requests') or []) else None

        def shutdown():
            cli('server', 'stop')
            report['shutdown'] = 'confirmed'
            pids = set()
            log = (home / 'server.log').read_text() if (home / 'server.log').exists() else ''
            pids.update(int(p) for p in re.findall(r'agent process started[^\n]*pid=(\d+)', log))
            if calls.exists():
                launches = [json.loads(line) for line in calls.read_text().splitlines()]
                (artifacts / 'private-process-records.json').write_text(json.dumps(launches))
                report['runtime_launches'] = [{'protocol_flags': c['protocol_flags']} for c in launches]
                pids.update(c['pid'] for c in launches)
            deadline = time.monotonic() + 6
            for pid in pids:
                while True:
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        break
                    if time.monotonic() >= deadline:
                        raise AssertionError('owned adapter/runtime process survived stop')
                    time.sleep(.1)
            report['owned_processes_reaped'] = len(pids)

        try:
            version = subprocess.check_output([str(runtime), '--version'], text=True, stderr=subprocess.PIPE, timeout=20).strip()
            report['runtime_version'] = safe(version)
            cli('server', 'start')
            snapshot = wait(lambda: (s if all(a['State'] in ('ready', 'unavailable', 'unauthenticated') for a in s['agents']) else None)
                            if (s := api('snapshot')) else None, 'startup probe', 60)
            agent = next(a for a in snapshot['agents'] if a['ID'] == args.agent)
            report['adapter_version'], report['readiness'] = agent.get('Version'), agent['State']
            if agent['State'] != 'ready':
                report['readiness_detail'] = safe(agent.get('Detail'))
            check(agent['State'] == 'ready', 'adapter ready')
            report['settings_catalog'] = [{'ID': o['ID'], 'Current': o['Current'], 'Values': o.get('Values', [])} for o in agent['Options']]
            settings = {}
            for field in ('Model', 'Effort', 'Permissions', 'Context', 'Speed'):
                option = next((o for o in agent['Options'] if o['ID'] == agent['Fields'].get(field)), None)
                values = [v for v in option['Values'] if not v.get('Models') or settings.get('Model') in v['Models']] if option else []
                settings[field] = option['Current'] if option and any(v['Value'] == option['Current'] for v in values) else (values[0]['Value'] if values else 'unavailable')
                if field == 'Model' and option:
                    selected = args.model or settings[field] or option['Values'][0]['Value']
                    check(any(v['Value'] == selected for v in option['Values']), 'explicit model is discovered')
                    settings[field] = selected
                if field == 'Effort' and args.effort:
                    check(option is not None and any(v['Value'] == args.effort for v in values), 'explicit effort is discovered')
                    settings[field] = args.effort
                if field == 'Speed':
                    report['speed_options'] = values
                    if args.speed:
                        selected_speed = next((v['Value'] for v in values if v['Value'] == args.speed or v.get('Name', '').lower() == args.speed.lower()), None)
                        check(selected_speed is not None, 'explicit speed is supported by selected model')
                        settings[field] = selected_speed
            if args.permissions:
                option = next((o for o in agent['Options'] if o['ID'] == agent['Fields'].get('Permissions')), None)
                check(option is not None and any(v['Value'] == args.permissions for v in option['Values']), 'explicit permission mode is discovered')
                settings['Permissions'] = args.permissions
            report['selected'] = settings
            project_id = command('project.add', Path=str(project))['TargetID']
            prompt = ('Use ' + ('AskUserQuestion' if args.agent == 'claude' else 'request_user_input') + ' now to ask exactly one question: Which color? '
                      'Use header Color, single selection, choices Blue and Green with short descriptions. '
                      'Wait for my answer, then reply with exactly the selected color. Do not use any other tools or change files.') if question_test else 'Reply with exactly pong. Do not use tools or change files.'
            thread_id = command('thread.start', ProjectID=project_id, Agent=args.agent,
                                Settings=settings, Text=prompt)['TargetID']
            t = wait(terminal_or_question, 'native question or prompt outcome')
            report['first_outcome'] = {'state': t['State'], 'error': safe(t.get('Error', '')), 'queued': len(t.get('Queue') or [])}
            if t['State'] == 'failed':
                check(len(t.get('Queue') or []) == 0 and t['NeedsResume'], 'failed dispatched prompt retained outside queue behind Resume')
                check(any(a.get('Prompt', {}).get('Text') == prompt for a in t['Activity']), 'failed prompt capture retained in history')
                raise AssertionError('provider failed before requested result')
            if question_test:
                request = next(r for r in t['Requests'] if r['State'] == 'pending' and r['Kind'] == 'question')
                report['native_question'] = {'mode': request['Mode'], 'route': request['DeliveryRoute'], 'questions': request['Questions']}
                check((request['Mode'] == 'blocking' and t['State'] == 'waiting') or
                      (request['Mode'] == 'async' and t['State'] == 'running'), 'native question preserves blocking or continued-work mode')
                check(len(request['Questions']) == 1 and 'Blue' in request['Questions'][0]['Options'], 'provider supplied requested bounded form')
                answer = dict(Version=1, ID=uuid.uuid4().hex, Kind='request.answer', ThreadID=thread_id,
                              TargetID=request['ID'], Revision=request['Revision'], QuestionAnswers=[{'Choices': ['Blue']}])
                receipt = api('command', answer)
                check(api('command', answer) == receipt, 'duplicate answer command returns original receipt')
                t = wait(lambda: (v if v['State'] in ('idle', 'failed') else None) if (v := thread()) else None, 'answered turn')
                report['request_delivery'] = t['Requests'][0]['Delivery']
                check(t['State'] == 'idle', 'provider turn completed after answer')
                check(t['Requests'][0]['Delivery'] == 'acp-unconfirmed', 'generic completion did not become an authoritative answer receipt')
                check(any(a['Role'] == 'agent' and a.get('Text', '').strip() == 'Blue' for a in t['Activity']), 'provider reply corroborates selected answer')
            else:
                check(t['State'] == 'idle' and any(a['Role'] == 'agent' and a.get('Text', '').strip() == 'pong' for a in t['Activity']), 'real pong turn completed')
            report['effective'] = t['Effective']
            usage = t.get('Usage')
            report['usage'] = usage
            check(usage is not None and usage.get('Used', -1) >= 0 and usage.get('Source'), 'native context observation reaches persisted thread')
            if question_test:
                anchors = [a for a in t['Activity'] if a.get('RequestID') == request['ID']]
                check(len(anchors) == 1 and anchors[0]['Role'] == 'question-answer', 'answer has one durable conversation anchor')
            if args.speed:
                check(t['Effective']['Speed'] == settings['Speed'], 'selected speed acknowledged as effective')
            command('prompt.send', ThreadID=thread_id, Settings=settings, Text=prompt if question_test else
                    'Write a long numbered list of 1000 imaginary colors. Do not use tools.')
            if question_test:
                pending = wait(lambda: next((r for r in thread().get('Requests') or [] if r['State'] == 'pending'), None), 'second waiting question')
                check(thread()['State'] in ('running', 'waiting'), 'Stop target has a live native question')
            else:
                wait(lambda: thread()['State'] == 'running', 'running cancellation target')
            command('thread.interrupt', ThreadID=thread_id)
            wait(lambda: thread().get('StopReason') == 'cancelled', 'cancel acknowledgement')
            if question_test:
                try:
                    command('request.answer', ThreadID=thread_id, TargetID=pending['ID'], Revision=pending['Revision'],
                            QuestionAnswers=[{'Choices': ['Blue']}])
                except urllib.error.HTTPError as error:
                    check(error.code == 409, 'late answer after Stop rejected')
                else:
                    raise AssertionError('late answer after Stop accepted')
                closed = next(r for r in thread()['Requests'] if r['ID'] == pending['ID'])
                check(not closed.get('SubmissionID') and not closed.get('QuestionAnswers'), 'cancelled waiting question has no accepted answer')
            before = len(thread()['Activity'])
            command('thread.resume', ThreadID=thread_id)
            time.sleep(.5)
            check(thread()['State'] == 'idle' and len(thread()['Activity']) == before and not thread()['Queue'], 'Resume sends no duplicate prompt')
            shutdown()
            cli('server', 'start')
            t = thread()
            check(not t['Queue'], 'restart retains no dispatched queue copies')
            if question_test:
                check(t['Requests'][0]['Delivery'] == 'acp-uncertain', 'restart retains accepted answer as uncertain')
                check(api('command', answer) == receipt, 'restart retry returns receipt without replay')
                check(sum(a.get('RequestID') == request['ID'] for a in t['Activity']) == 1, 'restart retains one question history anchor')
            report['result'] = 'PASS'
        except Exception as error:
            report['result'], report['error'] = 'FAIL', safe(error)
            if isinstance(error, urllib.error.HTTPError):
                report['response_error'] = safe(error.read().decode())
            if thread_id:
                current = thread()
                report['failed_state'] = {k: current.get(k) for k in ('State', 'Error', 'StopReason', 'Requests')}
            if (home / 'server.log').exists():
                report['diagnostics'] = [safe(line) for line in (home / 'server.log').read_text().splitlines()[-20:]]
        finally:
            try:
                shutdown()
            except Exception as error:
                report['result'], report['shutdown_error'] = 'FAIL', safe(error)
            (artifacts / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report, indent=2))
    return 0 if report['result'] == 'PASS' else 1


if __name__ == '__main__':
    raise SystemExit(main())
