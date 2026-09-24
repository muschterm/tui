#!/usr/bin/env python3
"""Drive a command in a real foot window with scripted keys and screenshot it.

Run inside a dedicated foot window (Hyprland + grim), never the user's own:

    TUI_GO_HOME=$(mktemp -d) setsid foot --app-id tui-probe \\
        python3 scripts/foot_capture.py OUT_DIR STEPS_JSON BINARY [ARGS...] &

STEPS_JSON is a list of [name, keys, wait_seconds]; after each step the window
whose class is ``tui-probe`` is captured to OUT_DIR/name.png. Keys go to the
child PTY, so window focus does not matter and nothing reaches other windows.
Real keyboard input is discarded after startup; terminal replies are forwarded
during the first three seconds so capability negotiation still works. Window
resizes are forwarded (SIGWINCH), since Hyprland tiles the window after launch.
Ctrl+Q detaches the TUI at the end; stop the server with ``BINARY server stop``.

Keys: F3 "\\x1bOR", F4 "\\x1bOS", Down "\\x1b[B", Enter "\\r", Esc "\\x1b".
"""
import fcntl
import json
import os
import pty
import select
import signal
import subprocess
import sys
import termios
import time
import tty

out, steps, cmd = sys.argv[1], json.loads(sys.argv[2]), sys.argv[3:]
pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)


def winch(*_):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, fcntl.ioctl(1, termios.TIOCGWINSZ, b"\0" * 8))
    os.kill(pid, signal.SIGWINCH)


signal.signal(signal.SIGWINCH, winch)
winch()
tty.setraw(0)


def geometry():
    clients = subprocess.run(["hyprctl", "clients", "-j"], capture_output=True, text=True).stdout
    for c in json.loads(clients):
        if c["class"] == "tui-probe":
            return f"{c['at'][0]},{c['at'][1]} {c['size'][0]}x{c['size'][1]}"
    raise SystemExit("tui-probe window not found")


def pump(seconds, forward=False):
    end = time.time() + seconds
    while time.time() < end:
        try:
            r, _, _ = select.select([fd, 0], [], [], 0.05)
        except InterruptedError:
            continue
        if fd in r:
            try:
                os.write(1, os.read(fd, 65536))
            except OSError:
                return
        if 0 in r:
            data = os.read(0, 1024)
            if forward:
                os.write(fd, data)


pump(3, forward=True)
for name, keys, wait in steps:
    os.write(fd, keys.encode("latin-1"))
    pump(wait)
    subprocess.run(["grim", "-g", geometry(), os.path.join(out, name + ".png")], check=True)
os.write(fd, b"\x11")
pump(1)
