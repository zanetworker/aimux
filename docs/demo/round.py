"""Resize a screenshot to 1600px wide, round its corners, add a white margin.

Usage: python3 round.py IN.png OUT.png
"""
import sys
from PIL import Image, ImageDraw

src, dst = sys.argv[1], sys.argv[2]
im = Image.open(src).convert("RGB")
w = 1600
im = im.resize((w, int(im.height * w / im.width)), Image.LANCZOS)
r, pad = 24, 32
mask = Image.new("L", im.size, 0)
ImageDraw.Draw(mask).rounded_rectangle([0, 0, im.width - 1, im.height - 1], r, fill=255)
out = Image.new("RGB", (im.width + 2 * pad, im.height + 2 * pad), "white")
ImageDraw.Draw(out).rounded_rectangle([pad - 1, pad - 1, pad + im.width, pad + im.height], r + 1, outline="#D0D7DE", width=2)
out.paste(im, (pad, pad), mask)
out.save(dst, optimize=True)
