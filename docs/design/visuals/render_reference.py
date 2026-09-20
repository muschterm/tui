"""Render documentation mockups. Requires Python 3, Node.js, sharp, and a monospace font.

This is illustration tooling, not an application or a terminal renderer.
"""
from pathlib import Path
from html import escape
import subprocess
import os
import base64

OUT = Path(__file__).resolve().parent
CW, CH, COLS, ROWS = 10, 20, 160, 50
NODE = os.environ.get('TUI_REFERENCE_NODE', 'node')
SHARP = os.environ.get('TUI_REFERENCE_SHARP', 'sharp')

def rasterize(source, destination):
    js = "require(process.argv[1])(process.argv[2]).png().toFile(process.argv[3]).catch(e => { console.error(e.message); process.exit(1); });"
    subprocess.run([NODE, '-e', js, SHARP, str(source), str(destination)], check=True)
PALETTES = {
    'dark': dict(bg='#181b2a', nav='#111421', panel='#1d2234', text='#dce4fa', muted='#929fbd', line='#38415c', blue='#89b4fa', violet='#c3a4ff', cyan='#7dd9dd', peach='#ffbd8a', pink='#efa4cf', green='#a6da95', yellow='#f4d68d', red='#ef8d9c', user='#23314e', agent='#23243a', tool='#172633', selected='#2c3e60', input='#242c43', add='#203c36', remove='#402633'),
    'light': dict(bg='#f2f3fb', nav='#e5e7f2', panel='#eceef8', text='#283351', muted='#65708d', line='#b7bfd7', blue='#2859b5', violet='#714bb2', cyan='#13787e', peach='#9d5417', pink='#a03a78', green='#34703a', yellow='#8d651b', red='#b13a52', user='#dce6fc', agent='#ebe5f8', tool='#dcecef', selected='#c4d7fa', input='#e0e5f5', add='#d6eadc', remove='#f6dce3'),
}
# Original image artwork illustrates a bounded kitty-image placement.
ASSETS = {
    'palette-study': '''<svg xmlns="http://www.w3.org/2000/svg" width="720" height="400"><defs><linearGradient id="sky" x2="1" y2="1"><stop stop-color="#47307c"/><stop offset=".55" stop-color="#304f91"/><stop offset="1" stop-color="#3b8a99"/></linearGradient></defs><rect width="720" height="400" fill="url(#sky)"/><circle cx="526" cy="135" r="78" fill="#ffbd8a"/><circle cx="526" cy="135" r="54" fill="#ef9fb5"/><path d="M0 320L185 120 365 325 560 193 720 330V400H0Z" fill="#7484ce"/><path d="M0 342L200 257 325 346 446 237 720 351V400H0Z" fill="#3a4275"/><path d="M0 381L246 337 402 369 720 327V400H0Z" fill="#1d263f"/><rect x="30" y="29" width="6" height="60" fill="#7dd9dd"/><rect x="48" y="29" width="155" height="8" fill="#cdd6f4"/><rect x="48" y="49" width="107" height="5" fill="#aeb9dd"/><rect x="30" y="358" width="32" height="14" fill="#89b4fa"/><rect x="67" y="358" width="32" height="14" fill="#c3a4ff"/><rect x="104" y="358" width="32" height="14" fill="#7dd9dd"/><rect x="141" y="358" width="32" height="14" fill="#ffbd8a"/><rect x="178" y="358" width="32" height="14" fill="#efa4cf"/></svg>''',
}
for name, svg in ASSETS.items():
    (OUT / f'{name}.svg').write_text(svg + '\n')
    rasterize(OUT / f'{name}.svg', OUT / f'{name}.png')

ICON = {
    'plus': '<path d="M9 3v12M3 9h12"/>',
    'search': '<circle cx="7" cy="7" r="5"/><path d="M11 11l5 5"/>',
    'folder': '<path d="M2 5h6l2 2h7v9H2zM2 5V3h6l2 2"/>',
    'thread': '<path d="M2 3h14v10H7l-4 3v-3H2zM5 6h8M5 9h6"/>',
    'git': '<circle cx="5" cy="3" r="2"/><circle cx="13" cy="6" r="2"/><circle cx="5" cy="15" r="2"/><path d="M5 5v8M13 8v2q0 3-8 3"/>',
    'file': '<path d="M4 2h7l4 4v11H4zM11 2v5h4M7 10h5M7 13h5"/>',
    'terminal': '<path d="M2 2h15v15H2zM5 6l3 3-3 3M10 13h4"/>',
    'spark': '<path d="M9 1l2 6 6 2-6 2-2 6-2-6-6-2 6-2z"/>',
    'agent': '<rect x="3" y="5" width="12" height="10"/><path d="M9 1v4M1 8v4M17 8v4M6 9h1M11 9h1M6 13h6"/>',
    'check': '<path d="M3 9l4 4 8-9"/>',
    'link': '<path d="M6 11L3 14q-3-3 0-6l4-4q3-3 6 0M12 7l3-3q3 3 0 6l-4 4q-3 3-6 0M6 12l6-6"/>',
    'image': '<path d="M2 2h15v15H2zM3 15l5-6 4 4 2-3 3 5"/><circle cx="12" cy="6" r="2"/>',
    'gear': '<path d="M7 1h4l1 3 3 1 2 3-2 3-1 3-3 1-3 2-3-2-1-3-2-3 2-3 1-3z"/><circle cx="9" cy="9" r="3"/>',
    'bolt': '<path d="M10 1L3 10h5l-1 7 8-10h-5z"/>',
    'shield': '<path d="M9 1l7 3v6q-2 5-7 7-5-2-7-7V4zM5 8l3 3 5-6"/>',
    'panel-left': '<rect x="2" y="2" width="14" height="14"/><path d="M7 2v14"/><path d="M3 3h3v12H3z" fill="currentColor" opacity=".25" stroke="none"/>',
    'panel-right': '<rect x="2" y="2" width="14" height="14"/><path d="M11 2v14"/><path d="M12 3h3v12h-3z" fill="currentColor" opacity=".25" stroke="none"/>',
    'panel-bottom': '<rect x="2" y="2" width="14" height="14"/><path d="M5 2v14M13 2v14M5 11h8"/><path d="M6 12h6v3H6z" fill="currentColor" opacity=".25" stroke="none"/>',
    'close': '<path d="M5 5l8 8M13 5l-8 8"/>',
    'expand': '<path d="M3 7V3h4M11 3h4v4M15 11v4h-4M7 15H3v-4"/>',
    'restore': '<path d="M5 5V2h11v11h-3"/><rect x="2" y="5" width="11" height="11"/>',
    'more': '<circle cx="3" cy="9" r=".6"/><circle cx="9" cy="9" r=".6"/><circle cx="15" cy="9" r=".6"/>',
    'disclose': '<path d="M7 5l4 4-4 4"/>',
    'collapse': '<path d="M5 7l4 4 4-4"/>',
    'send': '<path d="M9 15V3M4 8l5-5 5 5"/>',
    'stop': '<rect x="5" y="5" width="8" height="8"/>',
    'plan': '<path d="M2 4l1 1 2-2M8 4h8M2 9l1 1 2-2M8 9h8M2 14h3M8 14h8"/>',
    'radio': '<circle cx="9" cy="9" r="5"/>',
    'radio-selected': '<circle cx="9" cy="9" r="5"/><circle cx="9" cy="9" r="2" fill="currentColor" stroke="none"/>',
}


def render(theme, surface='git', bottom=False, maximized=False, question=None):
    """Compose review states, keeping text on a nominal terminal cell grid."""
    p = PALETTES[theme]
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="{COLS*CW}" height="{ROWS*CH}" viewBox="0 0 {COLS*CW} {ROWS*CH}" role="img" aria-labelledby="title desc">',
        f'<title id="title">Workspace concept revision 3: {theme}, {surface}</title>',
        '<desc id="desc">Illustrative data, not a running app. Persistent pane controls, optional Recents, compact activity, no invented avatars, and only opened surfaces in the right sidebar. SVG control icons illustrate raster or glyph equivalents.</desc>',
    ]

    def box(c, r, w, h, fill, stroke=None):
        assert c >= 0 and r >= 0 and c+w <= COLS and r+h <= ROWS
        parts.append(f'<rect x="{c*CW}" y="{r*CH}" width="{w*CW}" height="{h*CH}" fill="{p.get(fill, fill)}"' + (f' stroke="{p[stroke]}" stroke-width="1"' if stroke else '') + '/>')

    def txt(c, r, s, color='text', bold=False):
        assert r == int(r) and c+len(s) <= COLS and 0 <= r < ROWS, (c, r, s)
        parts.append(f'<text x="{c*CW}" y="{r*CH+15}" font-family="Menlo,monospace" font-size="15" font-weight="{700 if bold else 400}" fill="{p[color]}" textLength="{len(s)*CW}" lengthAdjust="spacingAndGlyphs">{escape(s)}</text>')

    def icon(c, r, name, color='muted', label=None):
        assert r == int(r) and c+2 <= COLS
        title = f'<title>{escape(label)}</title>' if label else ''
        parts.append(f'<g transform="translate({c*CW+1} {r*CH+1})" color="{p[color]}" fill="none" stroke="{p[color]}" stroke-width="1.5" stroke-linejoin="miter" stroke-linecap="square">{title}{ICON[name]}</g>')

    def control(c, r, name, label, active=False):
        if active:
            box(c, r-.5, 4, 2, 'selected')
        icon(c+1, r, name, 'blue' if active else 'muted', label)

    def pic(c, r, w, h, name):
        b64 = base64.b64encode((OUT / f'{name}.png').read_bytes()).decode()
        parts.append(f'<image x="{c*CW}" y="{r*CH}" width="{w*CW}" height="{h*CH}" xlink:href="data:image/png;base64,{b64}" preserveAspectRatio="xMidYMid meet"/>')

    def hr(c, r, w, color='line'):
        box(c, r, w, .05, color)

    def tab(c, label, active=False):
        w = len(label)+5
        if active:
            box(c, 4.5, w, 2, 'selected')
            hr(c, 7, w, 'blue')
        txt(c+1, 5, label, 'blue' if active else 'muted', active)
        icon(c+w-3, 5, 'close', label=f'Close {label} session' if label.startswith('Terminal ') else f'Close {label}')
        return c+w

    def terminal_content(c):
        txt(c, 9, '~/tui-foundation', 'blue')
        txt(c, 11, 'Control: You', 'muted')
        txt(c, 15, '$ git status --short', 'text')
        txt(c, 17, ' M ui/theme.go', 'yellow')
        txt(c, 18, ' M ui/activity.go', 'yellow')
        txt(c, 21, '$', 'green')
        box(c+2, 21, 1, 1, 'muted')

    def active_work(c, plan_row, agents_row):
        icon(c, plan_row, 'plan', 'yellow', 'Open Plan')
        txt(c+3, plan_row, 'Plan', 'yellow')
        txt(c+10, plan_row, '2/3', 'muted')
        txt(c+16, plan_row, 'Check focus contrast', 'muted')
        icon(c, agents_row, 'agent', 'pink', 'Open Agents')
        txt(c+3, agents_row, 'Input review', 'pink')
        txt(c+21, agents_row, 'Contrast check', 'pink')

    def question_card(c, w, top, mode):
        box(c-1, top, w+2, 13, 'panel')
        hr(c-1, top, w+2, 'yellow' if mode == 'blocking' else 'cyan')
        txt(c+1, top+1, 'Codex', 'violet', True)
        txt(c+w-20, top+1, 'Waiting for answer' if mode == 'blocking' else 'Answer anytime', 'yellow' if mode == 'blocking' else 'cyan')
        txt(c+1, top+3, '1 Theme ✓', 'muted')
        txt(c+13, top+3, '2 Icons', 'blue', True)
        hr(c+13, top+4.2, 7, 'blue')
        txt(c+25, top+3, '3 Density', 'muted')
        txt(c+1, top+5, 'Which icon style should the controls use?')
        icon(c+1, top+7, 'radio-selected', 'blue')
        txt(c+4, top+7, 'Outline icons', 'blue')
        icon(c+1, top+8, 'radio')
        txt(c+4, top+8, 'Filled icons', 'muted')
        icon(c+1, top+9, 'radio')
        txt(c+4, top+9, 'Other answer...', 'muted')
        txt(c+1, top+11, 'Back', 'muted')
        txt(c+11, top+11, 'Decline', 'muted')
        txt(c+23, top+11, 'Cancel', 'muted')
        txt(c+w-22, top+11, 'Next', 'blue')
        txt(c+w-11, top+11, 'Submit', 'muted')

    def finish(stem):
        parts.append('</svg>')
        (OUT / f'{stem}.svg').write_text('\n'.join(parts)+'\n')
        rasterize(OUT / f'{stem}.svg', OUT / f'{stem}.png')
        print(stem)

    sidebar = surface != 'none'
    center_end = 114 if sidebar else 160
    x = 32 if sidebar else 49
    width = 78 if sidebar else 90
    composer = 39 if not bottom else 31
    box(0, 0, 160, 50, 'bg')
    box(0, 0, 160, 3, 'nav')
    if maximized:
        box(0, 3, 160, 41, 'panel')
    else:
        box(0, 3, 28, 46, 'nav')
        box(28, 3, .1, 46, 'line')
    if sidebar and not maximized:
        box(114, 3, 46, 46, 'panel')
        box(114, 3, .1, 46, 'line')

    # Pane controls belong to application chrome, so hiding a pane cannot hide its toggle.
    control(1, 1, 'panel-left', 'Show navigation' if maximized else 'Hide navigation', not maximized)
    txt(7, 1, 'tui-foundation', 'muted')
    txt(32, 1, 'Polish the workspace', bold=True)
    control(143, 1, 'restore' if maximized else 'expand', 'Restore right panel' if maximized else ('Maximize active surface' if surface not in ('none', 'chooser') else 'No open surface to maximize'), maximized)
    control(149, 1, 'panel-bottom', 'Hide bottom panel' if bottom else 'Show bottom panel', bottom)
    control(155, 1, 'panel-right', 'Hide surfaces' if sidebar else 'Show surfaces', sidebar)

    if maximized:
        hr(2, 7, 156)
        cursor = 2
        for label in ('Git', 'Files', 'Terminal 1', 'Terminal 2'):
            cursor = tab(cursor, label, label == 'Terminal 2')
        icon(cursor+2, 5, 'plus', 'blue', 'Add surface')
        terminal_content(3)
        if question:
            active_work(3, 24, 27)
            question_card(3, 154, 30, question)
        else:
            active_work(3, 38, 41)
        # Expanded surfaces retain the ADE prompt/configuration footer.
        box(0, 44, 160, 6, 'input')
        hr(0, 44, 160, 'violet')
        txt(3, 45, 'Ask a follow-up...', 'muted')
        icon(151, 45, 'stop', label='Interrupt active turn')
        icon(156, 45, 'send', 'blue', 'Queue prompt')
        for c, label, color in [(3, 'Codex', 'violet'), (12, 'Model example', 'blue'), (31, 'High effort', 'muted'), (47, 'Ask permissions', 'muted'), (68, '200k context', 'muted'), (86, 'Standard speed', 'muted')]:
            txt(c, 47, label, color)
        icon(156, 47, 'gear', label='Configure agent settings')
        txt(3, 49, 'Context 42%', 'cyan')
        txt(20, 49, '5h 61% left', 'muted')
        txt(37, 49, '7d 78% left', 'muted')
        finish(f'workspace-maximized-questions-{theme}' if question else f'workspace-maximized-{theme}')
        return

    # Navigation avoids a search field and one disclosure glyph per ordinary row.
    icon(3, 5, 'plus', 'blue')
    txt(6, 5, 'New thread', 'blue', True)
    txt(3, 9, 'Projects', 'muted')
    icon(23, 9, 'more', label='Navigation options; show or hide Recents')
    icon(3, 12, 'folder', 'blue')
    txt(6, 12, 'tui-foundation', bold=True)
    box(2, 14, 24, 3, 'selected')
    box(2, 14, .2, 3, 'blue')
    txt(4, 14, 'Polish the workspace', 'blue', True)
    txt(4, 15, 'Waiting for answer' if question == 'blocking' else 'Codex is working', 'muted')
    txt(4, 18, 'Git review flow', 'muted')
    txt(4, 21, 'Editor presence', 'muted')
    icon(3, 25, 'folder', 'peach')
    txt(6, 25, 'launch-notes', 'muted')
    txt(3, 30, 'Recents', 'muted')
    icon(23, 30, 'disclose', label='Expand Recents')
    icon(3, 46, 'gear')
    txt(6, 46, 'Settings', 'muted')
    txt(3, 48, 'Connected', 'green')

    # A message tint and a named agent separate authors without synthetic avatars.
    user_x = x+12
    box(user_x, 6, width-12, 5 if question else 7, 'user')
    txt(user_x+2, 7, 'You', 'blue', True)
    txt(x+width-7, 7, '14:32', 'muted')
    txt(user_x+2, 9, 'Use this palette for the workspace theme.')
    if not question:
        icon(user_x+2, 11, 'image', 'blue')
        txt(user_x+5, 11, 'palette-study.png', 'blue')
    author_row = 13 if question else 16
    txt(x, author_row, 'Codex', 'violet', True)
    if question != 'blocking':
        txt(x+9, author_row, 'Working', 'muted')
    txt(x, author_row+2, 'The palette now covers both themes. I am checking focus')
    txt(x, author_row+3, 'and selection contrast.')
    if question:
        active_work(x, composer-20, composer-17)
        question_card(x, width, composer-14, question)
    else:
        icon(x, 22, 'terminal', 'cyan')
        txt(x+3, 22, '3 tools completed', 'cyan')
        txt(x+23, 22, '1 MCP call', 'muted')
        active_work(x, composer-6, composer-3)

    # Flat, clickable configuration values replace a dropdown chevron on every field.
    box(x-1, composer, width+2, 9, 'input')
    hr(x-1, composer, width+2, 'violet')
    txt(x+1, composer+1, 'Ask a follow-up...', 'muted')
    icon(x+1, composer+3, 'plus', 'muted', 'Attach')
    txt(x+5, composer+3, '1 queued', 'yellow')
    icon(x+width-9, composer+3, 'stop', 'muted', 'Interrupt active turn')
    box(x+width-4, composer+2.5, 4, 2, 'selected')
    icon(x+width-3, composer+3, 'send', 'blue', 'Queue prompt')
    icon(x+width-3, composer+5, 'gear', 'muted', 'Configure agent settings')
    txt(x+1, composer+5, 'Codex', 'violet')
    txt(x+10, composer+5, 'Model example', 'blue')
    txt(x+29, composer+5, 'High effort', 'muted')
    txt(x+1, composer+6, 'Ask permissions', 'muted')
    txt(x+21, composer+6, '200k context', 'muted')
    txt(x+39, composer+6, 'Standard speed', 'muted')
    txt(x+1, composer+8, 'Context 42%', 'cyan')
    txt(x+18, composer+8, '5h 61% left', 'muted')
    txt(x+35, composer+8, '7d 78% left', 'muted')
    if bottom:
        hr(28, 41, center_end-28)
        icon(31, 42, 'terminal', 'cyan')
        txt(34, 42, 'Terminal 2', 'cyan')
        txt(83, 42, 'Control: You', 'muted')
        icon(109, 42, 'close', label='Close Terminal 2 session')
        txt(32, 45, '~/tui-foundation', 'blue')
        txt(32, 46, '$ git status --short', 'muted')
        txt(32, 47, ' M docs/design.md', 'yellow')

    # Tabs are opened instances. Terminal creation can produce more than one.
    if sidebar:
        if surface == 'chooser':
            txt(118, 5, 'Add a surface', bold=True)
            icon(155, 5, 'close', label='Dismiss chooser and hide empty sidebar')
            for row, name, kind, detail, color in [
                (9, 'Files', 'folder', 'Browse, edit and preview', 'blue'),
                (15, 'Git', 'git', 'Changes and commit history', 'pink'),
                (21, 'Terminal', 'terminal', 'New shell', 'cyan'),
                (27, 'Agents', 'agent', 'Runs and transcripts', 'pink'),
                (33, 'Plan', 'plan', 'Steps and progress', 'yellow'),
                (39, 'Activity', 'terminal', 'Tools and MCP', 'cyan'),
            ]:
                if kind == 'folder':
                    box(117, row-1, 40, 4, 'selected')
                icon(119, row, kind, color)
                txt(123, row, name, color, True)
                txt(123, row+1, detail, 'muted')
        else:
            hr(117, 7, 40)
            if surface == 'terminal':
                labels = ('Git', 'Terminal 1', 'Terminal 2')
            elif surface in ('agents', 'plan'):
                labels = ('Git', 'Agents', 'Plan')
            else:
                labels = ('Git', 'Files', 'Terminal 1')
            selected = {'git': 'Git', 'files': 'Files', 'terminal': 'Terminal 2', 'agents': 'Agents', 'plan': 'Plan'}[surface]
            cursor = 116
            for label in labels:
                cursor = tab(cursor, label, label == selected)
            icon(155, 5, 'plus', 'blue', 'Add surface')
            if surface == 'git':
                txt(118, 9, 'Changes', 'muted')
                txt(130, 9, 'History', 'pink', True)
                hr(130, 10.4, 7, 'pink')
                txt(118, 12, 'main', 'blue')
                txt(132, 12, 'Pull ff-only', 'muted')
                icon(154, 12, 'more', label='Git actions, including rebase')
                box(116, 16, 42, 3, 'selected')
                for row, message, color in [
                    (16, '●  Refine activity surfaces', 'blue'),
                    (17, '│  f84a21c  main', 'muted'),
                    (20, '│ ●  Editor presence', 'pink'),
                    (21, '│ │  93bb172', 'muted'),
                    (22, '├─╯', 'pink'),
                    (24, '●  Shared workspace shell', 'violet'),
                    (25, '│  a7c29e4', 'muted'),
                ]:
                    txt(118, row, message, color)
                hr(117, 29, 40)
                txt(118, 31, 'Refine activity surfaces', bold=True)
                txt(118, 33, 'f84a21c', 'muted')
                txt(129, 33, '3 files', 'muted')
                txt(142, 33, '+32', 'green')
                txt(148, 33, '-8', 'red')
                for row, name in [(36, 'ui/theme.go'), (38, 'ui/activity.go'), (40, 'docs/design.md')]:
                    icon(118, row, 'file', 'muted')
                    txt(121, row, name, 'muted')
                txt(118, 44, 'Open commit diff', 'blue')
            elif surface == 'files':
                icon(118, 9, 'collapse', label='Collapse ui folder')
                txt(121, 9, 'ui', 'blue')
                icon(121, 11, 'file')
                txt(124, 11, 'theme.go')
                icon(121, 13, 'file')
                txt(124, 13, 'activity.go')
                icon(118, 16, 'collapse', label='Collapse assets folder')
                txt(121, 16, 'assets', 'peach')
                box(117, 18, 40, 2, 'selected')
                icon(121, 18, 'image', 'blue')
                txt(124, 18, 'palette-study.png', 'blue')
                hr(117, 22, 40)
                txt(118, 24, 'palette-study.png', bold=True)
                pic(118, 27, 38, 11, 'palette-study')
                txt(118, 40, '720 x 400', 'muted')
                txt(145, 40, 'PNG', 'muted')
                txt(118, 44, 'Fit to pane', 'blue')
            elif surface == 'terminal':
                terminal_content(118)
            elif surface == 'agents':
                for row, name, state, color in [(9, 'Input review', 'Running', 'pink'), (13, 'Contrast check', 'Running', 'pink'), (17, 'Docs review', 'Complete', 'muted')]:
                    if row == 9:
                        box(117, row-.5, 40, 3, 'selected')
                    txt(119, row, name, color, row == 9)
                    txt(119, row+1, state, 'muted')
                hr(117, 22, 40)
                txt(118, 24, 'Input review', 'pink', True)
                txt(118, 27, 'Check keyboard focus and selection.')
                txt(118, 31, 'Codex', 'violet', True)
                txt(118, 33, 'Focus remains visible in both themes.')
                icon(118, 36, 'terminal', 'cyan')
                txt(121, 36, 'Read ui/theme.go', 'cyan')
                txt(118, 40, 'Checking the selected-tab colors.', 'muted')
            else:
                txt(118, 10, 'Workspace theme', 'yellow', True)
                txt(118, 12, '2 of 3', 'muted')
                for row, name, state, kind, color in [(17, 'Define the palette', 'Complete', 'check', 'green'), (23, 'Apply both themes', 'Complete', 'check', 'green'), (29, 'Check focus contrast', 'In progress', 'bolt', 'yellow')]:
                    icon(118, row, kind, color)
                    txt(121, row, name, 'text')
                    txt(121, row+1, state, 'muted')

    if question:
        stem = f'workspace-questions-{question}-{theme}'
    elif bottom:
        stem = f'workspace-bottom-{theme}'
    elif surface == 'git':
        stem = f'workspace-{theme}'
    else:
        stem = f'workspace-{surface}-{theme}'
    finish(stem)


render('dark')
render('light')
render('dark', 'files')
render('dark', 'chooser')
render('dark', 'none')
render('dark', bottom=True)
render('dark', 'terminal')
render('dark', 'terminal', maximized=True)
render('dark', 'agents')
render('dark', 'plan')
render('dark', 'agents', question='blocking')
render('dark', 'plan', question='async')
render('dark', 'terminal', maximized=True, question='async')
