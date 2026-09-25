#!/usr/bin/env python3
"""Measure wheel-burst input latency in an OS PTY, not GUI terminal compatibility.

Each run owns a temporary application home and server. The raw PTY capture and
JSON report remain in --output. Child CPU covers the complete TUI lifetime;
latency starts at the first burst write and ends at alternate-screen exit.
"""
import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time
import urllib.request
from harness_env import isolated_env


ENTER = b'\x1b[?1049h'
LEAVE = b'\x1b[?1049l'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=Path(__file__).resolve().parents[1] / 'bin/tui-go')
    parser.add_argument('--events', type=int, default=500)
    parser.add_argument('--columns', type=int, default=160)
    parser.add_argument('--rows', type=int, default=50)
    parser.add_argument('--timeout', type=float, default=60)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    if args.events < 0 or args.columns < 80 or args.rows < 24 or args.timeout <= 0:
        parser.error('require events >= 0, columns >= 80, rows >= 24, timeout > 0')
    binary = args.binary.resolve(strict=True)
    output = args.output or Path(tempfile.mkdtemp(prefix='tui-scroll-evidence-', dir='/tmp'))
    output.mkdir(parents=True, exist_ok=True)
    report = {'binary': str(binary), 'events': args.events, 'columns': args.columns,
              'rows': args.rows, 'harness': 'direct child OS PTY; synthetic fixture',
              'cpu_scope': 'complete TUI child lifetime, excluding server', 'result': 'FAIL'}
    raw = bytearray()
    pid = fd = server = None
    reaped = False
    failure = None
    with tempfile.TemporaryDirectory(prefix='tui-scroll-home-', dir='/tmp') as directory:
        home = Path(directory)
        env = isolated_env(directory, TERM='xterm-256color')

        def get(path):
            discovery = json.loads((home / 'discovery.json').read_text())
            req = urllib.request.Request(discovery['URL'] + '/v1/' + path,
                headers={'Authorization': 'Bearer ' + discovery['Token'], 'X-TUI-Protocol': '1'})
            with urllib.request.urlopen(req, timeout=2) as response:
                return json.load(response)

        def read_available():
            try:
                data = os.read(fd, 65536)
            except BlockingIOError:
                return True
            except OSError as error:
                if error.errno == errno.EIO:
                    return False
                raise
            raw.extend(data)
            return bool(data)

        try:
            # Directly own the fixture server so cleanup can kill only our child.
            server = subprocess.Popen([str(binary), 'server', 'run'], env=env,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            deadline = time.monotonic() + 10
            while True:
                try:
                    get('snapshot')
                    break
                except (OSError, ValueError):
                    if time.monotonic() >= deadline or server.poll() is not None:
                        raise RuntimeError('isolated server did not become ready')
                    time.sleep(.05)
            pid, fd = pty.fork()
            if pid == 0:
                fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack('HHHH', args.rows, args.columns, 0, 0))
                os.execve(str(binary), [str(binary), '--client', 'scroll-benchmark'], env)
            os.set_blocking(fd, False)
            deadline = time.monotonic() + 10
            first_frame_at = None
            while True:
                now = time.monotonic()
                if ENTER in raw and first_frame_at is None:
                    first_frame_at = now
                # A working turn deliberately animates. Start while it is live
                # instead of waiting for terminal output to become idle.
                if first_frame_at is not None and now-first_frame_at >= .2:
                    break
                if now >= deadline:
                    raise TimeoutError('TUI did not finish its initial frame')
                if select.select([fd], [], [], .05)[0]:
                    if not read_available():
                        raise RuntimeError('TUI exited during startup')
            # Center transcript coordinates for the supported wide fixture layout.
            x, y = args.columns // 2, 5
            burst = (f'\x1b[<65;{x};{y}M'.encode() * args.events) + b'\x1b[19~\x11'
            report['input_bytes'] = len(burst)
            report['wheel_cell'] = [x, y]
            start = time.monotonic()
            deadline = start + args.timeout
            sent = 0
            capture_start = len(raw)
            while LEAVE not in raw[capture_start:]:
                remaining = deadline-time.monotonic()
                if remaining <= 0:
                    raise TimeoutError('wheel burst did not reach alternate-screen exit')
                readable, writable, _ = select.select([fd], [fd] if sent < len(burst) else [], [], min(.1, remaining))
                if writable:
                    try:
                        sent += os.write(fd, burst[sent:])
                    except BlockingIOError:
                        pass
                if readable and not read_available():
                    if LEAVE not in raw[capture_start:]:
                        raise RuntimeError('TUI exited without alternate-screen restoration')
            report['alternate_screen_exit_seconds'] = time.monotonic()-start
            report['sent_bytes'] = sent
            # Continue draining while final draft/layout flush completes.
            while time.monotonic() < deadline:
                ended, status, usage = os.wait4(pid, os.WNOHANG)
                if ended:
                    reaped = True
                    report['exit_code'] = os.waitstatus_to_exitcode(status)
                    report['child_user_seconds'] = usage.ru_utime
                    report['child_system_seconds'] = usage.ru_stime
                    report['child_cpu_seconds'] = usage.ru_utime + usage.ru_stime
                    report['process_exit_seconds'] = time.monotonic()-start
                    break
                if select.select([fd], [], [], .05)[0]:
                    read_available()
            if not reaped:
                raise TimeoutError('TUI final save/exit did not finish')
            report['light_persisted'] = get('views/scroll-benchmark')['data']['Light'] is True
            report['server_survived_detach'] = server.poll() is None and bool(get('snapshot'))
            if report['exit_code'] != 0 or not report['light_persisted'] or not report['server_survived_detach']:
                raise AssertionError('exit, persisted F8, or server-survival check failed')
            report['result'] = 'PASS'
        except Exception as error:
            failure = error
            report['error'] = str(error)
        finally:
            if pid is not None and pid != 0 and not reaped:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                _, status, usage = os.wait4(pid, 0)
                report['forced_cleanup'] = True
                report['child_cpu_seconds'] = usage.ru_utime + usage.ru_stime
            if fd is not None:
                os.close(fd)
            if server is not None:
                try:
                    subprocess.run([str(binary), 'server', 'stop'], env=env, timeout=5,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    server.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=5)
            (output / 'terminal.pty').write_bytes(raw)
            report['raw_output_bytes'] = len(raw)
            (output / 'report.json').write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps(report, indent=2))
    if failure:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
