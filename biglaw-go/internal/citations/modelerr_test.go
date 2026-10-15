// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package citations

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type replyProvider struct {
	text string
	err  error
}

func (p replyProvider) Chat(providers.ChatParams) (*providers.ChatResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &providers.ChatResponse{Content: []providers.ContentBlock{{Type: providers.BlockText, Text: p.text}}}, nil
}

func withCourtListener(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	orig := clBase
	clBase = srv.URL
	t.Cleanup(func() { clBase = orig })
}

func clFound(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "/clusters/"):
		_, _ = w.Write([]byte(`{"case_name":"Roe v. Wade","citation":["410 U.S. 113"],"date_filed":"1973-01-22","court":"scotus"}`))
	case strings.Contains(r.URL.RawQuery, "cited_gt"):
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	default:
		_, _ = w.Write([]byte(`{"results":[{"cluster_id":1,"caseName":"Roe v. Wade","citation":["410 U.S. 113"]}]}`))
	}
}

// A failed or unusable treatment analysis is an explicit UNKNOWN (unchecked),
// never a Caution/yellow signal.
func TestCitationModelFailureIsUnknown(t *testing.T) {
	withCourtListener(t, clFound)
	for name, p := range map[string]providers.Provider{
		"call fails":     replyProvider{err: errors.New("ollama: HTTP 500")},
		"garbage":        replyProvider{text: "no json"},
		"invalid signal": replyProvider{text: `{"signal":"purple","status":"good_law","confidence":0.9}`},
	} {
		t.Run(name, func(t *testing.T) {
			res := New(p, "light").Check("410 U.S. 113", "")
			if res.Signal != types.CitationSignalUnknown || res.Status != types.CitationStatusUnknown || res.Confidence != 0 {
				t.Fatalf("want unknown/0, got signal=%s status=%s conf=%v", res.Signal, res.Status, res.Confidence)
			}
			if !strings.Contains(res.Reasoning, "NOT been checked") {
				t.Errorf("reasoning does not say unchecked: %q", res.Reasoning)
			}
		})
	}
}

func TestCitationGoodAnalysisKept(t *testing.T) {
	withCourtListener(t, clFound)
	res := New(replyProvider{text: `{"signal":"green","status":"good_law","signalLabel":"Good law","confidence":0.8,"reasoning":"Followed."}`}, "light").Check("410 U.S. 113", "")
	if res.Signal != types.CitationSignalGreen || res.Status != "good_law" || res.Reasoning != "Followed." {
		t.Fatalf("unexpected %+v", res)
	}
}

// CourtListener being unreachable is "not checked", not "not found".
func TestCitationLookupFailureIsUnknownNotFound(t *testing.T) {
	withCourtListener(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) })
	res := New(replyProvider{text: "{}"}, "light").Check("410 U.S. 113", "")
	if res.Signal != types.CitationSignalUnknown || !strings.Contains(res.Reasoning, "could not be reached") {
		t.Fatalf("unexpected %+v", res)
	}
}
