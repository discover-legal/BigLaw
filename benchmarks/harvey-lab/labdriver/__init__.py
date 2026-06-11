"""Driver that runs BigLaw against Harvey's Legal Agent Benchmark (LAB).

Replaces the LAB harness *run* phase: task documents are converted to text
and ingested into BigLaw, the task runs through the orchestrator (human
gates auto-approved), and the synthesis is rendered into the deliverable
files LAB's evaluation phase expects under results/<run-id>/output/.

Scoring stays Harvey's: `uv run python -m evaluation.run_eval` in the
harvey-labs checkout grades the deliverables unchanged.
"""
