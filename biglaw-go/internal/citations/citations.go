// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CitationEngine — "Good Law?" signal for any case citation.
// Direct replacement for Westlaw KeyCite and LexisNexis Shepard's.
// Uses CourtListener's free public API + Haiku AI synthesis.

package citations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
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
	ID          *int     `json:"id"`
	ClusterID   *int     `json:"cluster_id"`
	CaseName    string   `json:"case_name"`
	Citation    []string `json:"citation"`
	Court       string   `json:"court"`
	DateFiled   string   `json:"date_filed"`
	AbsoluteURL string   `json:"absolute_url"`
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

var opinionPathRe = regexp.MustCompile(`/opinion/(\d+)/`)

// Check verifies a citation.
//
// A CourtListener failure (search, cluster or citing-opinions lookup) is an
// explicit UNKNOWN result ("not checked") — the treatment evidence the signal
// rests on is missing, so it must never read as "no negative treatment". A
// model failure (call failed, timed out, unusable output) is an error carrying
// a providers sentinel, answered as a typed HTTP error like every other
// model-backed engine; nothing is presented as a treatment verdict.
func (e *Engine) Check(ctx context.Context, query, taskID string) (types.CitationCheckResult, error) {
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	notReached := func(what string, err error) types.CitationCheckResult {
		slog.Warn("CitationEngine: CourtListener lookup failed", "lookup", what, "error", err)
		return unknownResult(query, checkedAt,
			"CourtListener could not be reached ("+what+" lookup failed), so this citation has NOT been checked. Verify it manually or try again later.")
	}

	hit, err := clSearch(ctx, query)
	if err != nil {
		return notReached("search", err), ctxErr(ctx)
	}
	if hit == nil {
		return unknownResult(query, checkedAt,
			"CourtListener could not locate this citation. It may be very recent, not yet indexed, or the citation format is unrecognised."), nil
	}

	clusterID := 0
	if hit.ClusterID != nil {
		clusterID = *hit.ClusterID
	} else if m := opinionPathRe.FindStringSubmatch(hit.AbsoluteURL); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &clusterID)
	}
	if clusterID <= 0 {
		// Without a cluster id there is no citing-opinions history to judge.
		return unknownResult(query, checkedAt,
			"CourtListener matched this citation but did not identify the opinion, so its treatment has NOT been checked. Verify it manually."), nil
	}

	cluster, err := clGetCluster(ctx, clusterID)
	if err != nil {
		return notReached("opinion", err), ctxErr(ctx)
	}
	citing, err := clGetCiting(ctx, clusterID, 20)
	if err != nil {
		return notReached("citing-opinions", err), ctxErr(ctx)
	}

	caseName := cluster.CaseName
	if caseName == "" {
		caseName = hit.CaseName
	}
	citation := query
	if len(cluster.Citation) > 0 {
		citation = cluster.Citation[0]
	} else if len(hit.Citation) > 0 {
		citation = hit.Citation[0]
	}
	year := 0
	if len(cluster.DateFiled) >= 4 {
		fmt.Sscanf(cluster.DateFiled[:4], "%d", &year)
	}
	court := cluster.Court
	if court == "" {
		court = hit.Court
	}
	clURL := fmt.Sprintf("%s/opinion/%d/", clBase, clusterID)

	// Build snippet of citing cases for AI synthesis
	var snippetLines []string
	for i, c := range citing.Results {
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
		caseName, citation, court, year, caseAge, citing.Count, citingSnippet)

	resp, err := e.provider.Chat(providers.ChatParams{
		Ctx:         ctx,
		Model:       e.haiku,
		MaxTokens:   512,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: userMsg}},
	})
	if err != nil {
		return types.CitationCheckResult{}, providers.ModelFailure("citation treatment analysis", err)
	}
	cw := 0
	cr := 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	cost.Default.Record(cost.RecordRequest{
		Model: e.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD:    cost.CalcCostUSD(e.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr),
		DurationMs: resp.DurationMs,
		Context:    cost.CostContext("citation_check"), TaskID: taskID,
	})

	raw := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			raw = blk.Text
			break
		}
	}
	a, err := parseAnalysis(raw)
	if err != nil {
		return types.CitationCheckResult{}, providers.ModelFailure("citation treatment analysis", err)
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
		Status:                 a.Status,
		Signal:                 a.Signal,
		SignalLabel:            a.SignalLabel,
		Confidence:             a.Confidence,
		PositiveTreatmentCount: a.Positive,
		NegativeTreatmentCount: a.Negative,
		TopNegativeTreatments:  a.Treatments,
		Reasoning:              a.Reasoning,
		CourtListenerURL:       clURL,
		CheckedAt:              checkedAt,
		CheckedBy:              "big-michael",
	}, nil
}

// ctxErr returns the caller's cancellation, if any, so an aborted request is
// reported as cancelled rather than answered with a "not checked" result.
func ctxErr(ctx context.Context) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

// analysis is the model's treatment verdict after validation.
type analysis struct {
	Signal             types.CitationSignal
	Status             types.CitationStatus
	SignalLabel        string
	Confidence         float64
	Positive, Negative int
	Treatments         []types.CitationTreatment
	Reasoning          string
}

// parseAnalysis decodes the model's JSON leniently (case-insensitive signal
// and status, fenced or prefixed output) but requires a recognised signal:
// anything else is unusable output, never a guessed verdict.
func parseAnalysis(raw string) (analysis, error) {
	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return analysis{}, fmt.Errorf("no JSON object in model output")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return analysis{}, fmt.Errorf("unparseable model output: %w", err)
	}
	sig := types.CitationSignal(strings.ToLower(strings.TrimSpace(strVal(parsed["signal"]))))
	switch sig {
	case types.CitationSignalGreen, types.CitationSignalYellow, types.CitationSignalRed, types.CitationSignalBlue:
	default:
		return analysis{}, fmt.Errorf("unrecognised citation signal %q", strVal(parsed["signal"]))
	}
	a := analysis{
		Signal:      sig,
		Status:      types.CitationStatus(strings.ToLower(strings.TrimSpace(strVal(parsed["status"])))),
		SignalLabel: strVal(parsed["signalLabel"]),
		Reasoning:   strVal(parsed["reasoning"]),
		Treatments:  []types.CitationTreatment{},
	}
	if a.Status == "" {
		a.Status = types.CitationStatus("unclear")
	}
	if v, ok := parsed["confidence"].(float64); ok {
		a.Confidence = math.Max(0, math.Min(1, v))
	}
	if v, ok := parsed["negativeTreatmentCount"].(float64); ok && v > 0 {
		a.Negative = int(v)
	}
	if v, ok := parsed["positiveTreatmentCount"].(float64); ok && v > 0 {
		a.Positive = int(v)
	}
	arr, _ := parsed["topNegativeTreatments"].([]any)
	for _, item := range arr {
		if len(a.Treatments) >= 3 {
			break
		}
		if m, ok := item.(map[string]any); ok {
			t := types.CitationTreatment{
				CaseName:      strVal(m["caseName"]),
				TreatmentType: strVal(m["treatmentType"]),
			}
			if y, ok := m["year"].(float64); ok {
				yr := int(y)
				t.Year = &yr
			}
			a.Treatments = append(a.Treatments, t)
		}
	}
	return a, nil
}

// ─── CourtListener helpers ────────────────────────────────────────────────────

func clHeaders() map[string]string {
	h := map[string]string{"Accept": "application/json"}
	if k := os.Getenv("COURT_LISTENER_API_KEY"); k != "" {
		h["Authorization"] = "Token " + k
	}
	return h
}

func clGet(ctx context.Context, urlStr string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: clTimeout}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
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

// clSearch returns (nil, nil) when CourtListener answered with no match and
// an error when the lookup itself failed.
func clSearch(ctx context.Context, query string) (*clSearchHit, error) {
	u := fmt.Sprintf("%s/api/rest/v4/search/?q=%s&type=o&order_by=score+desc&format=json",
		clBase, url.QueryEscape(query))
	data, err := clGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var r clSearchResp
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("CourtListener search response unparseable: %w", err)
	}
	if len(r.Results) == 0 {
		return nil, nil
	}
	return &r.Results[0], nil
}

func clGetCluster(ctx context.Context, id int) (*clCluster, error) {
	data, err := clGet(ctx, fmt.Sprintf("%s/api/rest/v4/clusters/%d/?format=json", clBase, id))
	if err != nil {
		return nil, err
	}
	var c clCluster
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("CourtListener cluster response unparseable: %w", err)
	}
	return &c, nil
}

func clGetCiting(ctx context.Context, id, limit int) (*clCitingResult, error) {
	u := fmt.Sprintf("%s/api/rest/v4/search/?type=o&cited_gt=%d&order_by=-dateFiled&format=json&page_size=%d",
		clBase, id, limit)
	data, err := clGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var r clCitingResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("CourtListener citing-opinions response unparseable: %w", err)
	}
	return &r, nil
}

func unknownResult(query, checkedAt, reasoning string) types.CitationCheckResult {
	return types.CitationCheckResult{
		Query:                 query,
		Status:                types.CitationStatusUnknown,
		Signal:                types.CitationSignalUnknown,
		SignalLabel:           "Unknown — citation not checked; verify manually",
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
