// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Extra document tools ported from src/tools/documents.ts — fetch_documents
// reads the full text of multiple knowledge-store documents in one call
// (capped at 20 IDs, 200k chars per document) so agents avoid repeated
// read_document round-trips.

package tools

import (
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

const (
	maxFetchDocs   = 20
	maxFetchDocLen = 200_000
)

func (r *Registry) registerDocumentExtraTools() {
	r.Register(r.fetchDocumentsTool())
}

// ─── fetch_documents ──────────────────────────────────────────────────────────

func (r *Registry) fetchDocumentsTool() *ToolImpl {
	return &ToolImpl{
		Name: "fetch_documents",
		Schema: providers.ToolParam{
			Name: "fetch_documents",
			Description: "Read the full text of multiple documents in a single call. Use this instead of calling " +
				"read_document repeatedly when you need several documents at once.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"doc_ids": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Array of document IDs to read (capped at 20 per call)",
					},
				},
				"required": []string{"doc_ids"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			docIDs := strSlice(input["doc_ids"])
			if len(docIDs) > maxFetchDocs {
				docIDs = docIDs[:maxFetchDocs]
			}
			documents := make([]map[string]interface{}, 0, len(docIDs))
			for _, docID := range docIDs {
				if ctx.KnowledgeStore == nil {
					documents = append(documents, map[string]interface{}{
						"doc_id": docID, "ok": false, "error": "Not found",
					})
					continue
				}
				text, err := ctx.KnowledgeStore.GetFullText(docID)
				if err != nil || text == "" {
					documents = append(documents, map[string]interface{}{
						"doc_id": docID, "ok": false, "error": "Not found",
					})
					continue
				}
				truncated := len(text) > maxFetchDocLen
				content := text
				if truncated {
					content = truncateUTF8(text, maxFetchDocLen)
				}
				documents = append(documents, map[string]interface{}{
					"doc_id":    docID,
					"ok":        true,
					"length":    len(text),
					"truncated": truncated,
					"content":   content,
				})
			}
			return map[string]interface{}{"count": len(documents), "documents": documents}, nil
		},
	}
}
