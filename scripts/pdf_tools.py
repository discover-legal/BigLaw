#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""
PDF tools for fac-eu-brief agents — called from Node.js via child_process.

Protocol: python3 scripts/pdf_tools.py <operation> '<json_args>'
Response: single JSON object printed to stdout; errors exit 1 with JSON error.

Operations:
  extract_text    — PyMuPDF: full text extraction with page/block structure
  extract_tables  — Camelot: table extraction returning row/column data
  generate        — PyMuPDF Story: create a formatted legal PDF from structured input
"""

import sys
import json
import os
import traceback


# ─── extract_text ─────────────────────────────────────────────────────────────

def extract_text(args: dict) -> dict:
    import fitz  # PyMuPDF

    path = args["path"]
    page_range = args.get("pages")  # e.g. "1-3" or None for all

    doc = fitz.open(path)
    pages = []

    indices = _parse_page_range(page_range, len(doc)) if page_range else range(len(doc))

    for i in indices:
        page = doc[i]
        blocks = page.get_text("blocks")
        text_blocks = [
            {"text": b[4].strip(), "x0": b[0], "y0": b[1], "x1": b[2], "y1": b[3]}
            for b in blocks if b[6] == 0 and b[4].strip()  # type 0 = text
        ]
        pages.append({
            "page": i + 1,
            "text": page.get_text("text"),
            "blocks": text_blocks,
            "width": page.rect.width,
            "height": page.rect.height,
        })

    total_pages = len(doc)
    doc.close()
    return {
        "path": path,
        "pageCount": total_pages,
        "extractedPages": len(pages),
        "pages": pages,
    }


# ─── extract_tables ───────────────────────────────────────────────────────────

def extract_tables(args: dict) -> dict:
    import camelot

    path = args["path"]
    pages = args.get("pages", "all")
    # lattice: bordered tables (most legal docs); stream: whitespace-delimited
    flavor = args.get("flavor", "lattice")

    try:
        tables = camelot.read_pdf(path, pages=str(pages), flavor=flavor)
    except Exception as e:
        # Fall back to stream if lattice fails (no visible table borders)
        if flavor == "lattice":
            tables = camelot.read_pdf(path, pages=str(pages), flavor="stream")
        else:
            raise

    result = []
    for table in tables:
        df = table.df
        rows = df.values.tolist()
        result.append({
            "tableIndex": len(result),
            "page": table.page,
            "accuracy": round(table.accuracy, 2),
            "whitespace": round(table.whitespace, 2),
            "headers": rows[0] if rows else [],
            "rows": rows[1:] if len(rows) > 1 else [],
            "shape": [df.shape[0], df.shape[1]],
        })

    return {
        "path": path,
        "tableCount": len(result),
        "flavor": flavor,
        "tables": result,
    }


# ─── generate ─────────────────────────────────────────────────────────────────

_CSS = """
body { font-family: Times New Roman, serif; font-size: 11pt; line-height: 1.5; margin: 72pt; }
h1   { font-size: 16pt; font-weight: bold; margin-top: 24pt; margin-bottom: 12pt; }
h2   { font-size: 13pt; font-weight: bold; margin-top: 18pt; margin-bottom: 8pt; }
h3   { font-size: 11pt; font-weight: bold; font-style: italic; margin-top: 12pt; margin-bottom: 6pt; }
p    { margin-bottom: 8pt; text-align: justify; }
ul, ol { margin-left: 24pt; margin-bottom: 8pt; }
li   { margin-bottom: 4pt; }
blockquote { margin-left: 36pt; margin-right: 36pt; font-style: italic; }
.confidential { text-align: center; color: red; font-weight: bold; font-size: 10pt; }
.header-meta  { font-size: 9pt; color: #444; border-bottom: 1pt solid #ccc; margin-bottom: 18pt; }
"""

def generate(args: dict) -> dict:
    import fitz

    title = args.get("title", "Legal Document")
    filename = args["filename"]
    output_dir = args.get("output_dir", ".")
    author = args.get("author", "fac-eu-brief")
    confidential = args.get("confidential", False)
    content = args["content"]  # string (markdown) or list of sections

    output_path = os.path.join(output_dir, filename)
    os.makedirs(output_dir, exist_ok=True)

    # Build HTML body
    if isinstance(content, str):
        body_html = _markdown_to_html(content)
    else:
        body_html = _sections_to_html(content)

    conf_banner = '<p class="confidential">CONFIDENTIAL — LEGALLY PRIVILEGED</p>' if confidential else ""
    meta_line = f'<div class="header-meta">Author: {_esc(author)}</div>' if author else ""

    html = f"""<!DOCTYPE html>
<html><head><style>{_CSS}</style></head>
<body>
{conf_banner}
<h1>{_esc(title)}</h1>
{meta_line}
{body_html}
</body></html>"""

    # Use PyMuPDF Story for proper pagination + layout
    mediabox = fitz.paper_rect("a4")
    margins = (56, 56, 56, 56)  # left, top, right, bottom (points)
    where = mediabox + (margins[0], margins[1], -margins[2], -margins[3])

    story = fitz.Story(html=html)
    writer = fitz.DocumentWriter(output_path)

    more = True
    while more:
        device = writer.begin_page(mediabox)
        more, _ = story.place(where)
        story.draw(device)
        writer.end_page()

    writer.close()

    stat = os.stat(output_path)
    # Count pages by re-opening
    with fitz.open(output_path) as doc:
        page_count = len(doc)

    return {
        "outputPath": output_path,
        "pageCount": page_count,
        "fileSizeBytes": stat.st_size,
    }


# ─── Helpers ──────────────────────────────────────────────────────────────────

def _parse_page_range(spec: str, total: int) -> range:
    """Parse '1-3' or '2' page range (1-based) to a range of 0-based indices."""
    if "-" in spec:
        parts = spec.split("-", 1)
        start = max(1, int(parts[0])) - 1
        end = min(total, int(parts[1]))
        return range(start, end)
    return range(int(spec) - 1, int(spec))


def _esc(text: str) -> str:
    return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def _markdown_to_html(md: str) -> str:
    """Convert basic markdown to HTML — covers common legal document patterns."""
    import re
    lines = md.split("\n")
    out = []
    in_list = False

    for line in lines:
        if line.startswith("### "):
            if in_list: out.append("</ul>"); in_list = False
            out.append(f"<h3>{_esc(line[4:])}</h3>")
        elif line.startswith("## "):
            if in_list: out.append("</ul>"); in_list = False
            out.append(f"<h2>{_esc(line[3:])}</h2>")
        elif line.startswith("# "):
            if in_list: out.append("</ul>"); in_list = False
            out.append(f"<h2>{_esc(line[2:])}</h2>")  # H1 already used for doc title
        elif line.startswith("- ") or line.startswith("* "):
            if not in_list: out.append("<ul>"); in_list = True
            item = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", _esc(line[2:]))
            out.append(f"<li>{item}</li>")
        elif line.strip() == "":
            if in_list: out.append("</ul>"); in_list = False
            out.append("")
        else:
            if in_list: out.append("</ul>"); in_list = False
            # Bold + italic inline
            text = re.sub(r"\*\*(.+?)\*\*", r"<strong>\1</strong>", _esc(line))
            text = re.sub(r"\*(.+?)\*", r"<em>\1</em>", text)
            out.append(f"<p>{text}</p>")

    if in_list:
        out.append("</ul>")
    return "\n".join(out)


def _sections_to_html(sections: list) -> str:
    """Convert list of {heading, content, subsections?} to HTML."""
    parts = []
    for section in sections:
        heading = section.get("heading", "")
        content = section.get("content", "")
        if heading:
            parts.append(f"<h2>{_esc(heading)}</h2>")
        if content:
            parts.append(_markdown_to_html(content))
        for sub in section.get("subsections", []):
            sub_heading = sub.get("heading", "")
            sub_content = sub.get("content", "")
            if sub_heading:
                parts.append(f"<h3>{_esc(sub_heading)}</h3>")
            if sub_content:
                parts.append(_markdown_to_html(sub_content))
    return "\n".join(parts)


# ─── ocr ──────────────────────────────────────────────────────────────────────

def ocr(args: dict) -> dict:
    """
    OCR a PDF (via PyMuPDF page rendering) or an image file (PNG/JPEG/TIFF).
    Uses Tesseract 5 via pytesseract.

    For scanned PDFs, each page is rasterised at 300 DPI before OCR so text
    quality is consistent regardless of original scan resolution.
    """
    import pytesseract
    from PIL import Image
    import fitz  # PyMuPDF

    path = args["path"]
    lang = args.get("lang", "eng")  # Tesseract language code(s), e.g. "eng+fra"
    dpi = int(args.get("dpi", 300))
    page_range = args.get("pages")

    ext = os.path.splitext(path)[1].lower()

    if ext == ".pdf":
        doc = fitz.open(path)
        total = len(doc)
        indices = _parse_page_range(page_range, total) if page_range else range(total)
        pages_out = []

        scale = dpi / 72.0  # 72 pt/inch → target DPI
        mat = fitz.Matrix(scale, scale)

        for i in indices:
            page = doc[i]
            pix = page.get_pixmap(matrix=mat, colorspace=fitz.csRGB)
            # PIL from raw bytes — avoids writing temp files
            img = Image.frombytes("RGB", [pix.width, pix.height], pix.samples)
            text = pytesseract.image_to_string(img, lang=lang)
            pages_out.append({
                "page": i + 1,
                "text": text.strip(),
                "width_px": pix.width,
                "height_px": pix.height,
            })

        doc.close()
        full_text = "\n\n".join(p["text"] for p in pages_out if p["text"])
        return {
            "path": path,
            "type": "pdf",
            "pageCount": total,
            "extractedPages": len(pages_out),
            "lang": lang,
            "dpi": dpi,
            "text": full_text,
            "pages": pages_out,
        }

    else:
        # Direct image OCR (PNG, JPEG, TIFF, BMP, etc.)
        img = Image.open(path)
        text = pytesseract.image_to_string(img, lang=lang)
        return {
            "path": path,
            "type": "image",
            "lang": lang,
            "text": text.strip(),
        }


def render_pages(args: dict) -> dict:
    """
    Rasterise PDF pages to PNG and return them base64-encoded, for feeding to a
    vision model (the hybrid extraction pipeline's reconcile pass). No OCR or
    text heuristics here — this is pure rasterisation; the LLM does the reading.

    Args: path (str), maxPages (int, default 8), dpi (int, default 150).
    Returns: { total_pages, rendered, capped, pages: [{page, png_base64, ...}] }.
    """
    import base64 as _b64
    import fitz  # PyMuPDF

    path = args["path"]
    max_pages = int(args.get("maxPages", 8))
    dpi = int(args.get("dpi", 150))

    doc = fitz.open(path)
    total = len(doc)
    limit = total if max_pages <= 0 else min(total, max_pages)

    scale = dpi / 72.0
    mat = fitz.Matrix(scale, scale)

    pages_out = []
    for i in range(limit):
        page = doc[i]
        pix = page.get_pixmap(matrix=mat, colorspace=fitz.csRGB)
        png = pix.tobytes("png")
        pages_out.append({
            "page": i + 1,
            "png_base64": _b64.b64encode(png).decode("ascii"),
            "width_px": pix.width,
            "height_px": pix.height,
        })

    doc.close()
    return {
        "path": path,
        "total_pages": total,
        "rendered": len(pages_out),
        "capped": limit < total,
        "dpi": dpi,
        "pages": pages_out,
    }


# ─── Entry point ──────────────────────────────────────────────────────────────

if __name__ == "__main__":
    operation = sys.argv[1] if len(sys.argv) > 1 else None
    args_json = sys.argv[2] if len(sys.argv) > 2 else "{}"

    try:
        args = json.loads(args_json)

        if operation == "extract_text":
            result = extract_text(args)
        elif operation == "extract_tables":
            result = extract_tables(args)
        elif operation == "generate":
            result = generate(args)
        elif operation == "ocr":
            result = ocr(args)
        elif operation == "render_pages":
            result = render_pages(args)
        else:
            result = {"error": f"Unknown operation: {operation}"}

        print(json.dumps(result))
        sys.exit(0)

    except Exception as exc:
        # Emit only the message — not a full traceback — to avoid leaking
        # internal file paths and library internals to callers.
        traceback.print_exc(file=sys.stderr)
        print(json.dumps({
            "error": str(exc),
            "type": type(exc).__name__,
        }))
        sys.exit(1)
