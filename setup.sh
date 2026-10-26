#!/usr/bin/env bash
# BigLaw 2 installer. Needs Docker Desktop (or Docker Engine with Compose v2); run it in a terminal:
#   curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
# It installs into ./biglaw and asks one question. Rerun it to update: your biglaw/.env is kept.
# SETUP_REF picks the branch or tag the compose file and .env.example come from (default main).
# Everything runs from main, called on the last line: a download cut short runs nothing.
set -euo pipefail

say() { printf '[BigLaw] %s\n' "$*"; }
fail() { printf '[BigLaw] %s\n' "$*" >&2; exit 1; }

main() {
  local src="https://raw.githubusercontent.com/discover-legal/BigLaw/${SETUP_REF:-main}"
  local url="http://127.0.0.1:3101" # docker-compose.yml publishes biglaw on this computer's loopback only
  local f key murl models
  command -v curl >/dev/null 2>&1 || fail "curl is required."
  command -v docker >/dev/null 2>&1 || fail "Docker is required: install Docker Desktop (https://docs.docker.com/get-docker/), then rerun."
  docker info >/dev/null 2>&1 || fail "Docker is not running: start Docker Desktop, then rerun."
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required (Docker Desktop includes it)."
  [ ! -e biglaw/.git ] || fail "./biglaw is a git checkout (BigLaw's source?): run this from another folder."

  mkdir -p biglaw
  cd biglaw
  for f in docker-compose.yml .env.example; do
    curl -fsSL "$src/$f" -o "$f.tmp" || fail "could not download $f from $src"
    mv "$f.tmp" "$f"
  done

  if [ -f .env ]; then
    say "Keeping your settings in biglaw/.env (delete that file to answer the setup question again)."
  else
    printf '[BigLaw] Paste your OpenRouter API key (https://openrouter.ai/keys),\n'
    printf '[BigLaw] or just press Enter to use Ollama on this computer: '
    IFS= read -rs key </dev/tty || fail "no terminal to ask on: download setup.sh and run 'bash setup.sh'."
    echo
    key="${key#"${key%%[![:space:]]*}"}" # trim white space a paste may carry
    key="${key%"${key##*[![:space:]]}"}"
    if [ -z "$key" ]; then
      murl=http://host.docker.internal:11434/v1
      models=lead=qwen2.5:14b,embed=nomic-embed-text
      say "Using Ollama. If you have not yet: ollama pull qwen2.5:14b && ollama pull nomic-embed-text"
      say "(On Linux, Ollama must listen beyond 127.0.0.1 for Docker to reach it: README § Install.)"
    else
      case "$key" in *[!A-Za-z0-9_-]*) fail "that does not look like an API key; nothing was saved." ;; esac
      murl=https://openrouter.ai/api/v1
      models=lead=anthropic/claude-haiku-4.5,vision=anthropic/claude-haiku-4.5 # no embed role: lexical search
    fi
    umask 077 # .env holds the key and the database password: readable by you alone
    rm -f .env.tmp
    trap 'rm -f .env.tmp' EXIT
    PW="$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')" KEY="$key" MURL="$murl" MODELS="$models" awk '
      /^POSTGRES_PASSWORD=$/ { $0 = $0 ENVIRON["PW"] }
      /^BIGLAW_MODEL_URL=$/ { $0 = $0 ENVIRON["MURL"] }
      /^BIGLAW_MODEL_KEY=$/ { $0 = $0 ENVIRON["KEY"] }
      /^BIGLAW_MODELS=$/ { $0 = $0 ENVIRON["MODELS"] }
      { print }' .env.example >.env.tmp
    grep -Eq '^POSTGRES_PASSWORD=[0-9a-f]{48}$' .env.tmp || fail "could not write a database password into .env"
    mv .env.tmp .env
  fi
  chmod 600 .env

  say "Downloading and starting BigLaw (the first time takes a few minutes)..."
  docker compose pull
  docker compose up -d --no-build
  for _ in $(seq 1 90); do
    if curl -fsS "$url/health" >/dev/null 2>&1; then
      say "BigLaw is running. Open $url in your browser."
      return 0
    fi
    sleep 2
  done
  fail "BigLaw did not answer at $url/health; see: cd biglaw && docker compose logs biglaw"
}

main "$@"
