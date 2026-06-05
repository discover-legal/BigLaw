#!/usr/bin/env tsx
/**
 * Big Michael — Interactive Setup Wizard
 * Usage: npm run setup
 */

import * as readline from "readline";
import { execSync } from "child_process";
import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import * as crypto from "crypto";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// ── Colours ────────────────────────────────────────────────────────────────
const fg = {
  red:    (s: string) => `\x1b[31m${s}\x1b[0m`,
  green:  (s: string) => `\x1b[32m${s}\x1b[0m`,
  yellow: (s: string) => `\x1b[33m${s}\x1b[0m`,
  cyan:   (s: string) => `\x1b[36m${s}\x1b[0m`,
  gray:   (s: string) => `\x1b[90m${s}\x1b[0m`,
  bold:   (s: string) => `\x1b[1m${s}\x1b[0m`,
  dim:    (s: string) => `\x1b[2m${s}\x1b[0m`,
};

const IC = {
  ok:   fg.green("✓"),
  fail: fg.red("✗"),
  warn: fg.yellow("⚡"),
  skip: fg.gray("○"),
  ask:  fg.cyan("?"),
};

const printOk   = (s: string) => console.log(`  ${IC.ok}  ${s}`);
const printFail = (s: string) => console.log(`  ${IC.fail}  ${s}`);
const printWarn = (s: string) => console.log(`  ${IC.warn}  ${s}`);
const printSkip = (s: string) => console.log(`  ${IC.skip}  ${fg.dim(s)}`);
const printNote = (s: string) => console.log(`     ${fg.gray(s)}`);

function printSection(title: string) {
  console.log(`\n  ${fg.bold(fg.cyan("▸"))} ${fg.bold(title)}`);
  console.log(`  ${fg.gray("─".repeat(60))}`);
}

function visLen(s: string) { return s.replace(/\x1b\[[0-9;]*m/g, "").length; }
function padEnd(s: string, n: number) { return s + " ".repeat(Math.max(0, n - visLen(s))); }

function printBox(lines: string[]) {
  const W = 62;
  const top = `  ┌${"─".repeat(W)}┐`;
  const bot = `  └${"─".repeat(W)}┘`;
  const row = (s: string) => {
    const inner = `  ${s}`;
    return `  │${inner}${" ".repeat(Math.max(0, W - visLen(inner)))} │`;
  };
  console.log([top, ...lines.map(row), bot].join("\n"));
}

// ── Spinner ────────────────────────────────────────────────────────────────
class Spinner {
  private frames = ["⠋","⠙","⠹","⠸","⠼","⠴","⠦","⠧","⠇","⠏"];
  private i = 0;
  private t?: ReturnType<typeof setInterval>;
  private label = "";

  start(msg: string) {
    this.label = msg;
    process.stdout.write("\n");
    this.t = setInterval(() => {
      process.stdout.write(`\r  ${fg.cyan(this.frames[this.i++ % this.frames.length])}  ${this.label}   `);
    }, 80);
  }

  done(msg?: string) { this.stop(msg ?? this.label, true); }
  fail(msg?: string) { this.stop(msg ?? this.label, false); }

  private stop(msg: string, success: boolean) {
    clearInterval(this.t);
    process.stdout.write(`\r  ${success ? IC.ok : IC.fail}  ${msg}   \n`);
  }
}

// ── Readline helpers ───────────────────────────────────────────────────────
let _rl: readline.Interface;

function initReadline() {
  _rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  _rl.on("SIGINT", () => { console.log("\n\n  Cancelled."); process.exit(0); });
}

function ask(prompt: string, def?: string): Promise<string> {
  const hint = def ? ` ${fg.gray(`[${def}]`)}` : "";
  return new Promise(res => {
    _rl.question(`  ${IC.ask} ${prompt}${hint}: `, ans => res(ans.trim() || def || ""));
  });
}

function askSecret(prompt: string, hasCur = false): Promise<string> {
  const hint = hasCur ? ` ${fg.gray("[keep — press Enter]")}` : "";
  if (!process.stdin.isTTY) return ask(`${prompt}${hint}`);

  return new Promise(res => {
    _rl.pause();
    process.stdout.write(`  ${IC.ask} ${prompt}${hint}: `);
    let val = "";
    process.stdin.setRawMode(true);
    process.stdin.resume();

    const onData = (buf: Buffer) => {
      const ch = buf.toString("utf8");
      if (ch === "") { process.stdout.write("\n"); process.exit(0); }
      if (ch === "\r" || ch === "\n") {
        process.stdin.setRawMode(false);
        process.stdin.removeListener("data", onData);
        process.stdout.write("\n");
        _rl.resume();
        res(val);
        return;
      }
      if (ch === "") { if (val.length) { val = val.slice(0, -1); process.stdout.write("\b \b"); } }
      else if (ch >= " ")  { val += ch; process.stdout.write("*"); }
    };

    process.stdin.on("data", onData);
  });
}

async function confirm(prompt: string, def = false): Promise<boolean> {
  const ans = await ask(`${prompt} ${fg.gray(def ? "(Y/n)" : "(y/N)")}`, def ? "y" : "n");
  return /^y/i.test(ans);
}

// ── Multi-select checkbox picker ───────────────────────────────────────────
interface SelectRow {
  id: string;
  label: string;
  hint: string;
  header?: true;   // non-navigable section label
}

async function multiSelect(title: string, rows: SelectRow[], preSelected: string[] = []): Promise<string[]> {
  const selected = new Set(preSelected);
  const navigable = rows.filter(r => !r.header);
  let cursor = 0;
  let rendered = 0;

  if (!process.stdin.isTTY) {
    // Non-interactive: return preselected unchanged
    return preSelected;
  }

  const buildLines = (): string[] => {
    const out: string[] = [];
    out.push(`  ${fg.bold(title)}`);
    out.push(`  ${fg.gray("↑↓ move   space select   a all   enter confirm")}`);
    out.push("");

    let navIdx = 0;
    for (const row of rows) {
      if (row.header) {
        out.push(`  ${fg.bold(fg.gray(row.label))}`);
        continue;
      }
      const active  = navIdx === cursor;
      const checked = selected.has(row.id);
      const box  = checked ? fg.green("[✓]") : fg.gray("[ ]");
      const name = active
        ? fg.bold(fg.cyan(padEnd(row.label, 30)))
        : padEnd(row.label, 30);
      const hint = fg.gray(row.hint.length > 32 ? row.hint.slice(0, 31) + "…" : row.hint);
      const pre  = active ? fg.cyan("  ▸ ") : "    ";
      out.push(`${pre}${box} ${name} ${hint}`);
      navIdx++;
    }

    out.push("");
    const n = selected.size;
    out.push(`  ${fg.gray(`${n} connector${n !== 1 ? "s" : ""} selected`)}`);
    return out;
  };

  const render = () => {
    const lines = buildLines();
    if (rendered > 0) process.stdout.write(`\x1b[${rendered}A`);
    for (const line of lines) process.stdout.write(`\x1b[2K${line}\n`);
    rendered = lines.length;
  };

  render();

  return new Promise(res => {
    _rl.pause();
    process.stdin.setRawMode(true);
    process.stdin.resume();

    const onData = (buf: Buffer) => {
      const ch = buf.toString("utf8");

      if (ch === "") { process.stdout.write("\n"); process.exit(0); }

      if (ch === "\r" || ch === "\n") {
        process.stdin.setRawMode(false);
        process.stdin.removeListener("data", onData);
        _rl.resume();
        console.log(); // blank line after picker
        res(Array.from(selected));
        return;
      }

      if (ch === "\x1b[A") { cursor = Math.max(0, cursor - 1); }
      else if (ch === "\x1b[B") { cursor = Math.min(navigable.length - 1, cursor + 1); }
      else if (ch === " ") {
        const id = navigable[cursor].id;
        if (selected.has(id)) selected.delete(id); else selected.add(id);
      }
      else if (ch === "a" || ch === "A") {
        if (selected.size === navigable.length) selected.clear();
        else for (const r of navigable) selected.add(r.id);
      }

      render();
    };

    process.stdin.on("data", onData);
  });
}

// ── System checks ──────────────────────────────────────────────────────────
function checkNode() {
  const v = process.version.slice(1);
  return { ok: parseInt(v) >= 18, version: v };
}

function checkPython(): { ok: boolean; version: string; bin: string } {
  for (const bin of ["python3", "python"]) {
    try {
      const v = execSync(`${bin} --version 2>&1`, { timeout: 5_000 }).toString().match(/(\d+\.\d+\.\d+)/)?.[1] ?? "";
      const [maj, min] = v.split(".").map(Number);
      if (maj === 3 && min >= 11) return { ok: true, version: v, bin };
    } catch {}
  }
  return { ok: false, version: "", bin: "python3" };
}

function checkDocker(): { ok: boolean; version: string } {
  try {
    const v = execSync("docker --version 2>&1", { timeout: 5_000 }).toString().match(/(\d+\.\d+)/)?.[1] ?? "";
    return { ok: true, version: v };
  } catch { return { ok: false, version: "" }; }
}

function checkTesseract(): { ok: boolean; version: string } {
  try {
    const v = execSync("tesseract --version 2>&1", { timeout: 5_000 }).toString().match(/(\d+\.\d+)/)?.[1] ?? "";
    return { ok: true, version: v };
  } catch { return { ok: false, version: "" }; }
}

// ── Connector definitions ──────────────────────────────────────────────────
interface KeyDef {
  key: string;
  label: string;
  secret?: boolean;
  default?: string;
  autoGen?: boolean;
}

interface ConnDef {
  id: string;
  name: string;
  blurb: string;
  keys: KeyDef[];
  howTo: string[];
}

// Rows for the checkbox picker
const PICKER_ROWS: SelectRow[] = [
  { id: "h-research",   label: "Legal Research",                 hint: "", header: true },
  { id: "tavily",       label: "Tavily Web Search",              hint: "real-time search for research agents" },
  { id: "courtlistener",label: "CourtListener  (free)",          hint: "US court opinions, dockets, filings" },
  { id: "westlaw",      label: "Westlaw / CoCounsel",            hint: "full Westlaw research — enterprise" },
  { id: "trellis",      label: "Trellis",                        hint: "state courts + judge analytics" },
  { id: "everlaw",      label: "Everlaw",                        hint: "eDiscovery review sets" },
  { id: "descrybe",     label: "Descrybe",                       hint: "UK · AU · CA case law" },
  { id: "solve-intel",  label: "Solve Intelligence",             hint: "patent search & claims drafting" },

  { id: "h-contract",   label: "Contract & Document Management", hint: "", header: true },
  { id: "ironclad",     label: "Ironclad",                       hint: "contract workflow" },
  { id: "docusign-clm", label: "DocuSign CLM",                   hint: "contract lifecycle management" },
  { id: "imanage",      label: "iManage",                        hint: "DMS search & retrieval" },
  { id: "definely",     label: "Definely",                       hint: "contract structure & definition AI" },
  { id: "lawve",        label: "Lawve AI",                       hint: "automated contract review" },

  { id: "h-productivity",label: "Productivity & Files",          hint: "", header: true },
  { id: "gdrive",       label: "Google Drive",                   hint: "search & retrieve documents" },
  { id: "box",          label: "Box",                            hint: "document vault" },
  { id: "slack",        label: "Slack",                          hint: "messages & notifications" },
  { id: "topcounsel",   label: "TopCounsel",                     hint: "outside counsel routing" },

  { id: "h-practice",   label: "Practice Management & E-Sig",   hint: "", header: true },
  { id: "clio",         label: "Clio",                           hint: "import matters, sync time entries" },
  { id: "docuseal",     label: "DocuSeal",                       hint: "self-hosted e-signature (Docker)" },

  { id: "h-advanced",   label: "Advanced",                       hint: "", header: true },
  { id: "auth",         label: "Multi-user Auth  (OAuth)",       hint: "Google / Microsoft login for lawyers" },
  { id: "typedb",       label: "TypeDB Conflict Graph",          hint: "n-ary conflict-of-interest detection" },
  { id: "ollama",       label: "Ollama  (local AI)",             hint: "route T3 agents to a local model" },
  { id: "infisical",    label: "Infisical",                      hint: "team secrets manager" },
];

const CONNECTORS: Record<string, ConnDef> = {
  tavily: {
    id: "tavily", name: "Tavily Web Search",
    blurb: "Real-time web search for research agents.",
    keys: [{ key: "TAVILY_API_KEY", label: "API key", secret: true }],
    howTo: ["app.tavily.com  →  sign up  →  copy API key", "Free tier: 1,000 searches/month"],
  },
  courtlistener: {
    id: "courtlistener", name: "CourtListener",
    blurb: "US court opinions, dockets, and filings. Free public API — key only needed for higher rate limits.",
    keys: [{ key: "COURT_LISTENER_API_KEY", label: "API key (optional — skip for free limits)", secret: true }],
    howTo: ["courtlistener.com  →  Settings  →  API Token"],
  },
  westlaw: {
    id: "westlaw", name: "Westlaw / CoCounsel",
    blurb: "Full Westlaw legal research and CoCounsel AI. Enterprise subscription required.",
    keys: [{ key: "WESTLAW_API_KEY", label: "API key", secret: true }],
    howTo: ["Contact your Westlaw account rep", "legal.thomsonreuters.com"],
  },
  trellis: {
    id: "trellis", name: "Trellis",
    blurb: "State court cases across all 50 US states plus judge analytics.",
    keys: [{ key: "TRELLIS_API_KEY", label: "API key", secret: true }],
    howTo: ["trellis.law  →  contact sales for API access"],
  },
  everlaw: {
    id: "everlaw", name: "Everlaw",
    blurb: "Search and review documents from Everlaw review sets.",
    keys: [{ key: "EVERLAW_API_KEY", label: "API key", secret: true }],
    howTo: ["everlaw.com  →  Settings  →  Integrations  →  API"],
  },
  descrybe: {
    id: "descrybe", name: "Descrybe",
    blurb: "Legal research for UK, Australian, and Canadian case law.",
    keys: [{ key: "DESCRYBE_API_KEY", label: "API key", secret: true }],
    howTo: ["descrybe.ai  →  account dashboard  →  API access"],
  },
  "solve-intel": {
    id: "solve-intel", name: "Solve Intelligence",
    blurb: "Patent prior-art search and AI-assisted claims drafting.",
    keys: [{ key: "SOLVE_INTELLIGENCE_API_KEY", label: "API key", secret: true }],
    howTo: ["solveintelligence.com  →  account settings  →  API keys"],
  },
  ironclad: {
    id: "ironclad", name: "Ironclad",
    blurb: "Search and retrieve contracts from your Ironclad repository.",
    keys: [{ key: "IRONCLAD_API_KEY", label: "API key", secret: true }],
    howTo: ["ironcladapp.com  →  Settings  →  API & Integrations"],
  },
  "docusign-clm": {
    id: "docusign-clm", name: "DocuSign CLM",
    blurb: "Search contracts and check envelope status in DocuSign.",
    keys: [{ key: "DOCUSIGN_API_KEY", label: "Integration key", secret: true }],
    howTo: ["developers.docusign.com  →  Create an Integration Key"],
  },
  imanage: {
    id: "imanage", name: "iManage",
    blurb: "Search and retrieve documents from iManage Work.",
    keys: [{ key: "IMANAGE_API_KEY", label: "API key", secret: true }],
    howTo: ["imanage.com  →  Developer portal  →  API credentials"],
  },
  definely: {
    id: "definely", name: "Definely",
    blurb: "AI-powered contract structure analysis and definition resolver.",
    keys: [{ key: "DEFINELY_API_KEY", label: "API key", secret: true }],
    howTo: ["definely.com  →  account settings  →  API"],
  },
  lawve: {
    id: "lawve", name: "Lawve AI",
    blurb: "Automated contract review and clause search.",
    keys: [{ key: "LAWVE_API_KEY", label: "API key", secret: true }],
    howTo: ["lawve.ai  →  account dashboard  →  API key"],
  },
  gdrive: {
    id: "gdrive", name: "Google Drive",
    blurb: "Search and retrieve documents from Google Drive.",
    keys: [{ key: "GOOGLE_DRIVE_API_KEY", label: "API key", secret: true }],
    howTo: ["console.cloud.google.com  →  Enable Drive API  →  Credentials  →  Create API key"],
  },
  box: {
    id: "box", name: "Box",
    blurb: "Search and retrieve documents from Box.",
    keys: [{ key: "BOX_API_KEY", label: "API key", secret: true }],
    howTo: ["developer.box.com  →  My Apps  →  Create  →  Server Authentication"],
  },
  slack: {
    id: "slack", name: "Slack",
    blurb: "Search Slack messages and send notifications from drafting agents.",
    keys: [{ key: "SLACK_API_KEY", label: "Bot token (xoxb-...)", secret: true }],
    howTo: ["api.slack.com  →  Create App  →  OAuth & Permissions", "Bot Scopes: channels:history, chat:write, search:read"],
  },
  topcounsel: {
    id: "topcounsel", name: "TopCounsel",
    blurb: "Route matters to your outside counsel panel and receive fee quotes.",
    keys: [{ key: "TOPCOUNSEL_API_KEY", label: "API key", secret: true }],
    howTo: ["topcounsel.com  →  Settings  →  API Access"],
  },
  clio: {
    id: "clio", name: "Clio",
    blurb: "Import matters and documents from Clio Manage; sync time entries back.",
    keys: [
      { key: "CLIO_CLIENT_ID",     label: "OAuth Client ID" },
      { key: "CLIO_CLIENT_SECRET", label: "OAuth Client Secret", secret: true },
      { key: "CLIO_REDIRECT_URI",  label: "Redirect URI", default: "http://localhost:3101/auth/clio/callback" },
      { key: "CLIO_REGION",        label: "Region  (us / eu / ca / au)", default: "us" },
    ],
    howTo: [
      "1. app.clio.com  →  Settings  →  Developer Applications  →  Add",
      "2. Redirect URI: http://localhost:3101/auth/clio/callback",
      "3. Copy Client ID and Client Secret",
    ],
  },
  docuseal: {
    id: "docuseal", name: "DocuSeal",
    blurb: "Self-hosted e-signature. Docker Compose starts it automatically — enter the key after first-run setup.",
    keys: [{ key: "DOCUSEAL_API_KEY", label: "API key (get it after Docker launch)", secret: true }],
    howTo: ["After setup: http://localhost:3000  →  complete first-run  →  Settings  →  API  →  copy key"],
  },
  auth: {
    id: "auth", name: "Multi-user Auth",
    blurb: "OAuth login for multiple lawyers. Disabled = single local-partner mode.",
    keys: [
      { key: "AUTH_ENABLED",           label: "Enable auth", default: "true" },
      { key: "SESSION_SECRET",          label: "Session secret (auto-generated)", secret: true, autoGen: true },
      { key: "GOOGLE_CLIENT_ID",        label: "Google Client ID     (optional, press Enter to skip)" },
      { key: "GOOGLE_CLIENT_SECRET",    label: "Google Client Secret  (optional)", secret: true },
      { key: "MICROSOFT_CLIENT_ID",     label: "Microsoft Client ID   (optional, press Enter to skip)" },
      { key: "MICROSOFT_CLIENT_SECRET", label: "Microsoft Client Secret (optional)", secret: true },
    ],
    howTo: [
      "Google:    console.cloud.google.com  →  Credentials  →  OAuth 2.0 Client IDs",
      "  Redirect: http://localhost:3101/auth/google/callback",
      "Microsoft: portal.azure.com  →  Azure AD  →  App registrations",
      "  Redirect: http://localhost:3101/auth/microsoft/callback",
    ],
  },
  typedb: {
    id: "typedb", name: "TypeDB Conflict Graph",
    blurb: "Polymorphic n-ary conflict-of-interest detection. Start with: docker compose --profile graph up -d",
    keys: [
      { key: "TYPEDB_URL",      label: "TypeDB host:port", default: "localhost:1729" },
      { key: "TYPEDB_DATABASE", label: "Database name",     default: "big-michael" },
    ],
    howTo: ["docker compose --profile graph up -d  — TypeDB starts on localhost:1729"],
  },
  ollama: {
    id: "ollama", name: "Ollama",
    blurb: "Route T3 tool agents to a local model — cuts cloud API costs on high-volume tasks.",
    keys: [
      { key: "OLLAMA_ENABLED",  label: "Enable Ollama for T3 agents", default: "true" },
      { key: "OLLAMA_MODEL",    label: "Model name",                   default: "llama3.2" },
      { key: "OLLAMA_BASE_URL", label: "Ollama base URL",              default: "http://localhost:11434" },
    ],
    howTo: ["ollama.com/download  →  install  →  ollama pull llama3.2"],
  },
  infisical: {
    id: "infisical", name: "Infisical",
    blurb: "Store all API keys in Infisical instead of .env — ideal for teams and CI/CD.",
    keys: [
      { key: "INFISICAL_CLIENT_ID",     label: "Machine Identity Client ID" },
      { key: "INFISICAL_CLIENT_SECRET", label: "Client Secret", secret: true },
      { key: "INFISICAL_PROJECT_ID",    label: "Project ID" },
    ],
    howTo: ["app.infisical.com  →  Project  →  Settings  →  Machine Identities  →  Create"],
  },
};

// ── .env load / write ──────────────────────────────────────────────────────
function loadEnv(): Record<string, string> {
  const p = path.join(ROOT, ".env");
  if (!fs.existsSync(p)) return {};
  const env: Record<string, string> = {};
  for (const line of fs.readFileSync(p, "utf8").split("\n")) {
    const m = line.match(/^([A-Z_0-9]+)=(.*)/);
    if (m) env[m[1]] = m[2].replace(/^["']|["']$/g, "");
  }
  return env;
}

function writeEnv(env: Record<string, string>, pythonBin: string) {
  const v = (k: string) => env[k] ?? "";
  const lines = [
    `# Big Michael — generated by npm run setup`,
    `# ${new Date().toISOString()}`,
    ``,
    `# ── Core ─────────────────────────────────────────────────────`,
    `ANTHROPIC_API_KEY=${v("ANTHROPIC_API_KEY")}`,
    `PORT=${v("PORT") || "3101"}`,
    `BIG_MICHAEL_MODE=${v("BIG_MICHAEL_MODE") || "auto"}`,
    `DATA_DIR=${v("DATA_DIR") || "./data"}`,
    ``,
    `# ── PDF Tools ────────────────────────────────────────────────`,
    `PYTHON_BIN=${pythonBin}`,
    ``,
    `# ── Legal Research ───────────────────────────────────────────`,
    `TAVILY_API_KEY=${v("TAVILY_API_KEY")}`,
    `COURT_LISTENER_API_KEY=${v("COURT_LISTENER_API_KEY")}`,
    `WESTLAW_API_KEY=${v("WESTLAW_API_KEY")}`,
    `TRELLIS_API_KEY=${v("TRELLIS_API_KEY")}`,
    `EVERLAW_API_KEY=${v("EVERLAW_API_KEY")}`,
    `DESCRYBE_API_KEY=${v("DESCRYBE_API_KEY")}`,
    `SOLVE_INTELLIGENCE_API_KEY=${v("SOLVE_INTELLIGENCE_API_KEY")}`,
    ``,
    `# ── Contract & Document Management ──────────────────────────`,
    `IRONCLAD_API_KEY=${v("IRONCLAD_API_KEY")}`,
    `DOCUSIGN_API_KEY=${v("DOCUSIGN_API_KEY")}`,
    `IMANAGE_API_KEY=${v("IMANAGE_API_KEY")}`,
    `DEFINELY_API_KEY=${v("DEFINELY_API_KEY")}`,
    `LAWVE_API_KEY=${v("LAWVE_API_KEY")}`,
    ``,
    `# ── Productivity & Files ─────────────────────────────────────`,
    `GOOGLE_DRIVE_API_KEY=${v("GOOGLE_DRIVE_API_KEY")}`,
    `BOX_API_KEY=${v("BOX_API_KEY")}`,
    `SLACK_API_KEY=${v("SLACK_API_KEY")}`,
    `TOPCOUNSEL_API_KEY=${v("TOPCOUNSEL_API_KEY")}`,
    ``,
    `# ── E-Signature (DocuSeal — self-hosted) ─────────────────────`,
    `DOCUSEAL_API_KEY=${v("DOCUSEAL_API_KEY")}`,
    `DOCUSEAL_BASE_URL=${v("DOCUSEAL_BASE_URL") || "http://localhost:3000"}`,
    ``,
    `# ── Practice Management (Clio) ──────────────────────────────`,
    `CLIO_CLIENT_ID=${v("CLIO_CLIENT_ID")}`,
    `CLIO_CLIENT_SECRET=${v("CLIO_CLIENT_SECRET")}`,
    `CLIO_REDIRECT_URI=${v("CLIO_REDIRECT_URI") || "http://localhost:3101/auth/clio/callback"}`,
    `CLIO_REGION=${v("CLIO_REGION") || "us"}`,
    ``,
    `# ── Authentication ───────────────────────────────────────────`,
    `AUTH_ENABLED=${v("AUTH_ENABLED") || "false"}`,
    `SESSION_SECRET=${v("SESSION_SECRET") || crypto.randomBytes(32).toString("hex")}`,
    `GOOGLE_CLIENT_ID=${v("GOOGLE_CLIENT_ID")}`,
    `GOOGLE_CLIENT_SECRET=${v("GOOGLE_CLIENT_SECRET")}`,
    `MICROSOFT_CLIENT_ID=${v("MICROSOFT_CLIENT_ID")}`,
    `MICROSOFT_CLIENT_SECRET=${v("MICROSOFT_CLIENT_SECRET")}`,
    ``,
    `# ── TypeDB Conflict Graph ────────────────────────────────────`,
    `TYPEDB_URL=${v("TYPEDB_URL")}`,
    `TYPEDB_DATABASE=${v("TYPEDB_DATABASE") || "big-michael"}`,
    ``,
    `# ── Local Inference (Ollama) ─────────────────────────────────`,
    `OLLAMA_ENABLED=${v("OLLAMA_ENABLED") || "false"}`,
    `OLLAMA_MODEL=${v("OLLAMA_MODEL") || "llama3.2"}`,
    `OLLAMA_BASE_URL=${v("OLLAMA_BASE_URL") || "http://localhost:11434"}`,
    `OLLAMA_TIERS=${v("OLLAMA_TIERS") || "3"}`,
    ``,
    `# ── Infisical Secrets Manager ────────────────────────────────`,
    `INFISICAL_CLIENT_ID=${v("INFISICAL_CLIENT_ID")}`,
    `INFISICAL_CLIENT_SECRET=${v("INFISICAL_CLIENT_SECRET")}`,
    `INFISICAL_PROJECT_ID=${v("INFISICAL_PROJECT_ID")}`,
    ``,
    `# ── Docket Monitor ───────────────────────────────────────────`,
    `DOCKET_MONITOR_ENABLED=${v("DOCKET_MONITOR_ENABLED") || "true"}`,
    `DOCKET_POLL_INTERVAL_MS=${v("DOCKET_POLL_INTERVAL_MS") || "14400000"}`,
    ``,
    `# ── Deadline Calculator ──────────────────────────────────────`,
    `DEADLINES_RULES_DIR=${v("DEADLINES_RULES_DIR") || "./src/deadlines/rules"}`,
    ``,
    `# ── Cost Overrides (USD per million tokens) ──────────────────`,
    `# COST_HAIKU_IN=1.00    COST_HAIKU_OUT=5.00`,
    `# COST_SONNET_IN=3.00   COST_SONNET_OUT=15.00`,
    `# COST_OPUS_IN=15.00    COST_OPUS_OUT=75.00`,
    `# LOCAL_INFERENCE_WATTS=250`,
  ];
  fs.writeFileSync(path.join(ROOT, ".env"), lines.join("\n") + "\n");
}

// ── Feature summary ────────────────────────────────────────────────────────
function printSummary(env: Record<string, string>, sys: { python: boolean; docker: boolean; tesseract: boolean }) {
  const has = (k: string) => !!env[k];
  const row = (name: string, status: string) => padEnd(`  ${fg.bold(name)}`, 36) + status;
  printBox([
    fg.bold("  Feature summary"),
    "",
    row("AI agents (Claude)",     has("ANTHROPIC_API_KEY")      ? fg.green("✓ enabled")                             : fg.red("✗ MISSING")),
    row("Web search (Tavily)",    has("TAVILY_API_KEY")          ? fg.green("✓ enabled")                             : fg.yellow("⚡ disabled")),
    row("PDF parsing",            sys.python                     ? fg.green("✓ enabled")                             : fg.yellow("⚡ install Python 3.11+")),
    row("OCR (scanned PDFs)",     sys.tesseract                  ? fg.green("✓ enabled")                             : fg.yellow("⚡ install Tesseract")),
    row("CourtListener",          fg.green("✓ always on (free)")),
    row("Westlaw",                has("WESTLAW_API_KEY")         ? fg.green("✓ enabled")                             : fg.gray("○ not configured")),
    row("Trellis",                has("TRELLIS_API_KEY")         ? fg.green("✓ enabled")                             : fg.gray("○ not configured")),
    row("Everlaw",                has("EVERLAW_API_KEY")         ? fg.green("✓ enabled")                             : fg.gray("○ not configured")),
    row("Clio",                   has("CLIO_CLIENT_ID")          ? fg.green("✓ configured")                          : fg.gray("○ not configured")),
    row("DocuSeal (e-signature)", sys.docker ? (has("DOCUSEAL_API_KEY") ? fg.green("✓ configured") : fg.yellow("⚡ running — enter key after launch")) : fg.gray("○ Docker not found")),
    row("TypeDB conflicts",       has("TYPEDB_URL")              ? fg.green("✓ enabled")                             : fg.gray("○ not configured")),
    row("Ollama (local AI)",      env.OLLAMA_ENABLED === "true"  ? fg.green("✓ enabled")                             : fg.gray("○ not configured")),
    row("Multi-user auth",        env.AUTH_ENABLED === "true"    ? fg.green("✓ enabled")                             : fg.yellow("⚡ single-user mode")),
  ]);
}

// ── Collect keys for a connector ──────────────────────────────────────────
async function collectKeys(conn: ConnDef, env: Record<string, string>): Promise<void> {
  const hasKeys = conn.keys.some(k => !!env[k.key]);

  console.log(`\n  ${fg.bold(fg.cyan("▸"))} ${fg.bold(conn.name)}`);
  printNote(conn.blurb);

  if (!hasKeys && conn.howTo.length) {
    console.log();
    printNote("How to get access:");
    for (const line of conn.howTo) printNote(`  ${line}`);
    console.log();
  } else { console.log(); }

  for (const k of conn.keys) {
    if (k.autoGen) {
      if (!env[k.key]) env[k.key] = crypto.randomBytes(32).toString("hex");
      printOk(`${k.label} — auto-generated.`);
      continue;
    }
    const cur = env[k.key] ?? k.default ?? "";
    const val = k.secret
      ? await askSecret(k.label, !!env[k.key])
      : await ask(k.label, cur || undefined);
    env[k.key] = (val || cur).trim();
  }
}

// ── Main ───────────────────────────────────────────────────────────────────
async function main() {
  process.stdout.write("\x1b[2J\x1b[H");

  console.log(`
${fg.cyan("  ┌──────────────────────────────────────────────────────────────┐")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  │")}    ${fg.bold("⚖  Big Michael")}                                              ${fg.cyan("│")}
${fg.cyan("  │")}    ${fg.dim("Multi-agent Legal AI  ·  Setup Wizard  ·  v0.4.0")}           ${fg.cyan("│")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  └──────────────────────────────────────────────────────────────┘")}
`);

  initReadline();

  const existing = loadEnv();
  const env: Record<string, string> = { ...existing };

  if (Object.keys(existing).length > 0) {
    printWarn(`Found existing ${fg.bold(".env")} — will update it (existing keys preserved).`);
    const fresh = await confirm("  Start fresh instead?", false);
    if (fresh) for (const k of Object.keys(env)) delete env[k];
    console.log();
  }

  const sys = { python: false, docker: false, tesseract: false };

  // ── System requirements ────────────────────────────────────────────────
  printSection("System Requirements");

  const nodeR = checkNode();
  if (nodeR.ok) printOk(`Node.js ${fg.dim(nodeR.version)}`);
  else { printFail("Node.js 18+ required. Install at nodejs.org"); process.exit(1); }

  const pyR = checkPython();
  sys.python = pyR.ok;
  if (pyR.ok) printOk(`Python ${fg.dim(pyR.version)}  ${fg.gray("— PDF parsing enabled")}`);
  else {
    printSkip("Python 3.11+ not found — PDF tools disabled");
    printNote("Install at python.org  (macOS: brew install python)");
  }

  const dockerR = checkDocker();
  sys.docker = dockerR.ok;
  if (dockerR.ok) printOk(`Docker ${fg.dim(dockerR.version)}  ${fg.gray("— DocuSeal e-signature available")}`);
  else {
    printSkip("Docker not found — DocuSeal e-signature disabled");
    printNote("Install at docker.com/get-docker");
  }

  const tessR = checkTesseract();
  sys.tesseract = tessR.ok;
  if (tessR.ok) printOk(`Tesseract ${fg.dim(tessR.version)}  ${fg.gray("— OCR on scanned PDFs enabled")}`);
  else {
    printSkip("Tesseract not found — OCR disabled");
    printNote("macOS: brew install tesseract   Linux: apt install tesseract-ocr");
  }

  // ── Anthropic key (required) ───────────────────────────────────────────
  printSection("Anthropic API Key  (required)");

  printNote("Powers every AI agent — the only key you absolutely need.");
  printNote("");
  if (!env.ANTHROPIC_API_KEY) {
    printNote("Get it in 2 minutes:");
    printNote("  1. console.anthropic.com  →  sign up / log in");
    printNote("  2. API Keys  →  Create Key  →  copy it");
    printNote(`  Pricing: ~$0.01 per research task ${fg.gray("(pay-as-you-go)")}`);
  }
  console.log();

  while (true) {
    const cur = env.ANTHROPIC_API_KEY ?? "";
    const raw = await askSecret("ANTHROPIC_API_KEY", !!cur);
    const val = raw || cur;
    if (!val) { printFail("This key is required."); continue; }
    if (!val.startsWith("sk-ant-") && !val.startsWith("sk-")) {
      printFail("Anthropic keys start with sk-ant-api03-...");
      const force = await confirm("  Use this value anyway?", false);
      if (!force) continue;
    }
    env.ANTHROPIC_API_KEY = val;
    printOk("Anthropic API key saved.");
    break;
  }

  // ── Connector checkbox picker ──────────────────────────────────────────
  printSection("Connectors & Integrations");
  printNote("Select the connectors you want to configure. You can add more later.");
  console.log();

  // Pre-select any connector whose keys are already in the env
  const navigableIds = PICKER_ROWS.filter(r => !r.header).map(r => r.id);
  const preSelected = navigableIds.filter(id => {
    const conn = CONNECTORS[id];
    return conn?.keys.some(k => !!existing[k.key]);
  });

  const chosen = await multiSelect("Choose connectors:", PICKER_ROWS, preSelected);

  // Collect keys for each selected connector
  for (const id of chosen) {
    const conn = CONNECTORS[id];
    if (conn) await collectKeys(conn, env);
  }

  if (chosen.length === 0) {
    printSkip("No connectors selected — you can add them any time by editing .env");
  }

  // ── Write .env ─────────────────────────────────────────────────────────
  printSection("Writing Configuration");

  writeEnv(env, pyR.bin);
  printOk(`.env written at ${fg.dim(path.join(ROOT, ".env"))}`);

  // ── Install ────────────────────────────────────────────────────────────
  printSection("Installation");

  const doInstall = await confirm("Run npm install?", true);
  let installed = false;
  if (doInstall) {
    const spin = new Spinner();
    spin.start("npm install  (takes ~30 seconds)");
    try {
      execSync("npm install", { cwd: ROOT, stdio: "pipe", timeout: 120_000 });
      spin.done("Dependencies installed.");
      installed = true;
    } catch (e: any) {
      spin.fail(`npm install failed — ${String(e.message).split("\n")[0]}`);
    }
  }

  if (sys.docker) {
    console.log();
    const doDocker = await confirm("Start DocuSeal (e-signature) via Docker Compose?", false);
    if (doDocker) {
      const spin = new Spinner();
      spin.start("docker compose up -d");
      try {
        execSync("docker compose up -d", { cwd: ROOT, stdio: "pipe", timeout: 90_000 });
        spin.done("DocuSeal running at http://localhost:3000");
        printNote("Visit http://localhost:3000 to complete first-run setup.");
        printNote(`Copy the API key, then re-run ${fg.bold("npm run setup")}.`);
      } catch {
        spin.fail("Docker Compose failed — is Docker Desktop running?");
      }
    }
  }

  console.log();
  const doSmoke = await confirm("Run smoke test to verify the setup?", installed);
  if (doSmoke) {
    const spin = new Spinner();
    spin.start("npm run smoke-test");
    try {
      execSync("npm run smoke-test", { cwd: ROOT, stdio: "pipe", timeout: 60_000 });
      spin.done("Smoke test passed.");
    } catch {
      spin.fail("Smoke test failed.");
      printNote("Check that ANTHROPIC_API_KEY is correct, then run:  npm run smoke-test");
    }
  }

  // ── Summary ────────────────────────────────────────────────────────────
  console.log("\n");
  printSummary(env, sys);

  console.log(`
${fg.cyan("  ┌──────────────────────────────────────────────────────────────┐")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  │")}  ${fg.bold("You're ready. Welcome to Big Michael.")}                       ${fg.cyan("│")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  │")}  ${fg.bold("Start the server:")}                                            ${fg.cyan("│")}
${fg.cyan("  │")}    ${fg.green("npm run dev")}    ${fg.dim("development mode (hot reload)")}              ${fg.cyan("│")}
${fg.cyan("  │")}    ${fg.green("npm start")}      ${fg.dim("production (npm run build first)")}          ${fg.cyan("│")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  │")}  REST API     →  http://localhost:3101                       ${fg.cyan("│")}
${fg.cyan("  │")}  Health       →  http://localhost:3101/health                ${fg.cyan("│")}
${fg.cyan("  │")}  Agents       →  http://localhost:3101/agents                ${fg.cyan("│")}
${fg.cyan("  │")}                                                              ${fg.cyan("│")}
${fg.cyan("  │")}  ${fg.dim("Add connectors: npm run setup")}                               ${fg.cyan("│")}
${fg.cyan("  └──────────────────────────────────────────────────────────────┘")}
`);

  _rl.close();
}

main().catch(err => {
  console.error(`\n  ${fg.red("✗")}  ${err.message}`);
  process.exit(1);
});
