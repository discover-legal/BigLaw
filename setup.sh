#!/usr/bin/env bash
# BigLaw — bootstrap installer (Go platform)
#
# One-liner (fresh machine, needs git + Docker):
#   curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
#
# Or if you already have the repo:
#   bash setup.sh
#
# Brings up the three-container Go stack (TypeDB → conflict-graph sidecar →
# BigLaw core) with Docker Compose. API keys are read from .env at the repo
# root; copy .env.example and fill in what you have — unconfigured connectors
# degrade gracefully.
#
# The TypeScript implementation this replaced is preserved at the git tag
# `typescript-final`.

set -euo pipefail

REPO_URL="https://github.com/discover-legal/BigLaw.git"
REPO_DIR="BigLaw"

say()  { printf '\033[1;33m[BigLaw]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[BigLaw]\033[0m %s\n' "$*" >&2; exit 1; }

command -v git    >/dev/null 2>&1 || fail "git is required — install it first."
command -v docker >/dev/null 2>&1 || fail "Docker is required — https://docs.docker.com/get-docker/"
docker info >/dev/null 2>&1       || fail "Docker daemon is not running — start Docker and re-run."

# Clone if we're not already inside the repo.
if [ ! -f "biglaw-go/docker-compose.yml" ]; then
  if [ -d "$REPO_DIR" ]; then
    say "Repo directory exists — using it."
  else
    say "Cloning $REPO_URL…"
    git clone --depth 1 "$REPO_URL" "$REPO_DIR"
  fi
  cd "$REPO_DIR"
fi

# Seed .env from the example if absent.
if [ ! -f .env ]; then
  if [ -f .env.example ]; then
    cp .env.example .env
    say "Created .env from .env.example — add your ANTHROPIC_API_KEY (or local-inference settings) before real use."
  else
    touch .env
    say "Created empty .env — add ANTHROPIC_API_KEY or LOCAL_INFERENCE_* settings."
  fi
fi

say "Building and starting the stack (TypeDB → sidecar → BigLaw core)…"
docker compose -f biglaw-go/docker-compose.yml up -d --build

say "Waiting for the API…"
for _ in $(seq 1 30); do
  if curl -fsS http://localhost:3102/health >/dev/null 2>&1; then
    say "BigLaw is up → REST API at http://localhost:3102"
    say "Web UI:  cd ui && npm install && npm run dev   (http://localhost:5173)"
    exit 0
  fi
  sleep 2
done

fail "API did not come up — check: docker compose -f biglaw-go/docker-compose.yml logs biglaw"
