# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""The pinned bench config: load it, check its hash and rules, and handle the API key.

The config file carries its own hash (`config_hash`, sha256 of its canonical JSON without that
field). A run refuses a config whose content no longer matches its hash, so a setting cannot be
changed by hand between runs without re-pinning (`run.py pin`), and every summary records the hash
it ran under: two summaries compare only when their hashes agree (compare.py).
"""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from urllib.parse import urlparse

from .jsonschema import validate

BENCH = Path(__file__).resolve().parent.parent          # eval/bench/
SCHEMAS = BENCH / "schema"
CONFIG = BENCH / "config.json"
SMOKE_CONFIG = BENCH / "smoke.json"

KEY_ENV = "ANTHROPIC_API_KEY"
KEY_FILE_ENV = "ANTHROPIC_API_KEY_FILE"   # a path to a file holding the key, instead of the key itself


class ConfigError(ValueError):
    pass


def canonical(cfg: dict) -> str:
    """The config as hashed: every key but config_hash, sorted, no white space."""
    body = {k: v for k, v in cfg.items() if k != "config_hash"}
    return json.dumps(body, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def config_hash(cfg: dict) -> str:
    return "sha256:" + hashlib.sha256(canonical(cfg).encode("utf-8")).hexdigest()


def load_schema(name: str) -> dict:
    return json.loads((SCHEMAS / name).read_text(encoding="utf-8"))


def check(cfg: dict) -> list[str]:
    """Every problem with cfg: its schema, its hash, and the rules of its mode."""
    errs = validate(cfg, load_schema("config.schema.json"))
    if errs:
        return errs
    if cfg["config_hash"] != config_hash(cfg):
        errs.append(f"config_hash {cfg['config_hash']} does not match the content ({config_hash(cfg)}): "
                    "a setting was edited without re-pinning; run `python3 eval/bench/run.py pin` and commit "
                    "(results under the old hash are then not comparable with new ones)")
    roles = cfg["models"]
    if roles.get("lead") is None:
        errs.append("models.lead is required")
    for role, mid in roles.items():
        if mid is not None and role not in ("lead", "specialist", "critic"):
            errs.append(f"models.{role}: only lead, specialist and critic may be set (vision/embed change the search "
                        "mode and what is read; not part of this suite)")
    if cfg["search"] == "lexical" and roles.get("embed"):
        errs.append("search is lexical but an embed role is set")
    ids = [t["id"] for t in cfg["tasks"]]
    if len(set(ids)) != len(ids):
        errs.append(f"task ids repeat: {ids}")
    per_task = [task_reserve(cfg, t) for t in cfg["tasks"]]
    if cfg["spend_cap_usd"] < min(per_task, default=0):
        errs.append(f"spend_cap_usd {cfg['spend_cap_usd']} cannot fund even one task (each reserves {min(per_task)})")
    if cfg["mode"] == "real":
        u = urlparse(cfg["model_url"])
        if u.scheme != "https" or not u.hostname:
            errs.append(f"model_url {cfg['model_url']!r} must be an https URL")
        elif "openai" in u.hostname:
            errs.append("model_url points at OpenAI: this suite runs no OpenAI models")
        for role, mid in roles.items():
            if mid is not None and not mid.startswith("claude-"):
                errs.append(f"models.{role} {mid!r} is not a claude- model id (no OpenAI models in this suite)")
        if cfg["judge"]["runner"] != "harvey-labs":
            errs.append("a real run is judged by harvey-labs (judge.runner)")
        if any(t["kind"] == "lab" for t in cfg["tasks"]) and not re.fullmatch(r"[0-9a-f]{7,40}", cfg["harness"]["commit"] or ""):
            errs.append("harness.commit must pin a harvey-labs commit")
    return errs


def load(path: Path) -> dict:
    """The config at path, refused (ConfigError naming every problem) unless it checks clean."""
    try:
        cfg = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise ConfigError(f"{path}: cannot read the config: {e}") from None
    errs = check(cfg)
    if errs:
        raise ConfigError(f"{path}:\n  - " + "\n  - ".join(errs))
    return cfg


def pin(path: Path) -> str:
    """Rewrite path's config_hash to match its content (an owner's deliberate change); returns it."""
    cfg = json.loads(Path(path).read_text(encoding="utf-8"))
    cfg["config_hash"] = config_hash(cfg)
    Path(path).write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return cfg["config_hash"]


def task_reserve(cfg: dict, task: dict) -> float:
    """What a task may spend at most: BigLaw's per-task USD budget, plus the judge's reserve for a LAB
    task (harvey-labs reports no judge cost, so the reserve is charged whatever the judge spends)."""
    return float(cfg["budget"]["usd"]) + (float(cfg["judge"]["reserve_usd"]) if task["kind"] == "lab" else 0.0)


def models_env(cfg: dict) -> str:
    """BIGLAW_MODELS for serve: every set role, in a fixed order."""
    roles = cfg["models"]
    return ",".join(f"{r}={roles[r]}" for r in ("lead", "specialist", "critic") if roles.get(r))


def refuse_env(env: dict) -> list[str]:
    """Problems with the caller's environment for a real run: any OPENAI_* setting (names only)."""
    bad = sorted(k for k in env if k.upper().startswith("OPENAI_"))
    return [f"OPENAI_* settings are refused (this suite runs no OpenAI): unset {', '.join(bad)}"] if bad else []


def load_key(env: dict) -> str:
    """The API key from ANTHROPIC_API_KEY, or from the file ANTHROPIC_API_KEY_FILE names. The key is
    never printed: errors name only the variable."""
    direct, path = env.get(KEY_ENV, ""), env.get(KEY_FILE_ENV, "")
    if direct and path:
        raise ConfigError(f"set {KEY_ENV} or {KEY_FILE_ENV}, not both")
    if path:
        try:
            direct = Path(path).expanduser().read_text(encoding="utf-8").strip()
        except OSError as e:
            raise ConfigError(f"{KEY_FILE_ENV}: cannot read the key file ({e.strerror})") from None
    direct = direct.strip()
    if not direct:
        raise ConfigError(f"no API key: set {KEY_ENV} (or {KEY_FILE_ENV} to a file holding it)")
    if re.search(r"\s", direct) or not direct.isprintable():
        raise ConfigError(f"the key in {KEY_FILE_ENV if path else KEY_ENV} holds white space or a control character (value not shown)")
    return direct


class Redactor:
    """Replaces every exact copy of the secrets in text written to a log (belt and braces: nothing
    the bench writes should hold the key in the first place)."""

    def __init__(self, *secrets: str):
        self.secrets = [s for s in secrets if s]

    def __call__(self, text: str) -> str:
        for s in self.secrets:
            text = text.replace(s, "[redacted]")
        return text
