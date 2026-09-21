#!/usr/bin/env python3
"""Render deterministic Go View ANSI files; these are not terminal screenshots.

Requires Pillow; accepts INPUT_DIRECTORY OUTPUT_DIRECTORY [--name FILE_STEM ...].
The input is a static grid with SGR colors, not an arbitrary terminal byte stream.
Unsupported escape sequences fail closed rather than executing terminal actions.
"""
import argparse
import gzip
import html
from pathlib import Path
import re
import unicodedata

from PIL import Image, ImageDraw, ImageFont

SGR = re.compile(r"\x1b\[([0-9;:]*)m")
COLORS = ["#000000", "#cd0000", "#00cd00", "#cdcd00", "#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
          "#7f7f7f", "#ff0000", "#00ff00", "#ffff00", "#5c5cff", "#ff00ff", "#00ffff", "#ffffff"]


def indexed(n):
    if n < 16:
        return COLORS[n]
    if n >= 232:
        value = 8 + (n - 232) * 10
        return f"#{value:02x}{value:02x}{value:02x}"
    n -= 16
    levels = [0, 95, 135, 175, 215, 255]
    return "#" + "".join(f"{levels[v]:02x}" for v in (n // 36, n // 6 % 6, n % 6))


def cells(text, default_fg, default_bg):
    fg, bg, inverse = default_fg, default_bg, False
    bold = underline = False
    x = y = pos = 0
    while pos < len(text):
        char = text[pos]
        if char == "\x1b":
            match = SGR.match(text, pos)
            if not match:
                raise ValueError(f"Unsupported escape at input byte/character {pos}")
            codes = [int(n or 0) for n in match[1].replace(":", ";").split(";")]
            i = 0
            while i < len(codes):
                code = codes[i]
                if code == 0:
                    fg, bg, inverse = default_fg, default_bg, False
                    bold = underline = False
                elif code == 1:
                    bold = True
                elif code == 22:
                    bold = False
                elif code == 4:
                    underline = True
                elif code == 24:
                    underline = False
                elif code == 7:
                    inverse = True
                elif code == 27:
                    inverse = False
                elif code == 39:
                    fg = default_fg
                elif code == 49:
                    bg = default_bg
                elif 30 <= code <= 37 or 90 <= code <= 97:
                    fg = COLORS[code - 30 if code < 90 else code - 90 + 8]
                elif 40 <= code <= 47 or 100 <= code <= 107:
                    bg = COLORS[code - 40 if code < 100 else code - 100 + 8]
                elif code in (38, 48):
                    mode = codes[i + 1]
                    if mode == 2:
                        color = "#" + "".join(f"{v:02x}" for v in codes[i + 2:i + 5])
                        i += 4
                    elif mode == 5:
                        color = indexed(codes[i + 2])
                        i += 2
                    else:
                        raise ValueError(f"Unsupported color mode {mode}")
                    if code == 38:
                        fg = color
                    else:
                        bg = color
                elif code not in (1, 2, 3, 4, 22, 23, 24):
                    raise ValueError(f"Unsupported SGR {code}")
                i += 1
            pos = match.end()
            continue
        pos += 1
        if char == "\n":
            x, y = 0, y + 1
            continue
        # Private-use Nerd Font glyphs are printable; control/format characters
        # remain forbidden in this static-grid renderer.
        if unicodedata.category(char) in ("Cc", "Cf", "Cs", "Cn"):
            raise ValueError(f"Unsupported control {ord(char):04x}")
        width = 0 if unicodedata.combining(char) else (2 if unicodedata.east_asian_width(char) in ("W", "F") else 1)
        yield x, y, char, width, bg if inverse else fg, fg if inverse else bg, bold, underline
        x += width


def render(source, output, font_path):
    match = re.match(r"(\d+)x(\d+)-", source.name)
    if not match:
        raise ValueError(f"Capture filename must begin COLUMNSxROWS-: {source}")
    cols, rows = map(int, match.groups())
    cw, ch = 10, 20
    light = "lighttrue" in source.name
    fg, bg = ("#27314c", "#f4f5fa") if light else ("#d9e1f4", "#181c2c")
    parsed = list(cells(source.read_text(), fg, bg))
    image = Image.new("RGB", (cols * cw, rows * ch), bg)
    draw = ImageDraw.Draw(image)
    font = ImageFont.truetype(str(font_path), 15)
    bold_path = font_path.with_name(font_path.name.replace("-Regular", "-Bold"))
    bold_font = ImageFont.truetype(str(bold_path), 15) if bold_path.exists() else font
    rectangles, texts = [], []
    for x, y, char, width, color, background, bold, underline in parsed:
        if x + width > cols or y >= rows:
            raise ValueError(f"Cell outside {cols}x{rows}: ({x},{y}) {char!r}")
        px, py = x * cw, y * ch
        if width:
            draw.rectangle((px, py, px + width * cw - 1, py + ch - 1), fill=background)
            rectangles.append(f'<rect x="{px}" y="{py}" width="{width*cw}" height="{ch}" fill="{background}"/>')
        if char != " ":
            draw.text((px, py + 15), char, font=bold_font if bold else font, fill=color, anchor="ls")
            weight = ' font-weight="700"' if bold else ''
            texts.append(f'<text x="{px}" y="{py+15}" fill="{color}"{weight}>{html.escape(char)}</text>')
        if underline and width:
            draw.line((px, py+17, px+width*cw-1, py+17), fill=color)
            rectangles.append(f'<rect x="{px}" y="{py+17}" width="{width*cw}" height="1" fill="{color}"/>')
    output.mkdir(parents=True, exist_ok=True)
    (output / f"{source.stem}.ansi.gz").write_bytes(gzip.compress(source.read_bytes(), mtime=0))
    image.save(output / f"{source.stem}.png")
    svg = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{cols*cw}" height="{rows*ch}" viewBox="0 0 {cols*cw} {rows*ch}">'
           '<title>Deterministic Go View output, rendered with fixed cells; not a terminal screenshot</title>'
           f'<rect width="100%" height="100%" fill="{bg}"/>' + "".join(rectangles)
           + f'<g font-family="{html.escape(font.getname()[0])},monospace" font-size="15" xml:space="preserve">' + "".join(texts) + '</g></svg>')
    (output / f"{source.stem}.svg").write_text(svg)
    print(f"{source.name} -> {output / (source.stem + '.png')}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--name", action="append", help="Capture stem to render; repeatable; defaults to every .ansi")
    parser.add_argument("--font", type=Path, default=Path("/System/Library/Fonts/Menlo.ttc"))
    args = parser.parse_args()
    for source in sorted(args.input.glob("*.ansi")):
        if args.name is None or source.stem in args.name:
            render(source, args.output, args.font)
