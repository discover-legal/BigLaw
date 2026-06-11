// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Embedding generation — OpenAI text-embedding-3-small or Ollama local model.
// Cosine similarity for Need/Offer matching and semantic search.

package embeddings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
)

type EmbeddingResult struct {
	Text      string
	Embedding []float32
	Model     string
}

type Client struct {
	cfg    *config.Config
	client *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Embed(text string) (*EmbeddingResult, error) {
	results, err := c.EmbedBatch([]string{text})
	if err != nil || len(results) == 0 {
		return nil, err
	}
	return &results[0], nil
}

func (c *Client) EmbedBatch(texts []string) ([]EmbeddingResult, error) {
	if c.cfg.Local.LocalEmbeddings {
		return c.embedOllama(texts)
	}
	return c.embedOpenAI(texts)
}

// ─── OpenAI (text-embedding-3-small) ─────────────────────────────────────────

type openAIEmbedRequest struct {
	Input      interface{} `json:"input"`
	Model      string      `json:"model"`
	Dimensions int         `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (c *Client) embedOpenAI(texts []string) ([]EmbeddingResult, error) {
	req := openAIEmbedRequest{
		Input:      texts,
		Model:      c.cfg.Embeddings.Model,
		Dimensions: c.cfg.Embeddings.Dimensions,
	}
	body, _ := json.Marshal(req)
	httpReq, _ := http.NewRequest("POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.Embeddings.APIKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai embed HTTP %d: %s", resp.StatusCode, string(b))
	}
	var r openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("openai embed decode: %w", err)
	}
	// Do not assume the API returns embeddings in request order or in full:
	// place each result by its `index` field and validate the count. A short
	// or reordered response would otherwise silently pair texts with the
	// wrong (or zero-valued) vectors and corrupt the comm graph downstream.
	if len(r.Data) != len(texts) {
		return nil, fmt.Errorf("openai embed count mismatch: got %d, expected %d", len(r.Data), len(texts))
	}
	results := make([]EmbeddingResult, len(texts))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai embed index out of range: %d", d.Index)
		}
		results[d.Index] = EmbeddingResult{
			Text:      texts[d.Index],
			Embedding: d.Embedding,
			Model:     c.cfg.Embeddings.Model,
		}
	}
	return results, nil
}

// ─── Ollama embeddings ────────────────────────────────────────────────────────

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (c *Client) embedOllama(texts []string) ([]EmbeddingResult, error) {
	model := c.cfg.Local.LocalEmbeddingModel
	baseURL := c.cfg.Local.OllamaURL
	results := make([]EmbeddingResult, len(texts))
	for i, text := range texts {
		reqBody, _ := json.Marshal(ollamaEmbedRequest{Model: model, Prompt: text})
		resp, err := c.client.Post(baseURL+"/api/embeddings", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("ollama embed: %w", err)
		}
		var r ollamaEmbedResponse
		json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		results[i] = EmbeddingResult{Text: text, Embedding: r.Embedding, Model: model}
	}
	return results, nil
}

// ─── Cosine similarity ────────────────────────────────────────────────────────

func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
