#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Draws the recorded demo's scanned exhibit (owner-run, needs Pillow; not part of CI).

The exhibit is fictional: an affidavit of service in the synthetic Greystone v. Talbot matter,
drawn from testdata/06-affidavit-of-service-scan.txt (one printed line per text line) and made to
look scanned (grey paper, a slight skew, speckle, a notary stamp, a signature). The demo recorder
ingests the JPEG through v2's vision path with a scripted reader (model.Fake) whose two readings
are that same text, so the transcript is exactly what the image shows; no model reads it.

Usage: python3 eval/demo/make_scan.py   (writes ui/src/recorded/06-affidavit-of-service-scan.jpg)
"""
import os
import random

from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
TEXT = os.path.join(HERE, "testdata", "06-affidavit-of-service-scan.txt")
OUT = os.path.join(HERE, "..", "..", "ui", "src", "recorded", "06-affidavit-of-service-scan.jpg")
FONTS = "/usr/share/fonts/truetype/liberation/"


def font(name, size):
    return ImageFont.truetype(FONTS + name, size)


def main():
    lines = open(TEXT, encoding="utf-8").read().rstrip("\n").split("\n")
    rnd = random.Random(26)
    w, h = 1275, 1650  # US Letter at 150 dpi
    page = Image.new("L", (w, h), 246)
    d = ImageDraw.Draw(page)
    body, bold, sig = font("LiberationSerif-Regular.ttf", 27), font("LiberationSerif-Bold.ttf", 29), font("LiberationSerif-Italic.ttf", 44)
    y = 130
    for i, line in enumerate(lines[:16]):
        f = bold if i in (0, 1, 4) else body
        if i in (0, 1, 2, 3, 4):  # the caption, centred
            x = (w - d.textlength(line, font=f)) / 2
        else:
            x = 150
        if i == 4:
            y += 30
        if i == 5 or i == 7 or i == 12:
            y += 22
        if i == 13:  # the signature, handwritten-looking, above its typed name
            y += 40
            d.text((170, y), line, font=sig, fill=40)
            d.line((150, y + 58, 620, y + 58), fill=60, width=2)
            y += 70
            continue
        if i == 15:
            y += 26
        d.text((x, y), line, font=f, fill=28)
        y += 40
    stamp = Image.new("L", (520, 210), 255)
    sd = ImageDraw.Draw(stamp)
    sd.rectangle((4, 4, 515, 205), outline=70, width=5)
    sy = 22
    for line, f in zip(lines[16:], (bold, bold, body, body)):
        sd.text(((520 - sd.textlength(line, font=f)) / 2, sy), line, font=f, fill=70)
        sy += 44
    stamp = stamp.rotate(-4, expand=True, fillcolor=255)
    page.paste(stamp, (650, y + 30), Image.eval(stamp, lambda v: 255 - v))
    px = page.load()
    for _ in range(9000):  # scanner speckle
        px[rnd.randrange(w), rnd.randrange(h)] = rnd.choice((150, 190, 210))
    page = page.rotate(0.6, resample=Image.BICUBIC, fillcolor=228).filter(ImageFilter.GaussianBlur(0.55))
    page.save(OUT, "JPEG", quality=72, optimize=True)
    print(OUT, os.path.getsize(OUT), "bytes")


if __name__ == "__main__":
    main()
