# Live ACP OS-PTY captures

Captured 2026-09-22 at 120×40 in isolated application homes with existing
provider logins. The PNG/SVG images reconstruct OS-PTY output with pyte and
render it using JetBrainsMono Nerd Font Mono at 15 px in 10×20 px cells. They
are **not native terminal screenshots** or direct Go View golden files. The
run inherited `NO_COLOR=1`; the renderer's neutral canvas is not terminal color
negotiation evidence. No font is bundled.

| Frame | What it establishes |
| --- | --- |
| [Codex draft](codex/120x40-lightfalse-01-draft.png) | New thread destination is a real temporary project |
| [Agent picker](codex/120x40-lightfalse-02-agents.png) | Fixture, Claude and Codex readiness |
| [Codex models](codex/120x40-lightfalse-03-models.png) | Agent-supplied model choices |
| [Prompt](codex/120x40-lightfalse-04-prompt.png) | Typed prompt and selected settings |
| [Sent](codex/120x40-lightfalse-05-sent.png) | First-Send transition; this frame may precede adapter dispatch |
| [Reply](codex/120x40-lightfalse-06-reply.png) | Real pong, idle status and supplied usage |
| [Cancelled](codex/120x40-lightfalse-07-cancelled.png) | Confirmed cancelled outcome and Resume control |
| [Resumed](codex/120x40-lightfalse-08-resumed.png) | Gate cleared without another prompt |
| [Claude models](claude/120x40-lightfalse-03-models.png) | Reported model list despite unavailable prompt quota |
| [Claude quota failure](claude/120x40-lightfalse-06-reply.png) | Visible failure with one retained queued prompt |
| [Unavailable adapters](unavailable/120x40-lightfalse-02-unavailable.png) | Missing executables are unavailable, not ready |

Each image has a matching `.svg` and compressed `.ansi.gz` source in its
folder. [Codex report](codex/report.json) passes; [Claude report](claude/report.json)
fails on the provider's session limit; [missing-executable report](unavailable/report.json)
passes. The [validation report](../go-acp-2026-09-22.md#fixture-regressions) also
links the seven passing fixture regression reports under `fixture/`. Reports list all steps in each run; only the selected Claude/unavailable
frames above are retained. Raw PTY streams and unredacted screen reconstructions
remain in temporary artifacts, outside the repository. Exported frames redact
home/project paths and email-like strings while preserving cell counts.

From `apps/go`, rerun the commands in the
[validation report](../go-acp-2026-09-22.md#environment-and-commands). Render the
resulting static exports with:

```sh
/tmp/tui-acp-render-venv/bin/python scripts/render-capture.py \
  /tmp/tui-acp-codex-final ../../docs/research/go-acp-captures/codex \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --title 'Live OS-PTY output reconstructed with pyte; not a native terminal screenshot'
/tmp/tui-acp-render-venv/bin/python scripts/render-capture.py \
  /tmp/tui-acp-claude-final ../../docs/research/go-acp-captures/claude \
  --name 120x40-lightfalse-03-models --name 120x40-lightfalse-06-reply \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --title 'Live OS-PTY output reconstructed with pyte; not a native terminal screenshot'
/tmp/tui-acp-render-venv/bin/python scripts/render-capture.py \
  /tmp/tui-acp-unavailable-final ../../docs/research/go-acp-captures/unavailable \
  --name 120x40-lightfalse-02-unavailable \
  --font /path/to/JetBrainsMonoNerdFontMono-Regular.ttf \
  --title 'Live OS-PTY output reconstructed with pyte; not a native terminal screenshot'
```

To regenerate the images without another live prompt, decompress each `.ansi.gz`
to a temporary directory preserving its `.ansi` filename, then pass that directory
to `render-capture.py` with the same font/title arguments. Provider responses,
option catalogues, quotas and timings can differ across live runs. Successful
Claude reply and live permission-card activation remain outstanding because of
the account limit; these captures do not claim them.
