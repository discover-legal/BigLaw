#!/usr/bin/env bash
# Big Michael — Bootstrap installer
#
# One-liner (fresh machine, no prerequisites needed):
#   curl -fsSL https://raw.githubusercontent.com/discover-legal/big-michael/main/setup.sh | bash
#
# Or if you already have the repo:
#   bash setup.sh
#
# Handles: Node.js install (via nvm), git clone, npm install, then
# launches the interactive setup wizard.

set -euo pipefail

REPO_URL="https://github.com/discover-legal/big-michael.git"
INSTALL_DIR="${BIG_MICHAEL_DIR:-$HOME/big-michael}"
MIN_NODE=18

# ── Colours ────────────────────────────────────────────────────────────────
R="\033[0m"; BOLD="\033[1m"; DIM="\033[2m"
RED="\033[31m"; GREEN="\033[32m"; YELLOW="\033[33m"; CYAN="\033[36m"; GRAY="\033[90m"

ok()   { echo -e "  ${GREEN}✓${R}  $*"; }
fail() { echo -e "  ${RED}✗${R}  $*"; }
warn() { echo -e "  ${YELLOW}⚡${R}  $*"; }
info() { echo -e "  ${CYAN}→${R}  $*"; }
note() { echo -e "     ${GRAY}$*${R}"; }
nl()   { echo; }

# ── Banner ────────────────────────────────────────────────────────────────
clear 2>/dev/null || true
echo -e "
${CYAN}  ┌──────────────────────────────────────────────────────────────┐${R}
${CYAN}  │${R}                                                              ${CYAN}│${R}
${CYAN}  │${R}    ${BOLD}⚖  Big Michael${R}                                              ${CYAN}│${R}
${CYAN}  │${R}    ${DIM}Multi-agent Legal AI  ·  Bootstrap Installer${R}               ${CYAN}│${R}
${CYAN}  │${R}                                                              ${CYAN}│${R}
${CYAN}  └──────────────────────────────────────────────────────────────┘${R}
"

# ── Detect OS ─────────────────────────────────────────────────────────────
OS="$(uname -s 2>/dev/null || echo Unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"
info "Platform: ${OS} / ${ARCH}"
nl

# ── Git ───────────────────────────────────────────────────────────────────
echo -e "  ${BOLD}${CYAN}▸${R} ${BOLD}Checking prerequisites${R}"
echo -e "  ${GRAY}────────────────────────────────────────────────────────────${R}"

if ! command -v git &>/dev/null; then
  fail "git not found."
  if [[ "$OS" == "Darwin" ]]; then
    note "Install Xcode Command Line Tools: xcode-select --install"
    note "Then re-run this script."
  elif command -v apt-get &>/dev/null; then
    info "Installing git via apt..."
    sudo apt-get update -qq && sudo apt-get install -y -qq git
    ok "git installed."
  elif command -v yum &>/dev/null; then
    info "Installing git via yum..."
    sudo yum install -y -q git
    ok "git installed."
  else
    echo "  Please install git and re-run this script."
    exit 1
  fi
else
  GIT_VER="$(git --version | grep -oE '[0-9]+\.[0-9]+' | head -1)"
  ok "git ${GIT_VER}"
fi

# ── Node.js ───────────────────────────────────────────────────────────────
needs_node=false
if command -v node &>/dev/null; then
  NODE_VER="$(node --version | sed 's/v//')"
  NODE_MAJOR="$(echo "$NODE_VER" | cut -d. -f1)"
  if (( NODE_MAJOR >= MIN_NODE )); then
    ok "Node.js ${NODE_VER}"
  else
    warn "Node.js ${NODE_VER} found — need ${MIN_NODE}+. Will upgrade via nvm."
    needs_node=true
  fi
else
  warn "Node.js not found. Will install via nvm."
  needs_node=true
fi

if [[ "$needs_node" == "true" ]]; then
  NVM_DIR="${NVM_DIR:-$HOME/.nvm}"

  if [[ ! -s "$NVM_DIR/nvm.sh" ]]; then
    info "Installing nvm (Node Version Manager)..."
    curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash
    ok "nvm installed."
  fi

  # shellcheck source=/dev/null
  export NVM_DIR
  source "$NVM_DIR/nvm.sh"

  info "Installing Node.js ${MIN_NODE} LTS..."
  nvm install --lts
  nvm use --lts
  NODE_VER="$(node --version | sed 's/v//')"
  ok "Node.js ${NODE_VER} ready."

  # Persist nvm in current shell for the rest of the script
  if [[ -f "$HOME/.bashrc" ]] && ! grep -q 'NVM_DIR' "$HOME/.bashrc" 2>/dev/null; then
    {
      echo ''
      echo '# nvm — added by Big Michael setup'
      echo "export NVM_DIR=\"$NVM_DIR\""
      # shellcheck disable=SC2016
      echo '[ -s "$NVM_DIR/nvm.sh" ] && \. "$NVM_DIR/nvm.sh"'
    } >> "$HOME/.bashrc"
  fi
fi

# ── Python (optional) ─────────────────────────────────────────────────────
if command -v python3 &>/dev/null; then
  PY_VER="$(python3 --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')"
  PY_MINOR="$(echo "$PY_VER" | cut -d. -f2)"
  if (( PY_MINOR >= 11 )); then
    ok "Python ${PY_VER}  ${GRAY}— PDF parsing enabled${R}"
  else
    warn "Python ${PY_VER} found — 3.11+ recommended for PDF tools"
    note "macOS: brew install python  ·  Linux: apt install python3.11"
  fi
else
  warn "Python not found — PDF parsing disabled"
  note "macOS: brew install python  ·  Linux: apt install python3.11"
fi

# ── Tesseract (optional) ──────────────────────────────────────────────────
if command -v tesseract &>/dev/null; then
  TESS_VER="$(tesseract --version 2>&1 | grep -oE '[0-9]+\.[0-9]+' | head -1)"
  ok "Tesseract ${TESS_VER}  ${GRAY}— OCR enabled${R}"
else
  warn "Tesseract not found — OCR disabled"
  note "macOS: brew install tesseract  ·  Linux: apt install tesseract-ocr"
fi

nl

# ── Clone or enter repo ───────────────────────────────────────────────────
echo -e "  ${BOLD}${CYAN}▸${R} ${BOLD}Repository${R}"
echo -e "  ${GRAY}────────────────────────────────────────────────────────────${R}"

# Detect if we're already inside the repo (script run from within clone)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || pwd)"
if [[ -f "$SCRIPT_DIR/package.json" ]] && grep -q '"name": "big-michael"' "$SCRIPT_DIR/package.json" 2>/dev/null; then
  REPO_DIR="$SCRIPT_DIR"
  ok "Already in repo at ${REPO_DIR}"
elif [[ -d "$INSTALL_DIR/.git" ]]; then
  REPO_DIR="$INSTALL_DIR"
  ok "Found existing clone at ${REPO_DIR}"
  info "Pulling latest changes..."
  git -C "$REPO_DIR" pull --ff-only --quiet 2>/dev/null || warn "Could not pull — continuing with existing version."
else
  info "Cloning into ${INSTALL_DIR}..."
  git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
  REPO_DIR="$INSTALL_DIR"
  ok "Cloned."
fi

nl

# ── Install deps ──────────────────────────────────────────────────────────
echo -e "  ${BOLD}${CYAN}▸${R} ${BOLD}Installing dependencies${R}"
echo -e "  ${GRAY}────────────────────────────────────────────────────────────${R}"

cd "$REPO_DIR"

info "npm install (this takes ~30 seconds on first run)..."
npm install --prefer-offline --no-audit --no-fund --loglevel=error 2>&1 \
  | grep -v "^npm warn" || true
ok "Dependencies ready."

# ── Python deps (optional) ────────────────────────────────────────────────
if command -v pip3 &>/dev/null && [[ -f requirements.txt ]]; then
  INSTALL_PY=false
  if command -v python3 &>/dev/null; then
    PY_MINOR="$(python3 --version 2>&1 | grep -oE '3\.([0-9]+)' | cut -d. -f2)"
    (( PY_MINOR >= 11 )) && INSTALL_PY=true
  fi
  if [[ "$INSTALL_PY" == "true" ]]; then
    info "Installing Python PDF dependencies..."
    pip3 install -q -r requirements.txt 2>/dev/null && ok "Python deps installed." \
      || warn "Python dep install failed — PDF tools may not work."
  fi
fi

nl

# ── Hand off to interactive wizard ───────────────────────────────────────
echo -e "  ${BOLD}${CYAN}▸${R} ${BOLD}Launching Setup Wizard${R}"
echo -e "  ${GRAY}────────────────────────────────────────────────────────────${R}"
nl

# Use npx so tsx works even if the local install failed
exec npm run setup
