// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// Shared RuVector (native in-process HNSW) type interfaces and helpers.
// Consumed by AgentRegistry, InterRoundMemoryStore, and KnowledgeStore so
// the interface definitions are defined once rather than three times.

import { Config } from "./config.js";

// eslint-disable-next-line @typescript-eslint/no-require-imports
export const { VectorDb } = require("ruvector") as { VectorDb: new (o: RvOptions) => RvDb };

export interface RvOptions { dimensions: number; storagePath?: string; distanceMetric?: string }
export interface RvEntry  { id?: string; vector: number[] | Float32Array; metadata?: Record<string, unknown> }
export interface RvHit    { id: string; score: number; metadata?: Record<string, unknown> }
export interface RvRecord { id?: string; vector: Float32Array; metadata?: Record<string, unknown> }

// Full interface — superset of what each individual store uses, so all three
// stores can type their `db` field against this single definition.
export interface RvDb {
  insert(e: RvEntry): Promise<string>;
  insertBatch(es: RvEntry[]): Promise<string[]>;
  search(q: { vector: number[] | Float32Array; k: number; filter?: Record<string, unknown> }): Promise<RvHit[]>;
  get(id: string): Promise<RvRecord | null>;
  delete(id: string): Promise<boolean>;
  len(): Promise<number>;
}

// Small non-zero vector for "list all / filter-only" queries — avoids the
// cosine-of-zero edge case in the native HNSW implementation.
export const SMALL_VEC: number[] = new Array(Config.embeddings.dimensions).fill(Number.EPSILON);
