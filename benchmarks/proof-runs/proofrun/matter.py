"""Load proof-run matters: the registry (matters.json), each matter's task.json,
answer_key.json and source documents."""

from __future__ import annotations

import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent          # benchmarks/proof-runs
TASKS = ROOT / "tasks"
REGISTRY = ROOT / "matters.json"
LABDRIVER_PARENT = ROOT.parent / "harvey-lab"          # reuse the LAB driver package


def labdriver():
    """Import benchmarks/harvey-lab/labdriver (the shared ingest/submit/poll/render driver)."""
    if str(LABDRIVER_PARENT) not in sys.path:
        sys.path.insert(0, str(LABDRIVER_PARENT))
    import labdriver  # noqa: F401  (imported for side effect of package init)
    from labdriver import driver, convert, biglaw  # noqa: F401
    return labdriver


@dataclass
class Matter:
    id: str                     # task path relative to tasks/, e.g. corporate-ma/kestrel-buyside-diligence
    entry: dict                 # matters.json registry entry
    task: dict                  # task.json
    key: dict                   # answer_key.json
    documents: dict[str, str] = field(default_factory=dict)  # filename -> text (as ingested)

    @property
    def dir(self) -> Path:
        return TASKS / self.id


def load_registry() -> list[dict]:
    data = json.loads(REGISTRY.read_text(encoding="utf-8"))
    return data["matters"]


def load_documents(task_dir: Path) -> dict[str, str]:
    """Convert documents exactly as the driver will before ingest."""
    convert = labdriver().convert
    converted, skipped = convert.convert_documents(task_dir / "documents")
    if skipped:
        raise RuntimeError(f"{task_dir.name}: documents could not be converted: {skipped}")
    return dict(converted)


def load_matter(matter_id: str, registry: list[dict] | None = None) -> Matter:
    registry = registry if registry is not None else load_registry()
    entry = next((m for m in registry if m["id"] == matter_id or m.get("slug") == matter_id), None)
    if entry is None:
        raise KeyError(f"unknown matter {matter_id!r}; see matters.json")
    task_dir = TASKS / entry["id"]
    task = json.loads((task_dir / "task.json").read_text(encoding="utf-8"))
    key = json.loads((task_dir / "answer_key.json").read_text(encoding="utf-8"))
    return Matter(id=entry["id"], entry=entry, task=task, key=key, documents=load_documents(task_dir))


def load_all(selected: list[str] | None = None) -> list[Matter]:
    reg = load_registry()
    ids = selected or [m["id"] for m in reg]
    return [load_matter(i, reg) for i in ids]
