// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package citations

import (
	"context"
	"encoding/json"
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

// A failed or unusable treatment analysis is a model failure (typed HTTP
// error upstream), never a guessed or "unknown" verdict dressed as a result.
func TestCitationModelFailureIsError(t *testing.T) {
	withCourtListener(t, clFound)
	for name, p := range map[string]providers.Provider{
		"call fails":     replyProvider{err: errors.New("ollama: HTTP 500")},
		"garbage":        replyProvider{text: "no json"},
		"invalid signal": replyProvider{text: `{"signal":"purple","status":"good_law","confidence":0.9,"positiveTreatmentCount":3}`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(p, "light").Check(context.Background(), "410 U.S. 113", "")
			if !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want ErrModelUnavailable, got %v", err)
			}
		})
	}
	_, err := New(replyProvider{err: providers.ErrModelTimeout}, "light").Check(context.Background(), "410 U.S. 113", "")
	if !errors.Is(err, providers.ErrModelTimeout) {
		t.Fatalf("timeout classification lost: %v", err)
	}
}

func TestCitationGoodAnalysisKept(t *testing.T) {
	withCourtListener(t, clFound)
	res, err := New(replyProvider{text: "```json\n" + `{"signal":"Green","status":"Good_Law","signalLabel":"Good law","confidence":0.8,"reasoning":"Followed."}` + "\n```"}, "light").Check(context.Background(), "410 U.S. 113", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Signal != types.CitationSignalGreen || res.Status != "good_law" || res.Reasoning != "Followed." {
		t.Fatalf("unexpected %+v", res)
	}
	// Never null on the wire: the UI reads .length.
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"topNegativeTreatments":[]`) {
		t.Fatalf("treatments must serialise as [], got %s", b)
	}
}

// Any CourtListener failure — search, opinion or citing-opinions lookup — is
// "not checked", never "not found" and never a verdict on missing evidence.
func TestCitationLookupFailureIsUnknown(t *testing.T) {
	for name, failing := range map[string]func(*http.Request) bool{
		"search": func(r *http.Request) bool {
			return !strings.Contains(r.URL.Path, "/clusters/") && !strings.Contains(r.URL.RawQuery, "cited_gt")
		},
		"cluster": func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/clusters/") },
		"citing":  func(r *http.Request) bool { return strings.Contains(r.URL.RawQuery, "cited_gt") },
	} {
		t.Run(name, func(t *testing.T) {
			withCourtListener(t, func(w http.ResponseWriter, r *http.Request) {
				if failing(r) {
					http.Error(w, "down", http.StatusBadGateway)
					return
				}
				clFound(w, r)
			})
			green := replyProvider{text: `{"signal":"green","status":"good_law","confidence":0.9}`}
			res, err := New(green, "light").Check(context.Background(), "410 U.S. 113", "")
			if err != nil {
				t.Fatal(err)
			}
			if res.Signal != types.CitationSignalUnknown || !strings.Contains(res.Reasoning, "could not be reached") {
				t.Fatalf("unexpected %+v", res)
			}
		})
	}
}
