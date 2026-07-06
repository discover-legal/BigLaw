// Render collateral/apache-release-deck/deck.html → collateral/apache-release-deck.pdf
// via Chromium PDF printing (Playwright). 15 pages, 1080×1350 px each (LinkedIn portrait carousel).
//
// Usage:
//   npm i playwright pdf-lib        # anywhere on the resolution path, or point PLAYWRIGHT_DIR
//   node render.mjs                 # optional: PLAYWRIGHT_DIR=/path/to/node_modules
//
// If the bundled Chromium is not installed (`npx playwright install chromium`), the script
// falls back to the system Edge/Chrome channel — both are Chromium PDF printing.
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import fs from "node:fs";

const here = path.dirname(fileURLToPath(import.meta.url));

async function load(mod) {
  const dir = process.env.PLAYWRIGHT_DIR;
  if (dir) {
    const entry = path.join(dir, mod, mod === "pdf-lib" ? "cjs/index.js" : "index.mjs");
    if (fs.existsSync(entry)) return import(pathToFileURL(entry).href);
  }
  return import(mod);
}

const { chromium } = await load("playwright");

let browser;
for (const opts of [{}, { channel: "msedge" }, { channel: "chrome" }]) {
  try { browser = await chromium.launch(opts); break; } catch { /* next */ }
}
if (!browser) throw new Error("No Chromium available: run `npx playwright install chromium` or install Edge/Chrome.");

const page = await browser.newPage({ viewport: { width: 1080, height: 1350 } });
await page.goto(pathToFileURL(path.join(here, "deck.html")).href, { waitUntil: "networkidle" });

// ---- overflow assertions: every slide must fit its 1080×1350 page exactly ----
const report = await page.evaluate(() => {
  return [...document.querySelectorAll(".slide")].map((s, i) => ({
    slide: i + 1,
    w: s.scrollWidth, h: s.scrollHeight,
    overflow: s.scrollWidth > 1080 || s.scrollHeight > 1350,
  }));
});
let bad = report.filter(r => r.overflow);
console.log(`slides: ${report.length}`);
for (const r of bad) console.log(`  OVERFLOW slide ${r.slide}: ${r.w}×${r.h}`);
if (bad.length) { await browser.close(); process.exit(1); }

// ---- optional per-slide screenshots for visual QA: node render.mjs --shots <dir> ----
const shotsIdx = process.argv.indexOf("--shots");
if (shotsIdx !== -1) {
  const dir = process.argv[shotsIdx + 1] || path.join(here, "shots");
  fs.mkdirSync(dir, { recursive: true });
  const slides = page.locator(".slide");
  for (let i = 0; i < report.length; i++) {
    await slides.nth(i).screenshot({ path: path.join(dir, `slide-${String(i + 1).padStart(2, "0")}.png`) });
  }
  console.log(`screenshots → ${dir}`);
}

// ---- print to PDF ----
const pdfPath = path.join(here, "..", "apache-release-deck.pdf");
await page.pdf({ path: pdfPath, width: "1080px", height: "1350px", printBackground: true });
await browser.close();

// ---- verify the PDF: page count + page size ----
const { PDFDocument } = await load("pdf-lib");
const doc = await PDFDocument.load(fs.readFileSync(pdfPath));
const n = doc.getPageCount();
const { width, height } = doc.getPage(0).getSize();
console.log(`pdf: ${pdfPath}`);
console.log(`pages: ${n} · page size: ${width.toFixed(1)}×${height.toFixed(1)} pt (expect 810×1012.5 = 1080×1350 px)`);
if (n !== report.length) { console.log("PAGE COUNT MISMATCH"); process.exit(1); }
console.log("OK");
