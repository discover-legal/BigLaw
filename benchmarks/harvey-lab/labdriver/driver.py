"""Run a Harvey LAB task through BigLaw and write a LAB-compatible run dir.

Usage (from benchmarks/harvey-lab/):

    python run.py --labs-dir ~/harvey-labs \\
        --task corporate-ma/review-data-room-red-flag-review

Then score with Harvey's eval phase, unchanged, from the harvey-labs checkout:

    uv run python -m evaluation.run_eval --run-id <printed-run-id> --task <task>
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from .biglaw import BigLawClient, BigLawError
from .convert import convert_documents
from .deliver import render_deliverable, split_by_markers, strip_markers

# LAB work_type → BigLaw WorkflowType. Overridable with --workflow.
WORK_TYPE_TO_WORKFLOW = {
    "analyze": "roundtable",
    "draft": "roundtable",
    "review": "review",
    "research": "full_bench",
}

MARKER_INSTRUCTION = (
    "Structure your final work product as one section per deliverable. Begin "
    "each section with a line containing exactly:\n=== DELIVERABLE: <filename> ===\n"
    "using the deliverable filenames listed above, and put that deliverable's "
    "complete, self-contained content in its section."
)


def deliverable_names(task_cfg: dict) -> list[str]:
    """Normalise task.json deliverables (strings or objects) to filenames."""
    names = []
    for d in task_cfg.get("deliverables") or []:
        if isinstance(d, str):
            names.append(d)
        elif isinstance(d, dict):
            name = d.get("filename") or d.get("file") or d.get("name") or d.get("path")
            if name:
                names.append(name)
    return names or ["deliverable.md"]


def pick_workflow(task_cfg: dict, override: str | None, deliverables: list[str]) -> str:
    if override:
        return override
    # A spreadsheet deliverable maps naturally onto BigLaw's tabulate
    # workflow, whose structured Task.table feeds the .xlsx renderer.
    if any(d.lower().endswith((".xlsx", ".xlsm")) for d in deliverables):
        return "tabulate"
    return WORK_TYPE_TO_WORKFLOW.get(str(task_cfg.get("work_type", "")).lower(), "roundtable")


def build_description(
    task_cfg: dict,
    doc_names: list[str],
    deliverables: list[str],
    use_markers: bool,
) -> str:
    parts = [str(task_cfg.get("instructions", "")).strip()]
    if doc_names:
        parts.append(
            "Source documents (ingested into the knowledge store, searchable by name):\n"
            + "\n".join(f"- {n}" for n in doc_names)
        )
    parts.append(
        "Expected deliverable(s): "
        + ", ".join(deliverables)
        + ". Produce a complete, self-contained work product covering every deliverable."
    )
    if use_markers:
        parts.append(MARKER_INSTRUCTION)
    return "\n\n".join(p for p in parts if p)


def run_one_biglaw_task(
    client: BigLawClient,
    description: str,
    workflow: str,
    doc_ids: list[str],
    args: argparse.Namespace,
    log,
    gates: list[dict],
) -> dict:
    """Submit one BigLaw task, resolve its gates per policy, return the final task."""
    task = client.submit_task(description, workflow, doc_ids, args.jurisdiction)
    task_id = task["id"]
    log("task_submitted", {"taskId": task_id, "workflow": workflow, "gatePolicy": args.gate_policy})

    def on_event(kind: str, payload: dict) -> None:
        if kind == "gate_resolved":
            gates.append({"taskId": task_id, **payload})
        log(kind, payload)

    final = client.wait_for_task(
        task_id, args.poll, args.timeout, on_event=on_event, gate_policy=args.gate_policy
    )
    if not (final.get("output") or "").strip():
        raise BigLawError(f"task {task_id} completed with an empty synthesis")
    return final


def run_task(args: argparse.Namespace) -> Path:
    labs_dir = Path(args.labs_dir).expanduser().resolve()
    task_dir = labs_dir / "tasks" / args.task
    task_json = task_dir / "task.json"
    if not task_json.is_file():
        sys.exit(f"error: {task_json} not found — check --labs-dir and --task")
    task_cfg = json.loads(task_json.read_text(encoding="utf-8"))

    deliverables = deliverable_names(task_cfg)
    workflow = pick_workflow(task_cfg, args.workflow, deliverables)
    multi = len(deliverables) > 1

    results_root = Path(args.results_dir).expanduser().resolve() if args.results_dir else labs_dir / "results"
    ts = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    run_id = f"{args.task}/{args.model_dir}/{ts}"
    run_dir = results_root / run_id
    output_dir = run_dir / "output"
    output_dir.mkdir(parents=True, exist_ok=True)
    transcript_path = run_dir / "transcript.jsonl"

    def log(kind: str, payload: dict) -> None:
        record = {"ts": datetime.now(timezone.utc).isoformat(), "event": kind, **payload}
        with transcript_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(record) + "\n")
        print(f"[{kind}] {payload}")

    client = BigLawClient(args.api)
    client.health()  # fail fast with a clear error if the backend is down

    started = time.monotonic()
    (run_dir / "config.json").write_text(json.dumps({
        "model": args.model_dir,
        "agent": "biglaw",
        "task": args.task,
        "workflow_type": workflow,
        "jurisdiction": args.jurisdiction,
        "split_mode": args.split_mode,
        "gate_policy": args.gate_policy,
        "api": args.api,
        "started_at": datetime.now(timezone.utc).isoformat(),
    }, indent=2), encoding="utf-8")

    # 1. Convert + ingest the task's documents (shared across submissions).
    converted, skipped = convert_documents(task_dir / "documents")
    for name in skipped:
        log("document_skipped", {"document": name})
    doc_ids, doc_names = [], []
    for rel, text in converted:
        doc_ids.append(client.ingest_text(rel, text))
        doc_names.append(rel)
    log("documents_ingested", {"count": len(doc_ids), "skipped": len(skipped)})

    # 2. Run BigLaw and map each deliverable to its content + optional table.
    gates: list[dict] = []
    finals: list[dict] = []
    outputs: dict[str, tuple[str, dict | None]] = {}

    if multi and args.split_mode == "per-task":
        for name in deliverables:
            description = build_description(task_cfg, doc_names, [name], use_markers=False)
            # An .xlsx deliverable still wants tabulate even if its siblings don't.
            wf = pick_workflow(task_cfg, args.workflow, [name])
            final = run_one_biglaw_task(client, description, wf, doc_ids, args, log, gates)
            finals.append(final)
            outputs[name] = (final["output"], final.get("table"))
    else:
        use_markers = multi and args.split_mode == "markers"
        description = build_description(task_cfg, doc_names, deliverables, use_markers)
        final = run_one_biglaw_task(client, description, workflow, doc_ids, args, log, gates)
        finals.append(final)
        synthesis, table = final["output"], final.get("table")
        sections = split_by_markers(synthesis, deliverables) if use_markers else {}
        if use_markers:
            log("synthesis_split", {
                "matched": sorted(sections),
                "fallback": sorted(set(deliverables) - set(sections)),
            })
        for name in deliverables:
            outputs[name] = (sections.get(name, strip_markers(synthesis)), table)

    # 3. Render deliverables where LAB's evaluation phase will look for them.
    for name, (text, table) in outputs.items():
        dest = output_dir / name
        render_deliverable(text, dest, biglaw_table=table)
        log("deliverable_written", {"file": str(dest.relative_to(run_dir))})

    # 4. metrics.json — key names follow the harness conventions best-effort;
    # eval folds these into scores.json metadata, it does not gate on them.
    in_tok = out_tok = 0
    cost_usd = 0.0
    for final in finals:
        try:
            cost = client.task_cost(final["id"]).get("summary", {})
        except BigLawError:
            cost = {}
        in_tok += (cost.get("totalInputTokens", 0) + cost.get("totalCacheReadTokens", 0)
                   + cost.get("totalCacheWriteTokens", 0))
        out_tok += cost.get("totalOutputTokens", 0)
        cost_usd += cost.get("totalUsd", 0)
    duration = time.monotonic() - started
    (run_dir / "metrics.json").write_text(json.dumps({
        "input_tokens": in_tok,
        "output_tokens": out_tok,
        "total_tokens": in_tok + out_tok,
        "turns": sum(len(f.get("rounds") or []) for f in finals),
        "duration_seconds": round(duration, 1),
        "num_documents": len(doc_ids),
        "documents_read": len(doc_ids),
        "completed": True,
        "ended_at": datetime.now(timezone.utc).isoformat(),
        "biglaw": {
            "task_ids": [f["id"] for f in finals],
            "workflow_type": workflow,
            "split_mode": args.split_mode if multi else "single",
            "cost_usd": round(cost_usd, 4),
            "gate_policy": args.gate_policy,
            "gates": gates,
            "gates_resolved": len(gates),
            "findings": sum(len(f.get("findings") or []) for f in finals),
            "documents_skipped": skipped,
        },
    }, indent=2), encoding="utf-8")

    print(f"\nrun complete in {duration:.0f}s — run dir: {run_dir}")
    print("score it from your harvey-labs checkout with:")
    print(f"  uv run python -m evaluation.run_eval --run-id {run_id} --task {args.task}")
    return run_dir


def list_tasks(labs_dir: Path) -> None:
    tasks_root = labs_dir / "tasks"
    if not tasks_root.is_dir():
        sys.exit(f"error: {tasks_root} not found — check --labs-dir")
    for tj in sorted(tasks_root.rglob("task.json")):
        rel = tj.parent.relative_to(tasks_root)
        try:
            cfg = json.loads(tj.read_text(encoding="utf-8"))
            print(f"{rel}  [{cfg.get('work_type', '?')}]  {cfg.get('title', '')}")
        except (json.JSONDecodeError, OSError):
            print(f"{rel}  [unreadable task.json]")


def main(argv: list[str] | None = None) -> None:
    p = argparse.ArgumentParser(description="Run Harvey LAB tasks through BigLaw.")
    p.add_argument("--labs-dir", required=True, help="path to a harvey-labs checkout")
    p.add_argument("--task", help="task path relative to tasks/, e.g. corporate-ma/review-data-room-red-flag-review")
    p.add_argument("--api", default="http://localhost:3101", help="BigLaw backend URL (Docker stack: http://localhost:3102)")
    p.add_argument("--workflow", choices=["roundtable", "review", "full_bench", "tabulate", "adversarial", "counsel"],
                   help="override the work_type→workflow mapping")
    p.add_argument("--jurisdiction", default="US", help="BigLaw jurisdiction tag (default US)")
    p.add_argument("--split-mode", choices=["markers", "per-task", "duplicate"], default="markers",
                   help="multi-deliverable handling: split one synthesis by markers (default), "
                        "one BigLaw task per deliverable, or duplicate the synthesis into each file")
    p.add_argument("--gate-policy", choices=["approve", "reject"], default="approve",
                   help="resolve human gates by approving flagged findings (default) or "
                        "rejecting them (benchmarks the verification gate as a filter)")
    p.add_argument("--model-dir", default="biglaw", help="model segment of the run-id (default biglaw)")
    p.add_argument("--results-dir", help="results root (default <labs-dir>/results, where evaluation.run_eval looks)")
    p.add_argument("--poll", type=float, default=10.0, help="poll interval seconds (default 10)")
    p.add_argument("--timeout", type=float, default=3600.0, help="per-BigLaw-task timeout seconds (default 3600)")
    p.add_argument("--list", action="store_true", help="list available tasks and exit")
    args = p.parse_args(argv)

    if args.list:
        list_tasks(Path(args.labs_dir).expanduser().resolve())
        return
    if not args.task:
        p.error("--task is required (or use --list)")

    try:
        run_task(args)
    except BigLawError as e:
        sys.exit(f"error: {e}")


if __name__ == "__main__":
    main()
