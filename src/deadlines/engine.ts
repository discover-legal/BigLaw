// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * DeadlineEngine — pure calendar arithmetic, no LLM, no network, no new npm packages.
 *
 * Loads YAML jurisdiction rule files and computes all downstream court/filing
 * deadlines from a trigger event and date. Non-engineers can add new jurisdictions
 * by dropping a YAML file in the rules directory — no code changes ever.
 */

import { readdir, readFile } from "fs/promises";
import { join, extname } from "path";
import { logger } from "../logger.js";
import type { JurisdictionRules, DeadlineRule, ComputedDeadline, DeadlineResult } from "./types.js";

// ─── Minimal YAML parser ──────────────────────────────────────────────────────
// Handles the simple key-value + list structure used in our rule files.
// No anchors, no complex types, no multi-document streams — just what we need.

function parseYaml(text: string): unknown {
  const lines = text.split(/\r?\n/);
  return parseBlock(lines, 0, 0).value;
}

interface ParseResult {
  value: unknown;
  nextLine: number;
}

function parseValue(raw: string): unknown {
  const s = raw.trim();
  if (s === "true") return true;
  if (s === "false") return false;
  if (s === "null" || s === "~") return null;
  // Quoted string
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1);
  }
  // Number
  if (/^-?\d+(\.\d+)?$/.test(s)) return Number(s);
  // Bare string
  return s;
}

function getIndent(line: string): number {
  let i = 0;
  while (i < line.length && line[i] === " ") i++;
  return i;
}

function isBlankOrComment(line: string): boolean {
  const t = line.trim();
  return t === "" || t.startsWith("#");
}

function parseBlock(lines: string[], startLine: number, baseIndent: number): ParseResult {
  // Skip blanks/comments to find what kind of block this is
  let i = startLine;
  while (i < lines.length && isBlankOrComment(lines[i])) i++;
  if (i >= lines.length) return { value: null, nextLine: i };

  const firstIndent = getIndent(lines[i]);
  const firstTrimmed = lines[i].trim();

  // Sequence block — first non-blank line at this level starts with "- "
  if (firstTrimmed.startsWith("- ")) {
    return parseSequence(lines, i, firstIndent);
  }

  // Mapping block — first non-blank line at this level is a "key: ..." or "key:"
  if (/^\w[\w\-]*\s*:/.test(firstTrimmed)) {
    return parseMapping(lines, i, firstIndent);
  }

  // Fall-through: treat as scalar
  return { value: parseValue(firstTrimmed), nextLine: i + 1 };
}

function parseSequence(lines: string[], startLine: number, baseIndent: number): ParseResult {
  const result: unknown[] = [];
  let i = startLine;

  while (i < lines.length) {
    if (isBlankOrComment(lines[i])) { i++; continue; }
    const indent = getIndent(lines[i]);
    if (indent < baseIndent) break;  // dedented past our block
    if (indent > baseIndent) { i++; continue; } // shouldn't happen at top of item

    const trimmed = lines[i].trim();
    if (!trimmed.startsWith("- ")) break;  // no longer a sequence item at this indent

    const rest = trimmed.slice(2).trim();
    i++;

    if (rest === "" || rest.startsWith("#")) {
      // Value is on subsequent indented lines — it's a nested block
      const nested = parseBlock(lines, i, baseIndent + 2);
      result.push(nested.value);
      i = nested.nextLine;
    } else if (/^\w[\w\-]*\s*:/.test(rest)) {
      // Inline mapping start: "- key: value"
      // First, collect the inline key-value
      const obj: Record<string, unknown> = {};
      const colonIdx = rest.indexOf(":");
      const key = rest.slice(0, colonIdx).trim();
      const val = rest.slice(colonIdx + 1).trim();

      if (val === "" || val.startsWith("#")) {
        // Value block follows on next lines
        const nested = parseBlock(lines, i, baseIndent + 2);
        obj[key] = nested.value;
        i = nested.nextLine;
      } else {
        obj[key] = parseValue(val);
      }

      // Collect sibling keys at baseIndent+2
      while (i < lines.length) {
        if (isBlankOrComment(lines[i])) { i++; continue; }
        const ind = getIndent(lines[i]);
        if (ind <= baseIndent) break;  // back to sequence level or above
        const t = lines[i].trim();
        if (!(/^\w[\w\-]*\s*:/.test(t))) { i++; continue; }
        const ci = t.indexOf(":");
        const k = t.slice(0, ci).trim();
        const v = t.slice(ci + 1).trim();
        i++;
        if (v === "" || v.startsWith("#")) {
          const nested = parseBlock(lines, i, ind + 2);
          obj[k] = nested.value;
          i = nested.nextLine;
        } else {
          obj[k] = parseValue(v);
        }
      }

      result.push(obj);
    } else {
      result.push(parseValue(rest));
    }
  }

  return { value: result, nextLine: i };
}

function parseMapping(lines: string[], startLine: number, baseIndent: number): ParseResult {
  const obj: Record<string, unknown> = {};
  let i = startLine;

  while (i < lines.length) {
    if (isBlankOrComment(lines[i])) { i++; continue; }
    const indent = getIndent(lines[i]);
    if (indent < baseIndent) break;
    if (indent > baseIndent) { i++; continue; }

    const trimmed = lines[i].trim();
    if (!(/^\w[\w\-]*\s*:/.test(trimmed))) break;

    const colonIdx = trimmed.indexOf(":");
    const key = trimmed.slice(0, colonIdx).trim();
    const rest = trimmed.slice(colonIdx + 1).trim();
    i++;

    if (rest === "" || rest.startsWith("#")) {
      // Value is on next indented lines
      // Peek to see if it's a sequence or nested mapping
      let j = i;
      while (j < lines.length && isBlankOrComment(lines[j])) j++;
      if (j < lines.length) {
        const nextIndent = getIndent(lines[j]);
        if (nextIndent > baseIndent) {
          const nested = parseBlock(lines, i, nextIndent);
          obj[key] = nested.value;
          i = nested.nextLine;
        } else {
          obj[key] = null;
        }
      } else {
        obj[key] = null;
      }
    } else {
      obj[key] = parseValue(rest);
    }
  }

  return { value: obj, nextLine: i };
}

// ─── Easter (Butcher/Meeus algorithm) ────────────────────────────────────────

function easterDate(year: number): Date {
  const a = year % 19;
  const b = Math.floor(year / 100);
  const c = year % 100;
  const d = Math.floor(b / 4);
  const e = b % 4;
  const f = Math.floor((b + 8) / 25);
  const g = Math.floor((b - f + 1) / 3);
  const h = (19 * a + b - d - g + 15) % 30;
  const i = Math.floor(c / 4);
  const k = c % 4;
  const l = (32 + 2 * e + 2 * i - h - k) % 7;
  const m = Math.floor((a + 11 * h + 22 * l) / 451);
  const month = Math.floor((h + l - 7 * m + 114) / 31); // 1-based
  const day = ((h + l - 7 * m + 114) % 31) + 1;
  return new Date(Date.UTC(year, month - 1, day));
}

function addDays(d: Date, n: number): Date {
  return new Date(d.getTime() + n * 86_400_000);
}

// ─── Holiday tables ───────────────────────────────────────────────────────────

/** nth weekday of a month: e.g. nthWeekday(year, month, 1 [Mon], 3) = 3rd Monday */
function nthWeekday(year: number, month: number, dayOfWeek: number, n: number): Date {
  // month is 0-based
  const first = new Date(Date.UTC(year, month, 1));
  const firstDow = first.getUTCDay(); // 0=Sun
  let diff = dayOfWeek - firstDow;
  if (diff < 0) diff += 7;
  const firstOccurrence = diff + 1;
  return new Date(Date.UTC(year, month, firstOccurrence + (n - 1) * 7));
}

/** last weekday of month */
function lastWeekday(year: number, month: number, dayOfWeek: number): Date {
  // month is 0-based
  const lastDay = new Date(Date.UTC(year, month + 1, 0)); // last day of month
  let diff = lastDay.getUTCDay() - dayOfWeek;
  if (diff < 0) diff += 7;
  return new Date(Date.UTC(year, month, lastDay.getUTCDate() - diff));
}

function usFederalHolidays(year: number): Set<string> {
  const holidays: Date[] = [
    new Date(Date.UTC(year, 0, 1)),            // New Year's Day
    nthWeekday(year, 0, 1, 3),                 // MLK Day (3rd Mon Jan)
    nthWeekday(year, 1, 1, 3),                 // Presidents Day (3rd Mon Feb)
    lastWeekday(year, 4, 1),                   // Memorial Day (last Mon May)
    new Date(Date.UTC(year, 5, 19)),            // Juneteenth (Jun 19)
    new Date(Date.UTC(year, 6, 4)),             // Independence Day (Jul 4)
    nthWeekday(year, 8, 1, 1),                 // Labor Day (1st Mon Sep)
    nthWeekday(year, 9, 1, 2),                 // Columbus Day (2nd Mon Oct)
    new Date(Date.UTC(year, 10, 11)),           // Veterans Day (Nov 11)
    nthWeekday(year, 10, 4, 4),                // Thanksgiving (4th Thu Nov)
    new Date(Date.UTC(year, 11, 25)),           // Christmas (Dec 25)
  ];
  // When a holiday falls on Saturday, observe Friday; Sunday → Monday
  const observed: Date[] = holidays.map((d) => {
    const dow = d.getUTCDay();
    if (dow === 6) return addDays(d, -1); // Saturday → Friday
    if (dow === 0) return addDays(d, 1);  // Sunday → Monday
    return d;
  });
  return new Set(observed.map(isoDate));
}

function ukBankHolidays(year: number): Set<string> {
  const easter = easterDate(year);
  const holidays: Date[] = [
    new Date(Date.UTC(year, 0, 1)),            // New Year's Day
    addDays(easter, -2),                        // Good Friday
    addDays(easter, 1),                         // Easter Monday
    nthWeekday(year, 4, 1, 1),                 // Early May BH (1st Mon May)
    lastWeekday(year, 4, 1),                   // Spring BH (last Mon May)
    lastWeekday(year, 7, 1),                   // Summer BH (last Mon Aug)
    new Date(Date.UTC(year, 11, 25)),           // Christmas
    new Date(Date.UTC(year, 11, 26)),           // Boxing Day
  ];
  // Substitute rules for Christmas/Boxing Day/New Year weekends
  const result: Set<string> = new Set();
  for (const d of holidays) {
    const dow = d.getUTCDay();
    if (dow === 6) {
      result.add(isoDate(addDays(d, 2))); // Sat → Mon
    } else if (dow === 0) {
      result.add(isoDate(addDays(d, 1))); // Sun → Mon
    } else {
      result.add(isoDate(d));
    }
  }
  return result;
}

function euInstitutionHolidays(year: number): Set<string> {
  const easter = easterDate(year);
  // Ascension = 39 days after Easter; Whit Monday = 50 days after Easter
  const holidays: Date[] = [
    new Date(Date.UTC(year, 0, 1)),            // New Year's Day
    addDays(easter, 1),                         // Easter Monday
    new Date(Date.UTC(year, 4, 1)),             // Labour Day
    addDays(easter, 39),                        // Ascension Thursday
    addDays(easter, 50),                        // Whit Monday
    new Date(Date.UTC(year, 11, 25)),           // Christmas Day
    new Date(Date.UTC(year, 11, 26)),           // Second Christmas Day
  ];
  return new Set(holidays.map(isoDate));
}

// Cache holiday sets per (year, holiday-type) to avoid recomputation
const holidayCache = new Map<string, Set<string>>();

function getHolidays(year: number, type: JurisdictionRules["holidays"]): Set<string> {
  if (type === "none") return new Set();
  const key = `${year}:${type}`;
  if (holidayCache.has(key)) return holidayCache.get(key)!;
  let set: Set<string>;
  if (type === "us_federal") set = usFederalHolidays(year);
  else if (type === "uk_bank") set = ukBankHolidays(year);
  else set = euInstitutionHolidays(year);
  holidayCache.set(key, set);
  return set;
}

// ─── Calendar helpers ─────────────────────────────────────────────────────────

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}

export function isWeekend(date: Date): boolean {
  const dow = date.getUTCDay();
  return dow === 0 || dow === 6; // Sun or Sat
}

export function isHoliday(date: Date, holidays: JurisdictionRules["holidays"]): boolean {
  if (holidays === "none") return false;
  const year = date.getUTCFullYear();
  return getHolidays(year, holidays).has(isoDate(date));
}

export function isBusinessDay(date: Date, holidays: JurisdictionRules["holidays"]): boolean {
  return !isWeekend(date) && !isHoliday(date, holidays);
}

/** Add exactly `days` calendar days (weekends and holidays included). */
export function addCalendarDays(start: Date, days: number): Date {
  return addDays(start, days);
}

/** Add `days` business days, skipping weekends and holidays. */
export function addBusinessDays(start: Date, days: number, holidays: JurisdictionRules["holidays"]): Date {
  let current = new Date(start.getTime());
  let remaining = days;
  while (remaining > 0) {
    current = addDays(current, 1);
    if (isBusinessDay(current, holidays)) {
      remaining--;
    }
  }
  return current;
}

// ─── DeadlineEngine ───────────────────────────────────────────────────────────

export class DeadlineEngine {
  private readonly rules: Map<string, JurisdictionRules> = new Map();

  /** Load all .yaml / .yml files from a directory. */
  async loadRulesDir(dir: string): Promise<void> {
    let files: string[];
    try {
      files = await readdir(dir);
    } catch {
      logger.warn(`DeadlineEngine: rules directory not found: ${dir}`);
      return;
    }

    const yamlFiles = files.filter((f) => extname(f) === ".yaml" || extname(f) === ".yml");
    let loaded = 0;
    for (const file of yamlFiles) {
      try {
        const text = await readFile(join(dir, file), "utf8");
        const parsed = parseYaml(text) as JurisdictionRules;
        this.loadRules(parsed);
        loaded++;
      } catch (err) {
        logger.warn(`DeadlineEngine: failed to load ${file}: ${(err as Error).message}`);
      }
    }
    logger.info(`DeadlineEngine: loaded ${loaded} rule set(s) from ${dir}`);
  }

  /** Load a single parsed ruleset. */
  loadRules(parsed: JurisdictionRules): void {
    if (!parsed || !parsed.jurisdiction || !parsed.rules) {
      throw new Error(`Invalid ruleset: missing required fields`);
    }
    this.rules.set(parsed.jurisdiction.toUpperCase(), parsed);
  }

  /** List all loaded jurisdictions. */
  listJurisdictions(): Array<{ jurisdiction: string; name: string; id: string; ruleCount: number }> {
    return Array.from(this.rules.values()).map((r) => ({
      jurisdiction: r.jurisdiction,
      name: r.name,
      id: r.id,
      ruleCount: r.rules.length,
    }));
  }

  /**
   * Compute all deadlines for a given trigger event and date.
   * Throws if the jurisdiction is not loaded.
   */
  compute(
    jurisdiction: string,
    triggerEvent: string,
    triggerDate: string | Date,
  ): DeadlineResult {
    const jKey = jurisdiction.toUpperCase();
    const ruleset = this.rules.get(jKey);
    if (!ruleset) {
      throw new Error(`No rules loaded for jurisdiction: ${jurisdiction}`);
    }

    const trigger = triggerEvent.trim().toLowerCase();
    const matching: DeadlineRule[] = ruleset.rules.filter(
      (r) => r.trigger.trim().toLowerCase() === trigger,
    );

    // Parse trigger date as UTC midnight. Accept either a bare YYYY-MM-DD or a
    // full ISO timestamp — slice to the date component so a datetime input does
    // not produce an Invalid Date (which would silently make every deadline NaN).
    const tDate =
      typeof triggerDate === "string"
        ? new Date(triggerDate.slice(0, 10) + "T00:00:00Z")
        : new Date(Date.UTC(triggerDate.getUTCFullYear(), triggerDate.getUTCMonth(), triggerDate.getUTCDate()));
    if (isNaN(tDate.getTime())) {
      throw new Error(`Invalid triggerDate: '${String(triggerDate)}' — expected YYYY-MM-DD`);
    }

    const deadlines: ComputedDeadline[] = matching.map((rule) => {
      let dueDate =
        rule.dayType === "calendar"
          ? addCalendarDays(tDate, rule.days)
          : addBusinessDays(tDate, rule.days, ruleset.holidays);

      // When a calendar-day period lands on a weekend or holiday, roll forward to
      // the next business day (FRCP 6(a)(1)(C), UK CPR 2.8(5), EU equivalents).
      // The business-day path already lands on a business day by construction.
      // A rule may opt out via `rollForward: false` for the rare non-rolling statute.
      if (rule.dayType === "calendar" && (rule as { rollForward?: boolean }).rollForward !== false) {
        while (!isBusinessDay(dueDate, ruleset.holidays)) {
          dueDate = addCalendarDays(dueDate, 1);
        }
      }

      const dueDateStr = isoDate(dueDate);

      let warningDate: string | undefined;
      if (rule.warningDays && rule.warningDays > 0) {
        const wDate = addCalendarDays(dueDate, -rule.warningDays);
        warningDate = isoDate(wDate);
      }

      const entry: ComputedDeadline = {
        ruleId: rule.id,
        event: rule.event,
        dueDate: dueDateStr,
        days: rule.days,
        dayType: rule.dayType,
        cite: rule.cite,
        ...(warningDate ? { warningDate } : {}),
        ...(rule.note ? { note: rule.note } : {}),
      };
      return entry;
    });

    // Sort by dueDate ascending
    deadlines.sort((a, b) => a.dueDate.localeCompare(b.dueDate));

    return {
      jurisdiction: ruleset.jurisdiction,
      jurisdictionName: ruleset.name,
      triggerEvent,
      triggerDate: isoDate(tDate),
      computedAt: new Date().toISOString(),
      deadlines,
    };
  }
}
