// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { QdrantClient } from "@qdrant/js-client-rest";
import { v4 as uuidv4 } from "uuid";
import { Config } from "../config.js";
import { embed, embedBatch } from "../embeddings.js";
import { logger } from "../logger.js";
import type { Document, SearchResult } from "../types.js";

const COLLECTION = Config.vectorDb.collections.documents;
const DIMS = Config.embeddings.dimensions;
const CHUNK_SIZE = 1500;    // characters per chunk
const CHUNK_OVERLAP = 200;

export class KnowledgeStore {
  private readonly qdrant: QdrantClient;
  private ready = false;

  constructor() {
    this.qdrant = new QdrantClient({
      url: Config.vectorDb.url,
      apiKey: Config.vectorDb.apiKey,
    });
  }

  async init(): Promise<void> {
    const { collections } = await this.qdrant.getCollections();
    if (!collections.some((c) => c.name === COLLECTION)) {
      await this.qdrant.createCollection(COLLECTION, {
        vectors: { size: DIMS, distance: "Cosine" },
      });
      logger.info("Knowledge store collection created", { collection: COLLECTION });
    }
    this.ready = true;
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

    // Embed all chunks in a single batched API call instead of N concurrent
    // calls — prevents rate-limit exhaustion and unbounded parallel requests.
    const embeddings = await embedBatch(chunks);

    const points = chunks.map((chunk, i) => ({
      id: uuidv4(),
      vector: embeddings[i].embedding,
      payload: {
        docId,
        title: doc.title,
        source: doc.source ?? null,
        jurisdiction: doc.jurisdiction ?? null,
        documentType: doc.documentType ?? null,
        ownerId: doc.ownerId ?? null,
        practiceArea: doc.practiceArea ?? null,
        detectedClientNumber: doc.detectedClientNumber ?? null,
        chunkIndex: i,
        totalChunks: chunks.length,
        content: chunk,
        ingestedAt: new Date().toISOString(),
      },
    }));

    await this.qdrant.upsert(COLLECTION, { wait: true, points });
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

    const must: unknown[] = [];
    if (opts.jurisdiction) must.push({ key: "jurisdiction", match: { value: opts.jurisdiction } });
    if (opts.documentType) must.push({ key: "documentType", match: { value: opts.documentType } });
    if (opts.ownerId) must.push({ key: "ownerId", match: { value: opts.ownerId } });

    const results = await this.qdrant.search(COLLECTION, {
      vector: embedding,
      limit: opts.topK ?? 8,
      filter: must.length ? { must } : undefined,
      with_payload: true,
    });

    return results.map((r) => {
      const p = r.payload as Record<string, unknown>;
      return {
        document: {
          id: p.docId as string,
          title: p.title as string,
          content: p.content as string,
          source: p.source as string | undefined,
          jurisdiction: p.jurisdiction as string | undefined,
          documentType: p.documentType as string | undefined,
          practiceArea: p.practiceArea as string | undefined,
          detectedClientNumber: p.detectedClientNumber as string | undefined,
          ingestedAt: new Date(p.ingestedAt as string),
        },
        score: r.score,
        excerpt: (p.content as string).slice(0, 300) + "…",
      };
    });
  }

  /**
   * Retrieve full document text by docId — concatenates all chunks in order.
   * When ownerId is provided, returns null if the document belongs to a different owner
   * so agent tools cannot read documents outside the submitting user's scope.
   */
  async getFullText(docId: string, ownerId?: string): Promise<string | null> {
    this.assertReady();
    const must: unknown[] = [{ key: "docId", match: { value: docId } }];
    if (ownerId) must.push({ key: "ownerId", match: { value: ownerId } });
    const result = await this.qdrant.scroll(COLLECTION, {
      filter: { must },
      limit: 500,
      with_payload: true,
    });

    if (!result.points.length) return null;

    const sorted = result.points.sort(
      (a, b) =>
        ((a.payload as Record<string, unknown>).chunkIndex as number) -
        ((b.payload as Record<string, unknown>).chunkIndex as number),
    );

    return sorted.map((p) => (p.payload as Record<string, unknown>).content as string).join("\n");
  }

  /** List every ingested document (one entry per docId), for pickers/browsing. */
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
    const seen = new Map<string, {
      id: string; title: string; jurisdiction?: string; documentType?: string;
      practiceArea?: string; detectedClientNumber?: string; ingestedAt?: string;
    }>();
    let offset: string | number | undefined | null = undefined;
    do {
      const res = await this.qdrant.scroll(COLLECTION, {
        limit: 256,
        with_payload: true,
        offset: offset ?? undefined,
        filter: ownerId ? { must: [{ key: "ownerId", match: { value: ownerId } }] } : undefined,
      });
      for (const pt of res.points) {
        const p = pt.payload as Record<string, unknown>;
        const id = p?.docId as string | undefined;
        if (id && !seen.has(id)) {
          seen.set(id, {
            id,
            title: (p.title as string) ?? "Untitled",
            jurisdiction: (p.jurisdiction as string) ?? undefined,
            documentType: (p.documentType as string) ?? undefined,
            practiceArea: (p.practiceArea as string) ?? undefined,
            detectedClientNumber: (p.detectedClientNumber as string) ?? undefined,
            ingestedAt: (p.ingestedAt as string) ?? undefined,
          });
        }
      }
      offset = res.next_page_offset as string | number | null | undefined;
    } while (offset != null);
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