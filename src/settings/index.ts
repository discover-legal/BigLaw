// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Runtime settings store — backs the admin panel.
 *
 * The orchestration knobs (DyTopo depth, verification passes, gate threshold)
 * and the DocuSeal connection live on the shared `Config` object, which every
 * read site consults at runtime. This store persists a small JSON file and
 * applies it onto `Config` in place, so admin-panel changes take effect live
 * without a restart. Env vars remain the defaults; the file overrides them.
 */

import { readFile, writeFile, rename } from "fs/promises";
import { Config as ConfigConst } from "../config.js";
import { logger } from "../logger.js";

export interface AppSettings {
  presentation: {
    /** "lawyer" = full legal terminology + citations; "plain" = plain-language for non-lawyers. */
    mode: "lawyer" | "plain";
    firmName: string;
  };
  dytopo: {
    maxRounds: number;
    maxAgentsPerRound: number;
    similarityThreshold: number;
  };
  debate: {
    verificationPasses: number;
    gateConfidenceThreshold: number;
    adversarialEnabled: boolean;
    citationRequired: boolean;
  };
  docuseal: {
    enabled: boolean;
    url: string;
    /** Stored but never returned to clients (see redactedSettings). */
    apiKey: string;
  };
}

/** Build the current settings object straight from the live Config. */
function currentSettings(): AppSettings {
  const Config = ConfigConst;
  return {
    presentation: { mode: Config.presentation.mode, firmName: Config.presentation.firmName },
    dytopo: {
      maxRounds: Config.dytopo.maxRounds,
      maxAgentsPerRound: Config.dytopo.maxAgentsPerRound,
      similarityThreshold: Config.dytopo.similarityThreshold,
    },
    debate: {
      verificationPasses: Config.debate.verificationPasses,
      gateConfidenceThreshold: Config.debate.gateConfidenceThreshold,
      adversarialEnabled: Config.debate.adversarialEnabled,
      citationRequired: Config.debate.citationRequired,
    },
    docuseal: { enabled: Config.docuseal.enabled, url: Config.docuseal.url, apiKey: Config.docuseal.apiKey },
  };
}

const clampInt = (v: unknown, lo: number, hi: number, dflt: number): number => {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : dflt;
};
const clampFloat = (v: unknown, lo: number, hi: number, dflt: number): number => {
  const n = Number(v);
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : dflt;
};

/**
 * Validate that `raw` is a public http/https URL (no SSRF via private/loopback addresses).
 * Returns the trimmed URL on success; throws a descriptive Error on failure.
 * Exported for unit-testing.
 */
export function assertPublicHttpUrl(raw: string, label: string): string {
  const trimmed = raw.trim();
  let u: URL;
  try {
    u = new URL(trimmed);
  } catch {
    throw new Error(`Invalid ${label} '${trimmed}': must be a public http or https URL`);
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") {
    throw new Error(`Invalid ${label} '${trimmed}': must be a public http or https URL`);
  }
  // Strip IPv6 brackets for hostname matching.
  const h = u.hostname.toLowerCase().replace(/^\[|\]$/g, "");
  const isPrivate =
    h === "localhost" ||
    h === "::1" ||               // IPv6 loopback
    h === "0.0.0.0" ||           // unspecified address
    /^127\./.test(h) ||          // 127.0.0.0/8  loopback
    /^169\.254\./.test(h) ||     // 169.254.0.0/16  link-local
    /^10\./.test(h) ||           // 10.0.0.0/8  RFC-1918
    /^172\.(1[6-9]|2\d|3[01])\./.test(h) ||  // 172.16-31.x  RFC-1918
    /^192\.168\./.test(h) ||     // 192.168.0.0/16  RFC-1918
    /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./.test(h) || // 100.64.0.0/10  IANA shared address space
    /^::ffff:/i.test(h) ||       // IPv4-mapped IPv6
    /^fc00:/i.test(h) ||         // fc00::/7  IPv6 ULA
    /^fe80:/i.test(h);           // fe80::/10  IPv6 link-local
  if (isPrivate) {
    throw new Error(`${label} '${trimmed}' resolves to a private or loopback address`);
  }
  return trimmed;
}

/** Apply a (partial) settings object onto the live Config, with validation. */
function applyToConfig(s: DeepPartial<AppSettings>): void {
  // Config is declared `as const` (compile-time readonly); at runtime it is a
  // plain mutable object. Cast to a mutable view to apply live overrides.
  const Config = ConfigConst as unknown as {
    presentation: { mode: "lawyer" | "plain"; firmName: string };
    dytopo: { maxRounds: number; maxAgentsPerRound: number; similarityThreshold: number };
    debate: { verificationPasses: number; gateConfidenceThreshold: number; adversarialEnabled: boolean; citationRequired: boolean };
    docuseal: { enabled: boolean; url: string; apiKey: string };
  };
  if (s.presentation) {
    if (s.presentation.mode === "lawyer" || s.presentation.mode === "plain") Config.presentation.mode = s.presentation.mode;
    if (typeof s.presentation.firmName === "string") Config.presentation.firmName = s.presentation.firmName.slice(0, 200);
  }
  if (s.dytopo) {
    if (s.dytopo.maxRounds !== undefined) Config.dytopo.maxRounds = clampInt(s.dytopo.maxRounds, 1, 30, Config.dytopo.maxRounds);
    if (s.dytopo.maxAgentsPerRound !== undefined) Config.dytopo.maxAgentsPerRound = clampInt(s.dytopo.maxAgentsPerRound, 1, 48, Config.dytopo.maxAgentsPerRound);
    if (s.dytopo.similarityThreshold !== undefined) Config.dytopo.similarityThreshold = clampFloat(s.dytopo.similarityThreshold, 0.1, 0.99, Config.dytopo.similarityThreshold);
  }
  if (s.debate) {
    if (s.debate.verificationPasses !== undefined) Config.debate.verificationPasses = clampInt(s.debate.verificationPasses, 0, 25, Config.debate.verificationPasses);
    if (s.debate.gateConfidenceThreshold !== undefined) Config.debate.gateConfidenceThreshold = clampFloat(s.debate.gateConfidenceThreshold, 0, 1, Config.debate.gateConfidenceThreshold);
    if (typeof s.debate.adversarialEnabled === "boolean") Config.debate.adversarialEnabled = s.debate.adversarialEnabled;
    if (typeof s.debate.citationRequired === "boolean") Config.debate.citationRequired = s.debate.citationRequired;
  }
  if (s.docuseal) {
    if (typeof s.docuseal.enabled === "boolean") Config.docuseal.enabled = s.docuseal.enabled;
    if (typeof s.docuseal.url === "string") {
      Config.docuseal.url = assertPublicHttpUrl(s.docuseal.url, "DocuSeal URL");
    }
    if (typeof s.docuseal.apiKey === "string") Config.docuseal.apiKey = s.docuseal.apiKey.trim();
  }
}

export class SettingsStore {
  private readonly path = ConfigConst.persistence.settingsFile;

  /** Load persisted overrides (if any) and apply them onto Config. */
  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      applyToConfig(JSON.parse(raw) as DeepPartial<AppSettings>);
      logger.info("Settings loaded", { path: this.path });
    } catch {
      // No settings file yet — Config defaults (from env) stand.
    }
  }

  /** Current effective settings, with the DocuSeal key redacted for clients. */
  get(): Omit<AppSettings, "docuseal"> & { docuseal: Omit<AppSettings["docuseal"], "apiKey"> & { apiKeySet: boolean } } {
    const s = currentSettings();
    const { apiKey, ...docusealPublic } = s.docuseal;
    return { ...s, docuseal: { ...docusealPublic, apiKeySet: !!apiKey } };
  }

  /** Apply a patch onto Config and persist the full settings to disk. */
  async update(patch: DeepPartial<AppSettings>): Promise<ReturnType<SettingsStore["get"]>> {
    applyToConfig(patch);
    // Atomic write: write to .tmp then rename so a mid-write crash doesn't
    // leave a partial JSON file that breaks init() on the next startup.
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(currentSettings(), null, 2), "utf8");
    await rename(tmp, this.path);
    logger.info("Settings updated", { path: this.path });
    return this.get();
  }
}

type DeepPartial<T> = { [K in keyof T]?: T[K] extends object ? DeepPartial<T[K]> : T[K] };
