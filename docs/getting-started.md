[Docs](index.md) › Get started › **Getting started**

# Getting started

## The easy way — one command

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

Needs git + Docker. Handles everything: clones the repo if needed, seeds `.env` from
`.env.example`, builds and starts the three-container stack (TypeDB → conflict-graph sidecar →
BigLaw core), and waits for the REST API at **http://localhost:3102**. Add your
`QWEN_API_KEY` (or local-inference settings) to `.env` — unconfigured connectors degrade
gracefully. Re-run any time.

## Already have the repo cloned?

```bash
bash setup.sh       # needs Docker running
```

## Manual setup (Go platform)

The platform is a single Go binary plus a TypeDB conflict-graph sidecar, packaged as a
three-container Docker stack. The retired TypeScript implementation is preserved at the
git tag **`typescript-final`**.

```bash
# Secrets — by default the model stack is Qwen, so set QWEN_API_KEY (DashScope).
# Or LOCAL_INFERENCE_* for Ollama/LM Studio, or another MODEL_STACK (glm/kimi/custom).
cp .env.example .env

# The whole stack: TypeDB → conflict-graph sidecar → BigLaw core
docker compose -f biglaw-go/docker-compose.yml up -d --build
# REST API → http://localhost:3102

# Or run the core natively (Go 1.25+, from the repo root so templates/ and
# deadlines/rules/ resolve):
go run ./biglaw-go/cmd/biglaw           # REST API on :3101
go run ./biglaw-go/cmd/biglaw demo      # one-command end-to-end tour: seed → tabular review →
                                        # CP checklist → counter-redline (~$0.03 in live model calls)

# Tests
cd biglaw-go && go test ./...
```

Model stack selection, persistence backends, and document ingestion options are covered in
[Models, persistence & documents](deployment/models-and-persistence.md).

## Web workbench (Vite + React)

```bash
cd ui
npm install
BIG_MICHAEL_API=http://localhost:3102 npm run dev   # workbench on :5173
```

Open **http://localhost:5173** — convene a matter, watch rounds stream live, approve gates,
review contracts against your playbook, walk due-diligence grids with verification-state
citation pills (click through to the highlighted source quote), follow Redtime negotiation
timelines, and pull cited findings and tabular-review CSVs.

To run the workbench and the Claude Code MCP at the same time, see
[Run modes](deployment/run-modes.md).

## Your first task

From the workbench: convene a matter and describe the work. From Claude Code (with the
repo open, so `.mcp.json` registers the MCP server):

```
Use BigLaw to review this SaaS master services agreement under New York law —
flag the uncapped indemnity and unlimited-liability exposure, and recommend fallback
positions for the customer. Run a roundtable workflow.
```

Claude Code submits the task, polls progress, approves any human gates, and surfaces the
final synthesis. Full details: [MCP / Claude Code](integration/mcp.md).

## Where next

- [Why BigLaw](why-biglaw.md) — what the incumbent stack costs, and the comparison table
- [Security](security.md) and [Legal notices](legal-notices.md) — before anything touches real client data
- [Architecture overview](architecture/overview.md) — how the bench actually works
