#!/usr/bin/env python3
"""Package raw ACP probe recordings into redacted fixtures."""
import json, os, pathlib, sys

# Usage: acp-adapter-probe-fixtures.py <scratch-dir> <fixture-out-dir>
# <scratch-dir> holds the probe's work/ and out/ directories; its absolute
# path is redacted out of the fixtures.
SCRATCH = sys.argv[1].rstrip("/")
OUT_ROOT = pathlib.Path(sys.argv[2])
SRC_ROOT = pathlib.Path(SCRATCH) / "out"

REPLACEMENTS = [
    # Add the operator account address(es) that appear in _auth/status_update.
    (os.environ.get("PROBE_REDACT_EMAIL", "\x00"), "<redacted-email>"),
    (SCRATCH + "/work/claude-cwd", "<SESSION_CWD>"),
    (SCRATCH + "/work/codex-cwd", "<SESSION_CWD>"),
    (SCRATCH, "<SCRATCH>"),
    (os.path.expanduser("~"), "<HOME>"),
]

MAX_LIST = 5


def redact_text(s: str) -> str:
    for a, b in REPLACEMENTS:
        s = s.replace(a, b)
    return s


ACCOUNT_KEYS = {"plan": "<redacted-plan>", "label": "<redacted-plan-label>",
                "organization": "<redacted-organization>"}


def trim(obj, in_auth=False):
    """Truncate long command/skill listings and redact account details."""
    if isinstance(obj, dict):
        out = {}
        auth = in_auth or "authStatus" in obj
        for k, v in obj.items():
            if auth and k in ACCOUNT_KEYS and isinstance(v, str):
                out[k] = ACCOUNT_KEYS[k]
                continue
            if k == "availableCommands" and isinstance(v, list) and len(v) > MAX_LIST:
                out[k] = [trim(x) for x in v[:MAX_LIST]]
                out["_fixtureTruncated"] = (
                    "availableCommands truncated to first %d of %d entries "
                    "(remainder were this machine's locally installed commands/skills)"
                    % (MAX_LIST, len(v))
                )
            else:
                out[k] = trim(v, auth)
        return out
    if isinstance(obj, list):
        return [trim(x, in_auth) for x in obj]
    return obj


def dump(path: pathlib.Path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    txt = json.dumps(trim(obj), indent=2, ensure_ascii=False) + "\n"
    path.write_text(redact_text(txt))


def dump_lines(path: pathlib.Path, lines):
    path.parent.mkdir(parents=True, exist_ok=True)
    buf = []
    for l in lines:
        buf.append(json.dumps(trim(l), ensure_ascii=False))
    txt = redact_text("\n".join(buf) + "\n")
    if len(txt) > 200_000:
        cut = txt[:200_000].rsplit("\n", 1)[0]
        txt = cut + '\n{"_fixtureTruncated":"stream truncated at 200 KB"}\n'
    path.write_text(txt)


def load_jsonl(p):
    return [json.loads(l) for l in p.read_text().splitlines() if l.strip()]


def req_resp(lines, phase, method):
    """Return {request, response} raw JSON-RPC messages for one call in a phase."""
    req = None
    for l in lines:
        if l["phase"] == phase and l["dir"] == "send" and l.get("msg", {}).get("method") == method:
            req = l
            break
    if req is None:
        return None
    rid = req["msg"].get("id")
    resp = None
    for l in lines:
        if l["dir"] == "recv" and l.get("msg", {}).get("id") == rid and "method" not in l["msg"]:
            resp = l
            break
    return {
        "request": req["msg"],
        "response": resp["msg"] if resp else None,
        "requestAt": req["t"],
        "responseAt": resp["t"] if resp else None,
    }


def main():
    for adapter in sorted(os.listdir(SRC_ROOT)):
        src = SRC_ROOT / adapter
        dst = OUT_ROOT / adapter
        main_lines = load_jsonl(src / "main.wire.jsonl")
        bad_lines = load_jsonl(src / "bad-version.wire.jsonl")

        dump(dst / "initialize.json", {
            "note": "session/initialize with clientCapabilities fs=false, terminal=false",
            "call": req_resp(main_lines, "initialize", "initialize"),
            "timing": json.loads((src / "initialize.json").read_text()).get("elapsedSeconds"),
        })
        dump(dst / "new-session.json", {
            "note": "session/new with cwd = a freshly git-initialised temporary directory",
            "call": req_resp(main_lines, "session-new", "session/new"),
            "timing": json.loads((src / "new-session.json").read_text()).get("elapsedSeconds"),
        })
        dump(dst / "set-config-option.json", {
            "note": "session/set_config_option switching the advertised model select",
            "summary": json.loads((src / "set-config-option.json").read_text()),
            "call": req_resp(main_lines, "set-config-option", "session/set_config_option"),
        })
        dump(dst / "bad-version.json", {
            "note": "initialize with protocolVersion 99",
            "summary": json.loads((src / "bad-version.json").read_text()),
            "wire": [l for l in bad_lines],
        })
        dump(dst / "shutdown.json", json.loads((src / "shutdown.json").read_text()))

        for phase, name in [
            ("prompt-pong", "prompt-pong.jsonl"),
            ("prompt-tool", "prompt-tool.jsonl"),
            ("prompt-cancel", "prompt-cancel.jsonl"),
        ]:
            dump_lines(dst / name, [l for l in main_lines if l["phase"] == phase])

        dump(dst / "prompt-summaries.json", {
            "prompt-pong": json.loads((src / "prompt-pong.json").read_text()),
            "prompt-tool": json.loads((src / "prompt-tool.json").read_text()),
            "prompt-cancel": json.loads((src / "prompt-cancel.json").read_text()),
        })
        dump_lines(dst / "wire-full.jsonl", main_lines)
        print("wrote", dst)


if __name__ == "__main__":
    main()
