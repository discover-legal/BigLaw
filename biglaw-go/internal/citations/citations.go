// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// CitationEngine — "Good Law?" signal for any case citation.
// Direct replacement for Westlaw KeyCite and LexisNexis Shepard's.
// Uses CourtListener's free public API + Haiku AI synthesis.

package citations

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	clTimeout  = 20 * time.Second
	clMaxBytes = 512 * 1024
)

var clBase = func() string {
	b := os.Getenv("COURT_LISTENER_BASE_URL")
	if b == "" {
		b = "https://www.courtlistener.com"
	}
	return strings.TrimRight(b, "/")
}()

// ─── CourtListener types ──────────────────────────────────────────────────────

type clSearchHit struct {
	ID          *int    `json:"id"`
	ClusterID   *int    `json:"cluster_id"`
	CaseName    string  `json:"case_name"`
	Citation    []string `json:"citation"`
	Court       string  `json:"court"`
	DateFiled   string  `json:"date_filed"`
	AbsoluteURL string  `json:"absolute_url"`
}

type clSearchResp struct {
	Count   int           `json:"count"`
	Results []clSearchHit `json:"results"`
}

type clCluster struct {
	ID          int      `json:"id"`
	CaseName    string   `json:"case_name"`
	Citation    []string `json:"citation"`
	DateFiled   string   `json:"date_filed"`
	Court       string   `json:"court"`
	AbsoluteURL string   `json:"absolute_url"`
}

type clCitingResult struct {
	Count   int `json:"count"`
	Results []struct {
		CaseName  string   `json:"case_name"`
		Citation  []string `json:"citation"`
		DateFiled string   `json:"date_filed"`
		Court     string   `json:"court"`
	} `json:"results"`
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine checks whether a citation is still good law.
type Engine struct {
	provider providers.Provider
	haiku    string
}

// New creates a CitationEngine.
func New(provider providers.Provider, haikuModel string) *Engine {
	return &Engine{provider: provider, haiku: haikuModel}
}

// Check verifies a citation and returns a CitationCheckResult.
func (e *Engine) Check(query, taskID string) types.CitationCheckResult {
	checkedAt := time.Now().UTC().Format(time.RFC3339)

	hit := clSearch(query)
	if hit == nil {
		return unknownResult(query, checkedAt,
			"CourtListener could not locate this citation. It may be very recent, not yet indexed, or the citation format is unrecognised.")
	}

	clusterID := 0
	if hit.ClusterID != nil {
		clusterID = *hit.ClusterID
	} else if m := regexp.MustCompile(`/opinion/(\d+)/`).FindStringSubmatch(hit.AbsoluteURL); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &clusterID)
	}

	cluster := clGetCluster(clusterID)
	caseName := hit.CaseName
	citation := query
	year := 0
	court := hit.Court
	clURL := ""
	if cluster != nil {
		caseName = cluster.CaseName
		if len(cluster.Citation) > 0 {
			citation = cluster.Citation[0]
		}
		yearStr := ""
		if len(cluster.DateFiled) >= 4 {
			yearStr = cluster.DateFiled[:4]
		}
		fmt.Sscanf(yearStr, "%d", &year)
		court = cluster.Court
		if clusterID > 0 {
			clURL = fmt.Sprintf("%s/opinion/%d/", clBase, clusterID)
		}
	} else if len(hit.Citation) > 0 {
		citation = hit.Citation[0]
	}

	citing := clGetCiting(clusterID, 20)
	citingCount := 0
	var citingResults []struct {
		CaseName  string
		Citation  []string
		DateFiled string
		Court     string
	}
	if citing != nil {
		citingCount = citing.Count
		for _, r := range citing.Results {
			citingResults = append(citingResults, struct {
				CaseName  string
				Citation  []string
				DateFiled string
				Court     string
			}{r.CaseName, r.Citation, r.DateFiled, r.Court})
		}
	}

	// Build snippet of citing cases for AI synthesis
	var snippetLines []string
	for i, c := range citingResults {
		if i >= 5 {
			break
		}
		yr := ""
		if len(c.DateFiled) >= 4 {
			yr = c.DateFiled[:4]
		}
		snippetLines = append(snippetLines, fmt.Sprintf("%s (%s, %s)", c.CaseName, yr, c.Court))
	}
	citingSnippet := strings.Join(snippetLines, "\n")
	if citingSnippet == "" {
		citingSnippet = "(none found)"
	}

	caseAge := ""
	if year > 0 {
		caseAge = fmt.Sprintf("%d years", time.Now().Year()-year)
	}

	system := `You are a legal citation analyst — the AI behind a KeyCite/Shepard's replacement.

Given information about a case and its citing treatment history, determine:
1. The citation signal: green (still good law), yellow (limited/questioned), red (overruled/superseded), blue (informational only)
2. The validity status: good_law, limited, overruled, superseded, unclear
3. A plain-English reasoning paragraph (2–4 sentences) explaining the signal.
4. Estimated negative treatment count and positive treatment count.
5. Up to 3 top negative treatments (if any), each with: caseName, treatmentType, year, brief note.

Respond in JSON only:
{"signal":"green"|"yellow"|"red"|"blue","status":"good_law"|"limited"|"overruled"|"superseded"|"unclear","signalLabel":"string","confidence":0.0-1.0,"negativeTreatmentCount":0,"positiveTreatmentCount":0,"topNegativeTreatments":[{"caseName":"...","treatmentType":"...","year":2024,"note":"..."}],"reasoning":"string"}`

	userMsg := fmt.Sprintf(`Case: %s
Citation: %s
Court: %s
Year: %d
Case age: %s
Total citing opinions (CourtListener): %d
Sample citing opinions (most recent):
%s

Based on this evidence, what is the citation signal?`,
		caseName, citation, court, year, caseAge, citingCount, citingSnippet)

	signal := types.CitationSignalYellow
	status := types.CitationStatus("unclear")
	signalLabel := "Caution — verify treatment manually"
	confidence := 0.5
	reasoning := "Unable to synthesise citation treatment. Verify manually."
	var treatments []types.CitationTreatment
	posCount := 0
	negCount := 0

	start := time.Now()
	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.haiku,
		MaxTokens:   512,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: userMsg}},
	})
	if err != nil {
		slog.Warn("CitationEngine AI synthesis failed", "error", err)
	} else {
		durationMs := time.Since(start).Milliseconds()
		cw := 0
		cr := 0
		if resp.Usage.CacheWriteTokens != nil {
			cw = *resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens != nil {
			cr = *resp.Usage.CacheReadTokens
		}
		costUSD := cost.CalcCostUSD(e.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
		cost.Default.Record(cost.RecordRequest{
			Model: e.haiku, Provider: "anthropic",
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
			CostUSD: costUSD, DurationMs: durationMs,
			Context: cost.CostContext("citation_check"), TaskID: taskID,
		})

		// Extract text
		raw := ""
		for _, blk := range resp.Content {
			if blk.Type == providers.BlockText {
				raw = blk.Text
				break
			}
		}
		s := strings.Index(raw, "{")
		eIdx := strings.LastIndex(raw, "}")
		if s >= 0 && eIdx > s {
			var parsed map[string]interface{}
			if json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed) == nil {
				if v, ok := parsed["signal"].(string); ok {
					signal = types.CitationSignal(v)
				}
				if v, ok := parsed["status"].(string); ok {
					status = types.CitationStatus(v)
				}
				if v, ok := parsed["signalLabel"].(string); ok {
					signalLabel = v
				}
				if v, ok := parsed["confidence"].(float64); ok {
					if v < 0 {
						v = 0
					}
					if v > 1 {
						v = 1
					}
					confidence = v
				}
				if v, ok := parsed["reasoning"].(string); ok {
					reasoning = v
				}
				if v, ok := parsed["negativeTreatmentCount"].(float64); ok {
					negCount = int(v)
				}
				if v, ok := parsed["positiveTreatmentCount"].(float64); ok {
					posCount = int(v)
				}
				if arr, ok := parsed["topNegativeTreatments"].([]interface{}); ok {
					for i, item := range arr {
						if i >= 3 {
							break
						}
						if m, ok := item.(map[string]interface{}); ok {
							t := types.CitationTreatment{
								CaseName:      strVal(m["caseName"]),
								TreatmentType: strVal(m["treatmentType"]),
							}
							if y, ok := m["year"].(float64); ok {
								yr := int(y)
								t.Year = &yr
							}
							treatments = append(treatments, t)
						}
					}
				}
			}
		}
	}

	clusterIDStr := ""
	if clusterID > 0 {
		clusterIDStr = fmt.Sprintf("%d", clusterID)
	}
	yr := year
	var yearPtr *int
	if yr > 0 {
		yearPtr = &yr
	}
	return types.CitationCheckResult{
		Query:                  query,
		ResolvedCitation:       safeStr(citation, query),
		ClusterID:              clusterIDStr,
		CaseName:               caseName,
		Court:                  court,
		Year:                   yearPtr,
		Status:                 status,
		Signal:                 signal,
		SignalLabel:            signalLabel,
		Confidence:             confidence,
		PositiveTreatmentCount: posCount,
		NegativeTreatmentCount: negCount,
		TopNegativeTreatments:  treatments,
		Reasoning:              reasoning,
		CourtListenerURL:       clURL,
		CheckedAt:              checkedAt,
		CheckedBy:              "big-michael",
	}
}

// ─── CourtListener helpers ────────────────────────────────────────────────────

func clHeaders() map[string]string {
	h := map[string]string{"Accept": "application/json"}
	if k := os.Getenv("COURT_LISTENER_API_KEY"); k != "" {
		h["Authorization"] = "Token " + k
	}
	return h
}

func clGet(urlStr string) ([]byte, error) {
	client := &http.Client{Timeout: clTimeout}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range clHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CourtListener HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, clMaxBytes))
}

func clSearch(query string) *clSearchHit {
	u := fmt.Sprintf("%s/api/rest/v4/search/?q=%s&type=o&order_by=score+desc&format=json",
		clBase, url.QueryEscape(query))
	data, err := clGet(u)
	if err != nil {
		return nil
	}
	var r clSearchResp
	if err := json.Unmarshal(data, &r); err != nil || len(r.Results) == 0 {
		return nil
	}
	return &r.Results[0]
}

func clGetCluster(id int) *clCluster {
	if id <= 0 {
		return nil
	}
	u := fmt.Sprintf("%s/api/rest/v4/clusters/%d/?format=json", clBase, id)
	data, err := clGet(u)
	if err != nil {
		return nil
	}
	var c clCluster
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	return &c
}

func clGetCiting(id, limit int) *clCitingResult {
	if id <= 0 {
		return nil
	}
	u := fmt.Sprintf("%s/api/rest/v4/search/?type=o&cited_gt=%d&order_by=-dateFiled&format=json&page_size=%d",
		clBase, id, limit)
	data, err := clGet(u)
	if err != nil {
		return nil
	}
	var r clCitingResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}
	return &r
}

func unknownResult(query, checkedAt, reasoning string) types.CitationCheckResult {
	return types.CitationCheckResult{
		Query:                 query,
		Status:                "unclear",
		Signal:                types.CitationSignalYellow,
		SignalLabel:           "Citation not found — verify manually",
		Confidence:            0,
		TopNegativeTreatments: []types.CitationTreatment{},
		Reasoning:             reasoning,
		CheckedAt:             checkedAt,
		CheckedBy:             "big-michael",
	}
}

func safeStr(s, fallback string) string {
	if s == fallback {
		return ""
	}
	return s
}

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
