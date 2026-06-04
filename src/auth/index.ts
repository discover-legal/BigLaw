// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Lawyer profiles, roles, and row-level access control.
 *
 * Access model:
 *   - partner (admin): sees every matter; assigns lawyers to matters.
 *   - lawyer: sees ONLY matters whose `assignedLawyerIds` contains their id.
 *     There is no inter-lawyer visibility unless a partner has assigned more
 *     than one lawyer to the same matter.
 *
 * Auth model:
 *   - Config.auth.enabled === false (local default): no login. Every request is
 *     the synthetic LOCAL_PARTNER, who sees everything — single-user dev.
 *   - Config.auth.enabled === true: the principal comes from the session (set by
 *     the OAuth callback). Requests without a session are unauthenticated.
 */

import { randomUUID } from "crypto";
import { readFile, writeFile, rename } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import type { LawyerProfile, SessionUser, Task, ToneProfile, UserMode } from "../types.js";

/** Derive the effective mode from role + stored mode preference. */
export function resolveMode(role: "lawyer" | "partner", storedMode?: UserMode): UserMode {
  if (role === "partner") return "admin";
  // Lawyers can only be full_flavour or lite — never admin.
  if (storedMode === "lite") return "lite";
  return "full_flavour";
}

/** The principal used for every request when auth is disabled (local dev). */
export const LOCAL_PARTNER: SessionUser = {
  profileId: "local-partner",
  name: "Local Partner",
  email: "local@bigmichael.dev",
  role: "partner",
  mode: "admin",
};

export const isPartner = (user: SessionUser | null): boolean => user?.role === "partner";

/** Can this principal view this matter? */
export function canViewTask(user: SessionUser | null, task: Pick<Task, "assignedLawyerIds">): boolean {
  if (!user) return false;
  if (user.role === "partner") return true;
  return !!task.assignedLawyerIds?.includes(user.profileId);
}

/** Filter a list of matters to those the principal may see. */
export function filterVisible<T extends Pick<Task, "assignedLawyerIds">>(user: SessionUser | null, tasks: T[]): T[] {
  if (user?.role === "partner") return tasks;
  if (!user) return [];
  return tasks.filter((t) => t.assignedLawyerIds?.includes(user.profileId));
}

// ─── Profile store ──────────────────────────────────────────────────────────

export class ProfileStore {
  private readonly path = Config.persistence.profilesFile;
  private profiles: LawyerProfile[] = [];

  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      this.profiles = (JSON.parse(raw) as LawyerProfile[]).map((p) => ({ ...p, createdAt: new Date(p.createdAt) }));
      logger.info("Lawyer profiles loaded", { count: this.profiles.length });
    } catch {
      this.profiles = [];
    }
    // When auth is off, ensure the local partner exists so the UI has an identity.
    if (!Config.auth.enabled && !this.profiles.some((p) => p.id === LOCAL_PARTNER.profileId)) {
      this.profiles.unshift({
        id: LOCAL_PARTNER.profileId, name: LOCAL_PARTNER.name, email: LOCAL_PARTNER.email,
        role: "partner", title: "Local development", color: "#E6B450", createdAt: new Date(),
      });
      await this.persist();
    }
  }

  list(): LawyerProfile[] { return [...this.profiles]; }
  get(id: string): LawyerProfile | undefined { return this.profiles.find((p) => p.id === id); }
  getByEmail(email: string): LawyerProfile | undefined {
    return this.profiles.find((p) => p.email.toLowerCase() === email.toLowerCase());
  }

  async create(input: {
    name: string; email: string; role?: string; title?: string; color?: string;
    practiceAreas?: string[]; bio?: string; mode?: string;
  }): Promise<LawyerProfile> {
    const name = (input.name || "").trim().slice(0, 200);
    const email = (input.email || "").trim().slice(0, 254);
    if (!name || !email) throw new Error("name and email are required");
    if (this.getByEmail(email)) throw new Error(`A profile with email ${email} already exists`);
    const role: "lawyer" | "partner" = input.role === "partner" ? "partner" : "lawyer";
    const profile: LawyerProfile = {
      id: randomUUID(),
      name,
      email,
      role,
      // Partners are always admin; lawyers default to full_flavour unless explicitly set to lite.
      mode: resolveMode(role, input.mode === "lite" ? "lite" : undefined),
      title: input.title?.trim().slice(0, 200) || undefined,
      color: (input.color || pickColor(name)).slice(0, 50),
      practiceAreas: Array.isArray(input.practiceAreas)
        ? input.practiceAreas.slice(0, 20).map((a) => String(a).trim().slice(0, 100)).filter(Boolean)
        : [],
      bio: input.bio?.trim().slice(0, 2000) || undefined,
      createdAt: new Date(),
    };
    this.profiles.push(profile);
    await this.persist();
    return profile;
  }

  /**
   * Update a profile. Mode may only be changed by a partner (enforced at the
   * API layer); the store re-resolves mode on every update to ensure partners
   * can never be demoted from admin regardless of what patch.mode contains.
   */
  async update(id: string, patch: Partial<Pick<LawyerProfile, "name" | "title" | "color" | "role" | "practiceAreas" | "bio" | "mode">>): Promise<LawyerProfile> {
    const p = this.get(id);
    if (!p) throw new Error("Profile not found");
    if (typeof patch.name === "string" && patch.name.trim()) p.name = patch.name.trim().slice(0, 200);
    if (typeof patch.title === "string") p.title = patch.title.trim().slice(0, 200) || undefined;
    if (typeof patch.color === "string") p.color = patch.color.slice(0, 50);
    if (patch.role === "partner" || patch.role === "lawyer") p.role = patch.role;
    if (Array.isArray(patch.practiceAreas)) p.practiceAreas = patch.practiceAreas.slice(0, 20).map((a) => String(a).trim().slice(0, 100)).filter(Boolean);
    if (typeof patch.bio === "string") p.bio = patch.bio.trim().slice(0, 2000) || undefined;
    // Mode: always re-resolve so partners can't be demoted from admin by a patch.
    if (patch.mode !== undefined) {
      p.mode = resolveMode(p.role, patch.mode === "lite" ? "lite" : "full_flavour");
    } else {
      // Keep in sync if role changed.
      p.mode = resolveMode(p.role, p.mode);
    }
    await this.persist();
    return p;
  }

  async updateTone(id: string, tone: ToneProfile): Promise<LawyerProfile> {
    const p = this.get(id);
    if (!p) throw new Error("Profile not found");
    p.toneProfile = tone;
    await this.persist();
    return p;
  }

  async clearTone(id: string): Promise<LawyerProfile> {
    const p = this.get(id);
    if (!p) throw new Error("Profile not found");
    delete p.toneProfile;
    await this.persist();
    return p;
  }

  async remove(id: string): Promise<boolean> {
    if (id === LOCAL_PARTNER.profileId) throw new Error("Cannot delete the local development profile");
    const before = this.profiles.length;
    this.profiles = this.profiles.filter((p) => p.id !== id);
    if (this.profiles.length === before) return false;
    await this.persist();
    return true;
  }

  private async persist(): Promise<void> {
    // Write to a temp file then rename so a crash mid-write never leaves a
    // partially-written profiles file (rename is atomic on POSIX filesystems).
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(this.profiles, null, 2), "utf8");
    await rename(tmp, this.path);
  }
}

const PALETTE = ["#E6B450", "#84A9CC", "#7FB069", "#DA6A60", "#E0913C", "#B08BD6", "#5FB0B7"];
function pickColor(seed: string): string {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  return PALETTE[h % PALETTE.length];
}
