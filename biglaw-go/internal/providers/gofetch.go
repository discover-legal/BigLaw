// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// GoFetch memory-plane integration. GoFetch (github.com/hordruma/gofetch) is
// the mechanical VRAM/RAM model-swap daemon; BigLaw is its policy brain. This
// provider wraps the local OpenAI-compatible provider and, before every chat
// call, asks GoFetch to make the target model VRAM-resident so the swap cost
// lands before inference instead of inside it.

package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/routing"
)

// GoFetchProvider decorates a local provider with GoFetch residency control.
// Residency failures are fail-open: GoFetch's llama-swap engine swaps lazily on
// the first inference request anyway, and an unreachable control plane must
// never take down inference that would otherwise succeed.
type GoFetchProvider struct {
	inner   *OllamaProvider
	baseURL string
	client  *http.Client
	// lastResident dedupes consecutive residency calls for the same model so a
	// long agentic loop on one model costs one control round-trip, not N.
	mu           sync.Mutex
	lastResident string
}

// NewGoFetchProvider wraps inner with residency control against the GoFetch
// daemon at baseURL (e.g. http://localhost:8080).
func NewGoFetchProvider(inner *OllamaProvider, baseURL string) *GoFetchProvider {
	return &GoFetchProvider{
		inner:   inner,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 120 * time.Second}, // a cold swap can take a while
	}
}

// EnsureResident asks GoFetch to load model into VRAM, blocking until ready.
func (p *GoFetchProvider) EnsureResident(model string) error {
	body, _ := json.Marshal(map[string]string{"model": model})
	resp, err := p.client.Post(p.baseURL+"/v1/vram/load", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gofetch: vram/load: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("gofetch: vram/load HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// Warm asks GoFetch to hold model's weights hot in RAM (tier "" uses the
// model's configured default). Best-effort: callers may ignore the error.
func (p *GoFetchProvider) Warm(model, tier string) error {
	body, _ := json.Marshal(map[string]string{"model": model, "tier": tier})
	resp, err := p.client.Post(p.baseURL+"/v1/ram/warm", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gofetch: ram/warm: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("gofetch: ram/warm HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// Chat ensures the target model is VRAM-resident, then delegates to the
// wrapped local provider (whose base URL is typically GoFetch's passthrough).
func (p *GoFetchProvider) Chat(params ChatParams) (*ChatResponse, error) {
	model := routing.ResolveModelID(params.Model)
	p.mu.Lock()
	stale := model != p.lastResident
	p.mu.Unlock()
	if stale {
		if err := p.EnsureResident(model); err != nil {
			slog.Warn("gofetch residency request failed; proceeding (lazy swap)", "model", model, "err", err)
		} else {
			p.mu.Lock()
			p.lastResident = model
			p.mu.Unlock()
		}
	}
	return p.inner.Chat(params)
}
