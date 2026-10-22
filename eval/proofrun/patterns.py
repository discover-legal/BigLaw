# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Deterministic text matching for proof-run answer keys.

An answer key never asks a model to judge a model. Every criterion is a
pattern evaluated mechanically against a *unit* of the deliverable (one
paragraph, one bullet, or one table row), so a score is reproducible from
the run directory alone.

Pattern grammar (used in answer_key.json ``detect`` / ``forbid`` blocks)::

    {"all": [ [alt, alt, ...], [alt, ...], ... ]}

Every group in ``all`` must match the same unit; a group matches when any of
its alternatives does. An alternative is one of:

- plain text            case-insensitive substring after normalisation
- ``re:<regex>``        Python regex, case-insensitive, on the normalised unit
- ``$money:<n>``        a US-dollar/euro amount in any common rendering
                        ($9,000,000 / $9 million / $9.0m / 9,000,000 / EUR 9m)
- ``$date:<YYYY-MM-DD>`` a calendar date in any common rendering
                        (November 12, 2026 / Nov. 12, 2026 / 12 November 2026 /
                        2026-11-12 / 11/12/2026)
- ``$pct:<n>``          a percentage (31% / 31 percent / 31 per cent)
- ``$num:<n>``          an integer with or without thousands separators

Normalisation lowercases, folds smart quotes/dashes/nbsp to ASCII, strips
markdown emphasis and collapses whitespace — applied to both sides.
"""

from __future__ import annotations

import datetime as _dt
import re
from functools import lru_cache

_MD_EMPH = re.compile(r"(\*\*|__|`)")
_WS = re.compile(r"\s+")
_FOLD = str.maketrans({
    "‘": "'", "’": "'", "‚": "'", "‛": "'",
    "“": '"', "”": '"', "„": '"', "«": '"', "»": '"',
    "‐": "-", "‑": "-", "‒": "-", "–": "-", "—": "-", "−": "-",
    " ": " ", " ": " ", " ": " ", "…": "...",
    "§": "s.",  # § → s. so "§ 802.51" and "s. 802.51" both normalise alike
})


def normalize(text: str) -> str:
    """Lowercase, fold typography to ASCII, drop md emphasis, collapse spaces."""
    text = (text or "").translate(_FOLD)
    text = _MD_EMPH.sub("", text)
    return _WS.sub(" ", text).strip().lower()


def normalize_quote(text: str) -> str:
    """Normalisation for verbatim-citation checks: as normalize(), plus
    markdown table pipes / heading hashes / list bullets removed and
    surrounding quote marks trimmed, so a quote lifted from a rendered
    markdown table still matches the source text."""
    t = normalize(text)
    t = re.sub(r"[|#>]", " ", t)
    t = re.sub(r"(^|\s)[-*+](?=\s)", " ", t)
    t = _WS.sub(" ", t).strip()
    return t.strip(" \"'.,;:")


# ─── macro expansion ─────────────────────────────────────────────────────────

_MONTHS = ["january", "february", "march", "april", "may", "june", "july",
           "august", "september", "october", "november", "december"]
_MON_ABBR = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sept?", "oct", "nov", "dec"]


def _money_regex(value: float) -> str:
    n = float(value)
    whole = int(round(n))
    alts = []
    comma = f"{whole:,}"
    alts.append(r"(?<![\d,.])" + re.escape(comma) + r"(?:\.0+)?(?![\d,])")
    alts.append(r"(?<![\d,.])" + str(whole) + r"(?![\d,])")
    for unit, div in (("million|mn|m", 1_000_000), ("billion|bn|b", 1_000_000_000), ("thousand|k", 1_000)):
        if whole >= div and whole % (div // 100 or 1) == 0:
            scaled = whole / div
            reps = {f"{scaled:g}"}
            if scaled == int(scaled):
                reps.add(f"{int(scaled)}.0")
                reps.add(f"{int(scaled)}.00")
            else:
                reps.add(f"{scaled:.2f}")
                reps.add(f"{scaled:.1f}" if round(scaled, 1) == scaled else f"{scaled:g}")
            for r in sorted(reps):
                alts.append(r"(?<![\d.])" + re.escape(r) + r"\s?(?:" + unit + r")\b")
    return "(?:" + "|".join(alts) + ")"


def _date_regex(iso: str) -> str:
    d = _dt.date.fromisoformat(iso)
    month, mabbr = _MONTHS[d.month - 1], _MON_ABBR[d.month - 1]
    day = str(d.day)
    mon = rf"(?:{month}|{mabbr}\.?)"
    alts = [
        rf"{mon}\s+{day}(?:st|nd|rd|th)?,?\s+{d.year}",
        rf"(?<!\d){day}(?:st|nd|rd|th)?\s+(?:of\s+)?{mon},?\s+{d.year}",
        rf"{d.year}-{d.month:02d}-{d.day:02d}",
        rf"(?<!\d)0?{d.month}/0?{d.day}/(?:{d.year}|{str(d.year)[2:]})(?!\d)",
    ]
    return "(?:" + "|".join(alts) + ")"


def _pct_regex(value: float) -> str:
    v = f"{float(value):g}"
    return rf"(?<![\d.]){re.escape(v)}(?:\.0+)?\s?(?:%|percent\b|per cent\b)"


def _num_regex(value: int) -> str:
    v = int(value)
    return rf"(?<![\d,.])(?:{re.escape(f'{v:,}')}|{v})(?![\d,])"


@lru_cache(maxsize=4096)
def compile_alt(alt: str) -> re.Pattern:
    if alt.startswith("re:"):
        return re.compile(alt[3:], re.IGNORECASE)
    if alt.startswith("$money:"):
        return re.compile(_money_regex(float(alt[7:])), re.IGNORECASE)
    if alt.startswith("$date:"):
        return re.compile(_date_regex(alt[6:]), re.IGNORECASE)
    if alt.startswith("$pct:"):
        return re.compile(_pct_regex(float(alt[5:])), re.IGNORECASE)
    if alt.startswith("$num:"):
        return re.compile(_num_regex(int(alt[5:])), re.IGNORECASE)
    return re.compile(re.escape(normalize(alt)), re.IGNORECASE)


def alt_matches(alt: str, norm_unit: str) -> bool:
    return compile_alt(alt).search(norm_unit) is not None


def group_matches(group: list[str], norm_unit: str) -> bool:
    return any(alt_matches(a, norm_unit) for a in group)


def pattern_matches(pattern: dict, unit: str, normalized: bool = False) -> bool:
    """True if every group in pattern['all'] matches this unit."""
    norm = unit if normalized else normalize(unit)
    groups = pattern.get("all") or []
    return bool(groups) and all(group_matches(g, norm) for g in groups)


def validate_pattern(pattern: dict) -> list[str]:
    """Return a list of problems with a pattern block (empty when valid)."""
    errs = []
    groups = pattern.get("all")
    if not isinstance(groups, list) or not groups:
        return ["pattern needs a non-empty 'all' list of groups"]
    for i, g in enumerate(groups):
        if not isinstance(g, list) or not g or not all(isinstance(a, str) and a for a in g):
            errs.append(f"group {i} must be a non-empty list of non-empty strings")
            continue
        for a in g:
            try:
                compile_alt(a)
            except (re.error, ValueError) as e:
                errs.append(f"group {i} alternative {a!r}: {e}")
    return errs


# ─── unit splitting ──────────────────────────────────────────────────────────

_TABLE_SEP = re.compile(r"^\s*\|?\s*:?-{3,}")
_BULLET = re.compile(r"^\s*(?:[-*+]|\d+[.)])\s+")


def split_units(text: str) -> list[str]:
    """Split a deliverable into scoring units: paragraphs, with each bullet /
    numbered item and each markdown table row as its own unit. Headings are
    kept as a prefix of the paragraph that follows them, so a fact stated as
    '## Norrland supply agreement' + 'Termination on change of control...'
    is one unit."""
    units: list[str] = []
    buf: list[str] = []
    heading = ""

    def flush():
        if buf:
            body = " ".join(s.strip() for s in buf).strip()
            if body:
                units.append((heading + " — " + body) if heading else body)
            buf.clear()

    for line in (text or "").splitlines():
        s = line.strip()
        if not s:
            flush()
            continue
        if s.startswith("#"):
            flush()
            heading = s.lstrip("#").strip()
            continue
        if s.startswith("|"):
            flush()
            if not _TABLE_SEP.match(s):
                units.append((heading + " — " + s) if heading else s)
            continue
        if _BULLET.match(s):
            flush()
            buf.append(s)
            continue
        buf.append(s)
    flush()
    return units
