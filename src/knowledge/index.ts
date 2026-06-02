// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// Knowledge store backed by RuVector's native in-process HNSW store.
// Documents are chunked and each chunk stored as a separate vector.
// No external service required — data persists to ./data/knowledge.rvdb.

import { v4 as uuidv4 } from "uuid";
import { Config } from "../config.js";
import { embed, embedBatch } from "../embeddings.js";
import { logger } from "../logger.js";
import type { Document, SearchResult } from "../types.js";

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { VectorDb } = require("ruvector") as { VectorDb: new (o: RvOptions) => RvDb };

interface RvOptions { dimensions: number; storagePath?: string; distanceMetric?: string }
interface RvEntry  { id?: string; vector: number[] | Float32Array; metadata?: Record<string, unknown> }
interface RvHit    { id: string; score: number; metadata?: Record<string, unknown> }
interface RvDb {
  insert(e: RvEntry): Promise<string>;
  insertBatch(es: RvEntry[]): Promise<string[]>;
  search(q: { vector: number[] | Float32Array; k: number; filter?: Record<string, unknown> }): Promise<RvHit[]>;
  delete(id: string): Promise<boolean>;
}

const DIMS = Config.embeddings.dimensions;
const CHUNK_SIZE = 1500;    // characters per chunk
const CHUNK_OVERLAP = 200;

// Small non-zero vector used for "fetch by filter" queries without a semantic query.
const SMALL_VEC: number[] = new Array(DIMS).fill(Number.EPSILON);

// Upper bound on chunks to retrieve when listing/fetching full docs.
// 10 000 chunks ≈ 150 MB of raw text — well beyond a typical law firm knowledge base.
const MAX_CHUNKS_FETCH = 10_000;

export class KnowledgeStore {
  private readonly db: RvDb;
  private ready = false;

  constructor() {
    this.db = new VectorDb({
      dimensions: DIMS,
      distanceMetric: "Cosine",
      storagePath: `${Config.vectorDb.dataDir}/knowledge.rvdb`,
    });
  }

  async init(): Promise<void> {
    this.ready = true;
    logger.info("Knowledge store ready (RuVector native)");
  }

  // 2 MB cap — prevents a single ingest from fanning out thousands of
  // concurrent embedding API calls and exhausting rate limits / memory.
  private static readonly MAX_CONTENT_CHARS = 2 * 1024 * 1024;

  /**
   * Ingest a document — chunks it and stores each chunk with its embedding.
   * Returns the document ID.
   */
  async ingest(doc: Omit<Document, "id" | "ingestedAt">): Promise<string> {
    this.assertReady();
    if (doc.content.length > KnowledgeStore.MAX_CONTENT_CHARS) {
      throw new Error(
        `Document content exceeds the ${KnowledgeStore.MAX_CONTENT_CHARS / 1024 / 1024} MB limit ` +
        `(${Math.round(doc.content.length / 1024)} KB received). Split into smaller documents.`,
      );
    }
    const docId = uuidv4();
    const chunks = chunkText(doc.content, CHUNK_SIZE, CHUNK_OVERLAP);

    logger.info("Ingesting document", { title: doc.title, chunks: chunks.length });

    const embeddings = await embedBatch(chunks);

    const ingestedAt = new Date().toISOString();
    await this.db.insertBatch(
      chunks.map((chunk, i) => ({
        id: uuidv4(),
        vector: embeddings[i].embedding,
        metadata: {
          docId,
          title:                 doc.title,
          source:                doc.source ?? null,
          jurisdiction:          doc.jurisdiction ?? null,
          documentType:          doc.documentType ?? null,
          ownerId:               doc.ownerId ?? null,
          practiceArea:          doc.practiceArea ?? null,
          detectedClientNumber:  doc.detectedClientNumber ?? null,
          chunkIndex:            i,
          totalChunks:           chunks.length,
          content:               chunk,
          ingestedAt,
        },
      })),
    );

    logger.info("Document ingested", { docId, chunks: chunks.length });
    return docId;
  }

  /**
   * Semantic search across all ingested documents.
   */
  async search(
    query: string,
    opts: { topK?: number; jurisdiction?: string; documentType?: string; ownerId?: string } = {},
  ): Promise<SearchResult[]> {
    this.assertReady();
    const { embedding } = await embed(query);

    const filter: Record<string, unknown> = {};
    if (opts.jurisdiction)  filter.jurisdiction  = opts.jurisdiction;
    if (opts.documentType)  filter.documentType  = opts.documentType;
    if (opts.ownerId)       filter.ownerId       = opts.ownerId;

    const results = await this.db.search({
      vector: embedding,
      k: Math.min(opts.topK ?? 8, 50),
      ...(Object.keys(filter).length ? { filter } : {}),
    });

    return results.map((r) => {
      const p = r.metadata as Record<string, unknown>;
      return {
        document: {
          id:                   p.docId as string,
          title:                p.title as string,
          content:              p.content as string,
          source:               (p.source as string) ?? undefined,
          jurisdiction:         (p.jurisdiction as string) ?? undefined,
          documentType:         (p.documentType as string) ?? undefined,
          practiceArea:         (p.practiceArea as string) ?? undefined,
          detectedClientNumber: (p.detectedClientNumber as string) ?? undefined,
          ingestedAt:           new Date(p.ingestedAt as string),
        },
        score:   r.score,
        excerpt: ((p.content as string) ?? "").slice(0, 300) + "…",
      };
    });
  }

  /**
   * Retrieve full document text by docId — concatenates all chunks in order.
   * When ownerId is provided, returns null if the document belongs to a different owner.
   */
  async getFullText(docId: string, ownerId?: string): Promise<string | null> {
    this.assertReady();
    const filter: Record<string, unknown> = { docId };
    if (ownerId) filter.ownerId = ownerId;

    const results = await this.db.search({ vector: SMALL_VEC, k: MAX_CHUNKS_FETCH, filter });
    if (!results.length) return null;

    const sorted = results
      .slice()
      .sort(
        (a, b) =>
          ((a.metadata?.chunkIndex as number) ?? 0) -
          ((b.metadata?.chunkIndex as number) ?? 0),
      );

    return sorted.map((r) => (r.metadata?.content as string) ?? "").join("\n");
  }

  /** List every ingested document (one entry per docId). */
  async listDocuments(ownerId?: string): Promise<Array<{
    id: string;
    title: string;
    jurisdiction?: string;
    documentType?: string;
    practiceArea?: string;
    detectedClientNumber?: string;
    ingestedAt?: string;
  }>> {
    this.assertReady();
    const filter: Record<string, unknown> = {};
    if (ownerId) filter.ownerId = ownerId;

    const results = await this.db.search({
      vector: SMALL_VEC,
      k: MAX_CHUNKS_FETCH,
      ...(Object.keys(filter).length ? { filter } : {}),
    });

    const seen = new Map<string, {
      id: string; title: string; jurisdiction?: string; documentType?: string;
      practiceArea?: string; detectedClientNumber?: string; ingestedAt?: string;
    }>();

    for (const r of results) {
      const p = r.metadata as Record<string, unknown>;
      const id = p?.docId as string | undefined;
      if (id && !seen.has(id)) {
        seen.set(id, {
          id,
          title:                (p.title as string) ?? "Untitled",
          jurisdiction:         (p.jurisdiction as string) ?? undefined,
          documentType:         (p.documentType as string) ?? undefined,
          practiceArea:         (p.practiceArea as string) ?? undefined,
          detectedClientNumber: (p.detectedClientNumber as string) ?? undefined,
          ingestedAt:           (p.ingestedAt as string) ?? undefined,
        });
      }
    }

    return [...seen.values()];
  }

  private assertReady(): void {
    if (!this.ready) throw new Error("KnowledgeStore not initialised — call init() first");
  }
}

// ─── Text chunking ────────────────────────────────────────────────────────────

function chunkText(text: string, chunkSize: number, overlap: number): string[] {
  const chunks: string[] = [];
  let start = 0;
  while (start < text.length) {
    const end = Math.min(start + chunkSize, text.length);
    chunks.push(text.slice(start, end));
    if (end === text.length) break;
    start += chunkSize - overlap;
  }
  return chunks;
}
