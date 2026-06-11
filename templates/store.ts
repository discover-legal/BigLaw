// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { readdir, readFile } from "fs/promises";
import { join, extname, dirname } from "path";
import { fileURLToPath } from "url";
import { logger } from "../logger.js";
import type { TaskTemplate } from "../adapters/lavern.js";

const DEFAULT_TEMPLATE_DIR = join(dirname(fileURLToPath(import.meta.url)));

export class TemplateStore {
  private readonly templates: Map<string, TaskTemplate> = new Map();

  async load(dir: string = DEFAULT_TEMPLATE_DIR): Promise<void> {
    let entries: string[];
    try {
      entries = await readdir(dir);
    } catch {
      logger.warn("Template directory not readable — skipping", { dir });
      return;
    }

    let loaded = 0;
    for (const entry of entries) {
      if (extname(entry) !== ".json") continue;
      try {
        const raw = await readFile(join(dir, entry), "utf8");
        const parsed = JSON.parse(raw) as TaskTemplate | TaskTemplate[];
        const items = Array.isArray(parsed) ? parsed : [parsed];
        for (const t of items) {
          this.templates.set(t.id, t);
          loaded++;
        }
      } catch (err) {
        logger.warn("Failed to parse template file", { file: entry, error: (err as Error).message });
      }
    }

    logger.info("Templates loaded", { count: loaded, dir });
  }

  get(id: string): TaskTemplate | null {
    return this.templates.get(id) ?? null;
  }

  list(): TaskTemplate[] {
    return Array.from(this.templates.values());
  }

  add(template: TaskTemplate): void {
    this.templates.set(template.id, template);
  }
}
