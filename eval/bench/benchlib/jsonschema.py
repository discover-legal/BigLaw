# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""A small JSON Schema validator (stdlib only): the subset eval/bench/schema/*.json use.

Keywords: type (a name or a list, "integer" excludes bool), enum, const, properties, required,
additionalProperties (false or a schema), items, minItems, pattern, minimum, minLength, $ref to
"#/$defs/<name>". Any other keyword is an error in the schema itself, so a schema never relies
on a check this validator does not make.
"""

from __future__ import annotations

import re
from typing import Any

KNOWN = {"$schema", "$id", "$defs", "title", "description", "type", "enum", "const", "properties",
         "required", "additionalProperties", "items", "minItems", "pattern", "minimum", "minLength", "$ref"}

TYPES = {
    "null": lambda v: v is None,
    "boolean": lambda v: isinstance(v, bool),
    "integer": lambda v: isinstance(v, int) and not isinstance(v, bool),
    "number": lambda v: isinstance(v, (int, float)) and not isinstance(v, bool),
    "string": lambda v: isinstance(v, str),
    "array": lambda v: isinstance(v, list),
    "object": lambda v: isinstance(v, dict),
}


class SchemaError(ValueError):
    """The schema itself uses a keyword or type this validator does not know."""


def validate(doc: Any, schema: dict) -> list[str]:
    """Every violation of schema by doc, as "path: problem" lines; [] when doc is valid."""
    errs: list[str] = []
    _check(doc, schema, schema, "$", errs)
    return errs


def _resolve(root: dict, ref: str) -> dict:
    if not ref.startswith("#/$defs/"):
        raise SchemaError(f"unsupported $ref {ref!r}")
    name = ref[len("#/$defs/"):]
    try:
        return root["$defs"][name]
    except KeyError:
        raise SchemaError(f"$ref {ref!r} names no definition") from None


def _check(v: Any, s: dict, root: dict, path: str, errs: list[str]) -> None:
    unknown = set(s) - KNOWN
    if unknown:
        raise SchemaError(f"{path}: unsupported keywords {sorted(unknown)}")
    if "$ref" in s:
        _check(v, _resolve(root, s["$ref"]), root, path, errs)
        return
    if "type" in s:
        names = s["type"] if isinstance(s["type"], list) else [s["type"]]
        for n in names:
            if n not in TYPES:
                raise SchemaError(f"{path}: unknown type {n!r}")
        if not any(TYPES[n](v) for n in names):
            errs.append(f"{path}: expected {' or '.join(names)}, got {type(v).__name__}")
            return
    if "const" in s and v != s["const"]:
        errs.append(f"{path}: must be {s['const']!r}")
    if "enum" in s and v not in s["enum"]:
        errs.append(f"{path}: {v!r} is not one of {s['enum']}")
    if isinstance(v, str):
        if "minLength" in s and len(v) < s["minLength"]:
            errs.append(f"{path}: shorter than {s['minLength']}")
        if "pattern" in s and not re.search(s["pattern"], v):
            errs.append(f"{path}: {v!r} does not match {s['pattern']}")
    if TYPES["number"](v) and "minimum" in s and v < s["minimum"]:
        errs.append(f"{path}: {v} is below {s['minimum']}")
    if isinstance(v, list):
        if "minItems" in s and len(v) < s["minItems"]:
            errs.append(f"{path}: fewer than {s['minItems']} items")
        if "items" in s:
            for i, x in enumerate(v):
                _check(x, s["items"], root, f"{path}[{i}]", errs)
    if isinstance(v, dict):
        props = s.get("properties", {})
        for k in s.get("required", []):
            if k not in v:
                errs.append(f"{path}: missing {k!r}")
        extra = s.get("additionalProperties", True)
        for k, x in v.items():
            if k in props:
                _check(x, props[k], root, f"{path}.{k}", errs)
            elif extra is False:
                errs.append(f"{path}: unexpected key {k!r}")
            elif isinstance(extra, dict):
                _check(x, extra, root, f"{path}.{k}", errs)
