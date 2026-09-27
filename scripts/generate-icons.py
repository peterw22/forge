#!/usr/bin/env python3
"""Draws the Forge icon and writes it in every size the clients need.

The drawing below is the source of the icon. Run this script after changing
it:

    python3 scripts/generate-icons.py

It needs Chrome or Chromium to rasterize SVG (set CHROME to its path if it is
not found) and sips, which is part of macOS, to scale.
"""
import glob
import os
import pathlib
import struct
import subprocess
import sys
import tempfile
import zlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
APP = ROOT / "flutter" / "pi_go_app"
DESIGN = ROOT / "design" / "icon"

# ---------------------------------------------------------------- drawing --
# Everything is drawn on a 1024 x 1024 canvas.

BACKGROUND_TOP = "#2f9a7b"
BACKGROUND_BOTTOM = "#124a3a"
METAL = "#ffffff"
SPARK = "#ffc24a"
SPARK_SMALL = "#ffd98a"

ANVIL = (
    "M150 400 L842 400 Q862 400 862 420 L862 452 Q862 472 842 472 L724 472 "
    "Q668 474 660 530 L660 588 Q662 642 724 650 L770 650 Q792 650 792 672 "
    "L792 716 Q792 738 770 738 L300 738 Q278 738 278 716 L278 672 "
    "Q278 650 300 650 L346 650 Q408 642 410 588 L410 552 Q408 500 352 490 "
    "C268 476 196 444 150 400Z"
)


def spark(cx, cy, radius, fill):
    """A four-point spark."""
    i = radius * 0.14
    return (
        f'<path fill="{fill}" d="M{cx} {cy - radius} '
        f"Q{cx + i} {cy - i} {cx + radius} {cy} "
        f"Q{cx + i} {cy + i} {cx} {cy + radius} "
        f"Q{cx - i} {cy + i} {cx - radius} {cy} "
        f'Q{cx - i} {cy - i} {cx} {cy - radius}Z"/>'
    )


def glyph(metal=METAL, large=SPARK, small=SPARK_SMALL):
    """The anvil and the sparks struck from it."""
    return (
        '<g transform="translate(6 40)">'
        f'<path d="{ANVIL}" fill="{metal}"/>'
        + spark(612, 236, 96, large)
        + spark(760, 300, 40, small)
        + spark(470, 300, 30, small)
        + "</g>"
    )


BACKGROUND = (
    '<defs><linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">'
    f'<stop offset="0" stop-color="{BACKGROUND_TOP}"/>'
    f'<stop offset="1" stop-color="{BACKGROUND_BOTTOM}"/>'
    "</linearGradient></defs>"
    '<rect width="1024" height="1024" fill="url(#bg)"/>'
)


def svg(body):
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" '
        f'width="1024" height="1024">{body}</svg>\n'
    )


def scaled(body, factor):
    offset = 512 * (1 - factor)
    return f'<g transform="translate({offset} {offset}) scale({factor})">{body}</g>'


def variants():
    art = BACKGROUND + glyph()
    # Launchers that mask the icon themselves crop to the middle two thirds.
    inset = scaled(glyph(), 0.6)
    return {
        # iOS and maskable web icons: the system cuts the shape.
        "full": svg(art),
        # Web, Android before 8 and the launch screen: a rounded tile.
        "tile": svg(
            '<clipPath id="c"><rect width="1024" height="1024" rx="230"/></clipPath>'
            f'<g clip-path="url(#c)">{art}</g>'
        ),
        # macOS and Linux: Apple's grid, a tile with a margin and a shadow.
        "desktop": svg(
            '<defs><filter id="s" x="-20%" y="-20%" width="140%" height="140%">'
            '<feDropShadow dx="0" dy="12" stdDeviation="14" flood-opacity=".3"/>'
            "</filter>"
            '<clipPath id="c"><rect x="100" y="100" width="824" height="824" rx="186"/></clipPath></defs>'
            '<rect x="100" y="100" width="824" height="824" rx="186" filter="url(#s)"/>'
            f'<g clip-path="url(#c)">{scaled(art, 824 / 1024)}</g>'
        ),
        # Android adaptive icon layers.
        "background": svg(BACKGROUND),
        "foreground": svg(inset),
        "monochrome": svg(scaled(glyph("#ffffff", "#ffffff", "#ffffff"), 0.6)),
    }


# ------------------------------------------------------------- rendering --


def find_chrome():
    candidates = [
        os.environ.get("CHROME", ""),
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/Applications/Chromium.app/Contents/MacOS/Chromium",
        *sorted(
            glob.glob(
                os.path.expanduser(
                    "~/Library/Caches/ms-playwright/chromium-*/chrome-mac*/"
                    "*.app/Contents/MacOS/*"
                )
            ),
            reverse=True,
        ),
    ]
    for candidate in candidates:
        if candidate and os.access(candidate, os.X_OK):
            return candidate
    sys.exit("Chrome was not found; set CHROME to its path")


def rasterize(chrome, source, target):
    """Renders an SVG at 1024 x 1024 with a transparent background."""
    page = source.with_suffix(".html")
    page.write_text(
        "<!doctype html><style>html,body{margin:0;background:transparent}"
        f'img{{display:block;width:1024px;height:1024px}}</style><img src="{source.name}">'
    )
    subprocess.run(
        [
            chrome,
            "--headless=new",
            "--disable-gpu",
            "--hide-scrollbars",
            "--force-device-scale-factor=1",
            "--default-background-color=00000000",
            "--window-size=1024,1024",
            f"--screenshot={target}",
            f"file://{page}",
        ],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def resize(source, target, size):
    target.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["sips", "-z", str(size), str(size), str(source), "--out", str(target)],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def read_png(path):
    """Decodes an 8-bit RGB or RGBA PNG without interlacing."""
    data = path.read_bytes()
    assert data[:8] == b"\x89PNG\r\n\x1a\n", path
    position, compressed, header = 8, b"", None
    while position < len(data):
        length, kind = struct.unpack(">I4s", data[position : position + 8])
        body = data[position + 8 : position + 8 + length]
        if kind == b"IHDR":
            header = struct.unpack(">IIBBBBB", body)
        elif kind == b"IDAT":
            compressed += body
        position += 12 + length
    width, height, depth, colour, _, _, interlace = header
    assert depth == 8 and colour in (2, 6) and interlace == 0, (path, header)
    channels = 4 if colour == 6 else 3
    stride = width * channels
    raw = zlib.decompress(compressed)
    rows, previous = [], bytearray(stride)
    for y in range(height):
        start = y * (stride + 1)
        kind = raw[start]
        row = bytearray(raw[start + 1 : start + 1 + stride])
        if kind == 1:
            for x in range(channels, stride):
                row[x] = (row[x] + row[x - channels]) & 255
        elif kind == 2:
            for x in range(stride):
                row[x] = (row[x] + previous[x]) & 255
        elif kind == 3:
            for x in range(stride):
                left = row[x - channels] if x >= channels else 0
                row[x] = (row[x] + ((left + previous[x]) >> 1)) & 255
        elif kind == 4:
            for x in range(stride):
                a = row[x - channels] if x >= channels else 0
                b = previous[x]
                c = previous[x - channels] if x >= channels else 0
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                nearest = a if pa <= pb and pa <= pc else b if pb <= pc else c
                row[x] = (row[x] + nearest) & 255
        rows.append(row)
        previous = row
    return width, height, channels, rows


def write_opaque(path):
    """Rewrites a PNG without an alpha channel, which iOS icons must not have."""
    width, height, channels, rows = read_png(path)
    raw = bytearray()
    for row in rows:
        raw.append(0)
        if channels == 3:
            raw += row
            continue
        for x in range(0, len(row), 4):
            raw += row[x : x + 3]

    def chunk(kind, body):
        return (
            struct.pack(">I", len(body))
            + kind
            + body
            + struct.pack(">I", zlib.crc32(kind + body) & 0xFFFFFFFF)
        )

    path.write_bytes(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(bytes(raw), 9))
        + chunk(b"IEND", b"")
    )


# ---------------------------------------------------------------- output --

IOS = APP / "ios/Runner/Assets.xcassets"
MACOS = APP / "macos/Runner/Assets.xcassets/AppIcon.appiconset"
ANDROID = APP / "android/app/src/main/res"
WEB = APP / "web"

IOS_SIZES = {
    "Icon-App-20x20@1x": 20,
    "Icon-App-20x20@2x": 40,
    "Icon-App-20x20@3x": 60,
    "Icon-App-29x29@1x": 29,
    "Icon-App-29x29@2x": 58,
    "Icon-App-29x29@3x": 87,
    "Icon-App-40x40@1x": 40,
    "Icon-App-40x40@2x": 80,
    "Icon-App-40x40@3x": 120,
    "Icon-App-60x60@2x": 120,
    "Icon-App-60x60@3x": 180,
    "Icon-App-76x76@1x": 76,
    "Icon-App-76x76@2x": 152,
    "Icon-App-83.5x83.5@2x": 167,
    "Icon-App-1024x1024@1x": 1024,
}
# Android density: legacy icon, adaptive layer.
ANDROID_SIZES = {
    "mdpi": (48, 108),
    "hdpi": (72, 162),
    "xhdpi": (96, 216),
    "xxhdpi": (144, 324),
    "xxxhdpi": (192, 432),
}
ADAPTIVE_ICON = """<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@mipmap/ic_launcher_background" />
    <foreground android:drawable="@mipmap/ic_launcher_foreground" />
    <monochrome android:drawable="@mipmap/ic_launcher_monochrome" />
</adaptive-icon>
"""


def main():
    chrome = find_chrome()
    DESIGN.mkdir(parents=True, exist_ok=True)
    drawings = variants()
    (DESIGN / "forge-icon.svg").write_text(drawings["full"])
    (DESIGN / "forge-icon-desktop.svg").write_text(drawings["desktop"])

    with tempfile.TemporaryDirectory() as directory:
        work = pathlib.Path(directory)
        master = {}
        for name, drawing in drawings.items():
            source = work / f"{name}.svg"
            source.write_text(drawing)
            master[name] = work / f"{name}.png"
            rasterize(chrome, source, master[name])

        outputs = []

        def emit(variant, target, size, opaque=False):
            resize(master[variant], target, size)
            if opaque:
                write_opaque(target)
            outputs.append(target)

        for name, size in IOS_SIZES.items():
            emit("full", IOS / "AppIcon.appiconset" / f"{name}.png", size, opaque=True)
        for suffix, scale in (("", 1), ("@2x", 2), ("@3x", 3)):
            emit("tile", IOS / "LaunchImage.imageset" / f"LaunchImage{suffix}.png", 168 * scale)

        for size in (16, 32, 64, 128, 256, 512, 1024):
            emit("desktop", MACOS / f"app_icon_{size}.png", size)
        emit("desktop", ROOT / "flatpak" / "com.tingouw.forge.png", 512)

        for density, (legacy, layer) in ANDROID_SIZES.items():
            folder = ANDROID / f"mipmap-{density}"
            emit("tile", folder / "ic_launcher.png", legacy)
            emit("background", folder / "ic_launcher_background.png", layer, opaque=True)
            emit("foreground", folder / "ic_launcher_foreground.png", layer)
            emit("monochrome", folder / "ic_launcher_monochrome.png", layer)
        adaptive = ANDROID / "mipmap-anydpi-v26" / "ic_launcher.xml"
        adaptive.parent.mkdir(parents=True, exist_ok=True)
        adaptive.write_text(ADAPTIVE_ICON)
        outputs.append(adaptive)

        emit("tile", WEB / "favicon.png", 32)
        emit("tile", WEB / "icons" / "Icon-192.png", 192)
        emit("tile", WEB / "icons" / "Icon-512.png", 512)
        emit("full", WEB / "icons" / "Icon-maskable-192.png", 192, opaque=True)
        emit("full", WEB / "icons" / "Icon-maskable-512.png", 512, opaque=True)

        emit("full", DESIGN / "forge-icon.png", 1024, opaque=True)

    print(f"wrote {len(outputs)} files")


if __name__ == "__main__":
    main()
