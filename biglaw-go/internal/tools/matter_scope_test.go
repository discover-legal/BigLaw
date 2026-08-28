// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/rag"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// fakeKnowledge holds two matters' documents, like a real firm store.
type scopeKnowledge struct{ docs map[string]types.Document }

func (f *scopeKnowledge) Search(query, ownerID string, topK int) ([]types.SearchResult, error) {
	out := []types.SearchResult{}
	for _, d := range f.docs {
		if strings.Contains(strings.ToLower(d.Content+d.Title), strings.ToLower(query)) || query == "legal document" {
			out = append(out, types.SearchResult{Document: d, Score: 0.9})
		}
	}
	return out, nil
}
func (f *scopeKnowledge) GetFullText(docID string) (string, error) {
	if d, ok := f.docs[docID]; ok {
		return d.Content, nil
	}
	return "", fmt.Errorf("not found")
}
func (f *scopeKnowledge) GetByID(docID string) *types.Document {
	if d, ok := f.docs[docID]; ok {
		return &d
	}
	return nil
}

// scopeHarness builds a registry whose RAG store and knowledge store hold TWO
// matters: the Tremblay family file and the Okafor employment file — the exact
// shape that leaked in production.
func scopeHarness(t *testing.T) (*Registry, agents.ToolContext) {
	t.Helper()
	svc := rag.New(rag.NewMemStore(), nil, nil) // BM25-only: deterministic, no embedder
	svc.IngestDoc("doc-trem", "Tremblay Disclosure", "The matrimonial home equalization payment for Tremblay separation. Spousal support and parenting schedule for the children.")
	svc.IngestDoc("doc-okaf", "Okafor Agreement", "The Okafor employment agreement: commissions, non-compete covenant, and overtime classification for the employee.")
	cfg := &config.Config{}
	r := NewRegistry(cfg, nil, nil, svc, nil)
	ks := &scopeKnowledge{docs: map[string]types.Document{
		"doc-trem": {ID: "doc-trem", Title: "Tremblay Disclosure", Content: "Tremblay equalization and support figures."},
		"doc-okaf": {ID: "doc-okaf", Title: "Okafor Agreement", Content: "Okafor commissions and covenant terms."},
	}}
	ctx := agents.ToolContext{KnowledgeStore: ks, TaskID: "task-okafor", DocumentIDs: []string{"doc-okaf"}}
	return r, ctx
}

// TestMatterScopeSearchChunks pins the confidentiality contract: a task scoped
// to the Okafor matter must never retrieve Tremblay chunks, even on a query
// that matches the Tremblay document best.
func TestMatterScopeSearchChunks(t *testing.T) {
	r, ctx := scopeHarness(t)
	res, err := r.Execute("search_chunks", map[string]interface{}{"query": "equalization spousal support parenting Tremblay", "top_k": 8}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.(map[string]interface{})["results"].([]map[string]interface{}) {
		if strings.Contains(fmt.Sprint(m["title"]), "Tremblay") {
			t.Fatalf("cross-matter leak: Tremblay chunk surfaced in Okafor task: %v", m)
		}
	}
	// The same query WITHOUT scope does surface Tremblay — proving the filter
	// (not the ranking) is what protects the matter.
	res, _ = r.Execute("search_chunks", map[string]interface{}{"query": "equalization spousal support parenting Tremblay", "top_k": 8}, agents.ToolContext{})
	found := false
	for _, m := range res.(map[string]interface{})["results"].([]map[string]interface{}) {
		if strings.Contains(fmt.Sprint(m["title"]), "Tremblay") {
			found = true
		}
	}
	if !found {
		t.Fatal("harness broken: unscoped search should find Tremblay chunks")
	}
}

func TestMatterScopeDocAddressedTools(t *testing.T) {
	r, ctx := scopeHarness(t)
	// read_document on the other matter's doc must refuse.
	res, err := r.Execute("read_document", map[string]interface{}{"doc_id": "doc-trem"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]interface{})
	if m["text"] != "" || m["error"] == nil {
		t.Fatalf("read_document must refuse out-of-scope doc, got %v", m)
	}
	// In-scope doc still reads.
	res, _ = r.Execute("read_document", map[string]interface{}{"doc_id": "doc-okaf"}, ctx)
	if !strings.Contains(fmt.Sprint(res.(map[string]interface{})["text"]), "Okafor") {
		t.Fatal("in-scope read_document must work")
	}
	// get_outline / read_section refuse out-of-scope.
	res, _ = r.Execute("get_outline", map[string]interface{}{"doc_id": "doc-trem"}, ctx)
	if res.(map[string]interface{})["error"] == nil {
		t.Fatal("get_outline must refuse out-of-scope doc")
	}
	res, _ = r.Execute("find_in_document", map[string]interface{}{"doc_id": "doc-trem", "query": "support"}, ctx)
	if res.(map[string]interface{})["error"] == nil {
		t.Fatal("find_in_document must refuse out-of-scope doc")
	}
}

func TestMatterScopeKnowledgeTools(t *testing.T) {
	r, ctx := scopeHarness(t)
	res, err := r.Execute("search_knowledge", map[string]interface{}{"query": "Tremblay equalization"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.(map[string]interface{})["results"].([]map[string]interface{}) {
		if strings.Contains(fmt.Sprint(m["title"]), "Tremblay") {
			t.Fatalf("search_knowledge leaked Tremblay into Okafor task: %v", m)
		}
	}
	res, _ = r.Execute("list_documents", map[string]interface{}{}, ctx)
	for _, m := range res.(map[string]interface{})["documents"].([]map[string]interface{}) {
		if strings.Contains(fmt.Sprint(m["title"]), "Tremblay") {
			t.Fatalf("list_documents leaked Tremblay into Okafor task: %v", m)
		}
	}
}
