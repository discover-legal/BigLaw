// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

// MockSearchProvider serves canned web-search results for tests.
type MockSearchProvider struct {
	Canned []map[string]any
}

// NewMockSearchProvider returns a provider with one canned result by default.
func NewMockSearchProvider() *MockSearchProvider {
	return &MockSearchProvider{Canned: []map[string]any{
		{"title": "doc", "snippet": "fact", "url": "https://example/"},
	}}
}

// Search implements SearchProvider.
func (m *MockSearchProvider) Search(query string, k int) []map[string]any {
	if k > len(m.Canned) {
		k = len(m.Canned)
	}
	return m.Canned[:k]
}
