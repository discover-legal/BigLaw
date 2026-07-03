// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Behavioral tests for cross-document discrepancy detection (crossdoc.go). No network,
// no real model: model calls go to an in-process httptest server speaking the
// OpenAI-compatible chat wire format (the tabreview_test.go pattern).
//
// Acceptance fixtures are the four PLANTED discrepancies from the SEC-referral task —
// all four must be flagged with BOTH verbatim quotes — and the two false-positive
// shapes a partner called embarrassing, which must NOT be flagged.

package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Fakes ──────────────────────────────────────────────────────────────────

type crossDocFakeCounts struct{ adjudications, alias int64 }

// newCrossDocFakeServer serves the two model calls this pass makes: alias unification
// ({"same": ...}) and contradiction adjudication ({"contradiction": ...}). The alias
// judge says "same" for the Ostrowski/Bayshore payment stream; the adjudicator rejects
// the 10-day/45-day obligation shape and confirms everything else.
func newCrossDocFakeServer(t *testing.T, counts *crossDocFakeCounts) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var user string
		for _, m := range body.Messages {
			if m.Role == "user" {
				var s string
				_ = json.Unmarshal(m.Content, &s)
				user = s
			}
		}

		reply := `{}`
		switch {
		case strings.Contains(user, "same underlying entity or payment stream"):
			atomic.AddInt64(&counts.alias, 1)
			if strings.Contains(user, "Bayshore") {
				reply = `{"same": true}`
			} else {
				reply = `{"same": false}`
			}
		case strings.Contains(user, "genuine inconsistency"):
			atomic.AddInt64(&counts.adjudications, 1)
			if strings.Contains(user, "45 days") {
				// Two different Code-of-Ethics obligations sharing a label: not a conflict.
				reply = `{"contradiction": false, "significance": ""}`
			} else {
				reply = `{"contradiction": true, "significance": "The government's own exhibit contradicts the referral's figure; the defense should exploit the inconsistency."}`
			}
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 50, "completion_tokens": 20},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func crossDocTestOrchestrator() *Orchestrator {
	return &Orchestrator{cfg: &config.Config{}, costs: &cost.Store{}}
}

func crossDocTestProvider(t *testing.T, url string) providers.Provider {
	t.Helper()
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = url
	cfg.Model.PrimaryKey = "test-key"
	p, err := providers.NewRegistry(cfg).Get("test-model")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fh builds a harvested figure record with Context defaulting to the quote — the shape
// the production sweep produces (post-normalizeFigures canonical Measures labels are
// pre-set here; the normalization itself is the harvest's machinery, reused not re-tested).
func fh(value, quote, source, entity, measures string) figureHit {
	return figureHit{Value: value, Quote: quote, Source: source, Entity: entity, Measures: measures, Context: quote}
}

func crossDocCorpus(hits []figureHit) map[string]string {
	docs := map[string]string{}
	for _, h := range hits {
		docs[h.Source] += h.Quote + "\n"
	}
	return docs
}

func findingWithValues(fs []types.Finding, a, b string) *types.Finding {
	for i, f := range fs {
		if strings.Contains(f.Content, a) && strings.Contains(f.Content, b) {
			return &fs[i]
		}
	}
	return nil
}

func assertBothQuotesCited(t *testing.T, f *types.Finding, quoteA, quoteB string) {
	t.Helper()
	if f == nil {
		t.Fatal("finding is nil")
	}
	if len(f.Citations) < 2 {
		t.Fatalf("finding carries %d citations, want >= 2: %s", len(f.Citations), f.Content)
	}
	found := map[string]bool{}
	for _, c := range f.Citations {
		if !c.MechanicallyVerified {
			t.Errorf("citation %q is not mechanically verified", c.Quote)
		}
		found[c.Quote] = true
	}
	for _, q := range []string{quoteA, quoteB} {
		if !found[q] {
			t.Errorf("finding is missing the verbatim quote %q; has %v", q, found)
		}
	}
	if f.AgentID != crossDocAgentID {
		t.Errorf("AgentID = %q, want %q", f.AgentID, crossDocAgentID)
	}
}

// ─── The four planted discrepancies (acceptance fixtures) ────────────────────

const (
	qTradesReferral = "The staff reviewed 4,217 omnibus equity trades allocated by Ostrowski during the review period."
	qTradesExhibit  = "Total Omnibus Equity Trades Analyzed | 4,312"
	qCompReferral   = "¶56. Ostrowski received undisclosed compensation totaling $92,600 from Bayshore Palms during the relevant period."
	qCompExhibit    = "Grand total of 22 payments, 05/2022 through 01/2024: $103,800"
	qRateReferral   = "Approximately 73% of the profitable omnibus trades were allocated to the Lakeshore proprietary account."
	qRateExhibit    = "Profitable omnibus trade allocation rate across the review population: 78%"
	qDateEmail      = "Date: Mon, 15 Apr 2024 09:23:47 -0400"
	qDateReferral   = "On or about April 22, 2024, Ostrowski instructed Chen to \"clean up the shared drive\"."
)

func fourPlantedHits() []figureHit {
	emailHeader := fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "Exhibit E — Chen Email", "Chen", "email sent date")
	emailHeader.Context = "From: Ostrowski To: Chen " + qDateEmail + " Subject: need you to clean up the shared drive before Friday"
	return []figureHit{
		// 1. Same metric, different counts: 4,217 (referral) vs 4,312 (exhibit).
		fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "Exhibit C — Trade Analysis", "", "omnibus trade count"),
		// 2. Same payment stream, different totals — entity alias Ostrowski/Bayshore Palms.
		fh("$92,600", qCompReferral, "SEC Referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", qCompExhibit, "Exhibit D — Bank Records", "Bayshore Palms LLC", "undisclosed compensation total"),
		// 3. Same event, different dates: email header vs "on or about" allegation.
		emailHeader,
		fh("April 22, 2024", qDateReferral, "SEC Referral", "Ostrowski", "obstruction instruction date"),
		// 4. Same statistic, different rates: 73% vs 78%.
		fh("73%", qRateReferral, "SEC Referral", "Ostrowski", "profitable omnibus allocation rate"),
		fh("78%", qRateExhibit, "Exhibit F — Methodology Sheet", "", "profitable omnibus allocation rate"),
	}
}

func TestCrossDocAllFourPlantedDiscrepancies(t *testing.T) {
	counts := &crossDocFakeCounts{}
	srv := newCrossDocFakeServer(t, counts)
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	hits := fourPlantedHits()
	fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, prov, "test-model")

	if len(fs) != 4 {
		var got []string
		for _, f := range fs {
			got = append(got, f.Content)
		}
		t.Fatalf("want 4 discrepancy findings, got %d:\n%s", len(fs), strings.Join(got, "\n"))
	}
	assertBothQuotesCited(t, findingWithValues(fs, "4,217", "4,312"), qTradesReferral, qTradesExhibit)
	assertBothQuotesCited(t, findingWithValues(fs, "$92,600", "$103,800"), qCompReferral, qCompExhibit)
	assertBothQuotesCited(t, findingWithValues(fs, "73%", "78%"), qRateReferral, qRateExhibit)
	assertBothQuotesCited(t, findingWithValues(fs, "15 Apr 2024", "April 22, 2024"), qDateEmail, qDateReferral)

	// The compensation pair required the model alias unification (Ostrowski ↔ Bayshore).
	if atomic.LoadInt64(&counts.alias) < 1 {
		t.Error("expected at least one alias-unification model call for Ostrowski/Bayshore")
	}
	// Every finding carries a defense-significance note (here the adjudicator's).
	for _, f := range fs {
		if !strings.Contains(f.Content, "defense") {
			t.Errorf("finding lacks a defense-significance note: %s", f.Content)
		}
	}
	// Section handle: the referral's ¶56 rides into the compensation discrepancy.
	if f := findingWithValues(fs, "$92,600", "$103,800"); f != nil && !strings.Contains(f.Content, "¶56") {
		t.Errorf("compensation finding lost the ¶56 section handle: %s", f.Content)
	}
}

// ─── The two false positives that must NOT fire ──────────────────────────────

// "2021 vs early 2022" — the vintages of the deleted spreadsheets, consistently described
// in BOTH documents. Both docs assert both values → enumeration, not conflict. The
// suppression is deterministic (per-document value-set rule): the adjudicator is never
// even consulted.
func TestCrossDocVintageEnumerationNotFlagged(t *testing.T) {
	counts := &crossDocFakeCounts{}
	srv := newCrossDocFakeServer(t, counts)
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	qRef := "The deleted spreadsheets had been created in 2021 and early 2022."
	qFor := "Recovered file fragments bore creation dates in 2021 and early 2022."
	hits := []figureHit{
		fh("2021", qRef, "SEC Referral", "Ostrowski", "deleted spreadsheet vintage"),
		fh("early 2022", qRef, "SEC Referral", "Ostrowski", "deleted spreadsheet vintage"),
		fh("2021", qFor, "Forensic Exhibit", "Ostrowski", "deleted spreadsheet vintage"),
		fh("early 2022", qFor, "Forensic Exhibit", "Ostrowski", "deleted spreadsheet vintage"),
	}
	fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, prov, "test-model")
	if len(fs) != 0 {
		t.Fatalf("vintage enumeration must not be flagged, got: %s", fs[0].Content)
	}
	if n := atomic.LoadInt64(&counts.adjudications); n != 0 {
		t.Errorf("enumeration should be suppressed deterministically, but the adjudicator was called %d times", n)
	}
}

// "10-day vs 45-day" — two DIFFERENT Code-of-Ethics obligations. Three layers of defense,
// each tested: (a) canonical metric-identity separation — different labels are never
// compared; (b) label collision — the adjudicator sees both contexts and rejects it;
// (c) no provider — bare durations are never flagged deterministically.
func TestCrossDocDifferentObligationsNotFlagged(t *testing.T) {
	qInitial := "Each access person shall file an initial holdings report within 10 days of becoming an access person."
	qAnnual := "The referral notes annual holdings reports were due within 45 days of the fiscal year end."

	t.Run("distinct canonical labels never meet", func(t *testing.T) {
		counts := &crossDocFakeCounts{}
		srv := newCrossDocFakeServer(t, counts)
		defer srv.Close()
		o := crossDocTestOrchestrator()
		prov := crossDocTestProvider(t, srv.URL)
		hits := []figureHit{
			fh("10 days", qInitial, "Code of Ethics", "access persons", "initial holdings report deadline"),
			fh("45 days", qAnnual, "SEC Referral", "access persons", "annual holdings report deadline"),
		}
		fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, prov, "test-model")
		if len(fs) != 0 {
			t.Fatalf("different obligations must not be flagged, got: %s", fs[0].Content)
		}
		if n := atomic.LoadInt64(&counts.adjudications); n != 0 {
			t.Errorf("distinct labels should never reach the adjudicator, called %d times", n)
		}
	})

	t.Run("label collision is rejected by the adjudicator", func(t *testing.T) {
		counts := &crossDocFakeCounts{}
		srv := newCrossDocFakeServer(t, counts)
		defer srv.Close()
		o := crossDocTestOrchestrator()
		prov := crossDocTestProvider(t, srv.URL)
		hits := []figureHit{
			fh("10 days", qInitial, "Code of Ethics", "access persons", "holdings report deadline"),
			fh("45 days", qAnnual, "SEC Referral", "access persons", "holdings report deadline"),
		}
		fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, prov, "test-model")
		if len(fs) != 0 {
			t.Fatalf("adjudicator rejection must suppress the finding, got: %s", fs[0].Content)
		}
		if n := atomic.LoadInt64(&counts.adjudications); n != 1 {
			t.Errorf("expected exactly one adjudication call, got %d", n)
		}
	})

	t.Run("no provider: bare durations are never flagged", func(t *testing.T) {
		o := crossDocTestOrchestrator()
		hits := []figureHit{
			fh("10 days", qInitial, "Code of Ethics", "access persons", "holdings report deadline"),
			fh("45 days", qAnnual, "SEC Referral", "access persons", "holdings report deadline"),
		}
		fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, "")
		if len(fs) != 0 {
			t.Fatalf("durations must not be flagged on the deterministic path, got: %s", fs[0].Content)
		}
	})
}

// ─── Alias unification: deterministic path ────────────────────────────────────

// A grounded evidence-graph fact linking the two entities unifies them WITHOUT a model
// call — tested with no provider at all, which also exercises graceful degradation
// (deterministic conflict rules + the template significance note).
func TestCrossDocAliasUnificationDeterministicGraphTie(t *testing.T) {
	qRef := "¶56. Ostrowski received undisclosed compensation payments totaling $92,600 from Bayshore Palms."
	qEx := "Grand total of undisclosed compensation payments received: $103,800"
	hits := []figureHit{
		fh("$92,600", qRef, "SEC Referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", qEx, "Exhibit D — Bank Records", "Bayshore Palms LLC", "undisclosed compensation total"),
	}
	g := evidencegraph.New()
	link := "Ostrowski controls Bayshore Palms LLC"
	if !g.Add(evidencegraph.Fact{Subject: "Ostrowski", Relation: "controls", Object: "Bayshore Palms LLC", Quote: link, Source: "SEC Referral"}, link) {
		t.Fatal("graph fact was not accepted")
	}

	o := crossDocTestOrchestrator()
	fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), g, nil, "")
	f := findingWithValues(fs, "$92,600", "$103,800")
	if f == nil {
		t.Fatalf("graph-tied alias pair was not flagged; findings: %v", fs)
	}
	assertBothQuotesCited(t, f, qRef, qEx)
	if !strings.Contains(f.Content, "do not silently reconcile") {
		t.Errorf("no-provider finding should carry the template note, got: %s", f.Content)
	}
	// Without the graph tie the entities stay separate and nothing is flagged —
	// the deterministic fallback never unifies parties on a guess.
	if fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, ""); len(fs) != 0 {
		t.Errorf("without a graph tie the alias pair must not unify deterministically, got: %s", fs[0].Content)
	}
}

// Token containment ("Ostrowski" ⊆ "Richard Ostrowski") unifies deterministically.
func TestCrossDocAliasTokenContainment(t *testing.T) {
	qA := "Ostrowski reviewed a total of 4,217 omnibus equity trades in the sample."
	qB := "Richard Ostrowski analyzed 4,312 omnibus equity trades across the sample."
	hits := []figureHit{
		fh("4,217", qA, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qB, "Exhibit C", "Richard Ostrowski", "omnibus trade count"),
	}
	o := crossDocTestOrchestrator()
	fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, "")
	if findingWithValues(fs, "4,217", "4,312") == nil {
		t.Fatalf("token-contained entities should unify deterministically; findings: %v", fs)
	}
}

// ─── No-provider degradation ──────────────────────────────────────────────────

func TestCrossDocNoProviderDegradation(t *testing.T) {
	hits := []figureHit{
		fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "Exhibit C — Trade Analysis", "", "omnibus trade count"),
	}
	o := crossDocTestOrchestrator()
	fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, "")
	f := findingWithValues(fs, "4,217", "4,312")
	if f == nil {
		t.Fatalf("trade-count conflict must survive without a provider; findings: %v", fs)
	}
	assertBothQuotesCited(t, f, qTradesReferral, qTradesExhibit)
	if !strings.Contains(f.Content, "do not silently reconcile") {
		t.Errorf("degraded finding should carry the template significance note: %s", f.Content)
	}

	// Date conflict also degrades gracefully (template date note).
	emailHeader := fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "Exhibit E — Chen Email", "Chen", "email sent date")
	emailHeader.Context = "From: Ostrowski To: Chen " + qDateEmail + " Subject: need you to clean up the shared drive"
	dateHits := []figureHit{emailHeader, fh("April 22, 2024", qDateReferral, "SEC Referral", "Ostrowski", "obstruction instruction date")}
	dfs := o.crossDocFindings("task-1", dateHits, crossDocCorpus(dateHits), nil, nil, "")
	df := findingWithValues(dfs, "15 Apr 2024", "April 22, 2024")
	if df == nil {
		t.Fatalf("date conflict must survive without a provider; findings: %v", dfs)
	}
	if !strings.Contains(df.Content, "metadata conflicts") {
		t.Errorf("degraded date finding should carry the date template note: %s", df.Content)
	}
}

// ─── Grounding, canonicalization, integration ─────────────────────────────────

// The substring lock: a quote that is not a verbatim span of its source document is
// dropped before it can conflict with anything.
func TestCrossDocSubstringLockDropsFabricatedQuote(t *testing.T) {
	hits := []figureHit{
		fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", "a fabricated quote that appears nowhere", "Exhibit C — Trade Analysis", "", "omnibus trade count"),
	}
	docs := map[string]string{
		"SEC Referral":               qTradesReferral,
		"Exhibit C — Trade Analysis": "The exhibit says something else entirely.",
	}
	o := crossDocTestOrchestrator()
	if fs := o.crossDocFindings("task-1", hits, docs, nil, nil, ""); len(fs) != 0 {
		t.Fatalf("a fabricated quote must be dropped by the substring lock, got: %s", fs[0].Content)
	}
}

// Formatting differences are not conflicts: "4,217" and "4217" share a canonical value.
func TestCrossDocCanonicalValueEquality(t *testing.T) {
	qA := "The staff reviewed 4,217 omnibus equity trades in total."
	qB := "A population of 4217 omnibus equity trades was reviewed."
	hits := []figureHit{
		fh("4,217", qA, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4217", qB, "Exhibit C", "Ostrowski", "omnibus trade count"),
	}
	o := crossDocTestOrchestrator()
	if fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, ""); len(fs) != 0 {
		t.Fatalf("equal canonical values must not be flagged, got: %s", fs[0].Content)
	}
}

// Precision-aware dates: "April 2024" is consistent with "April 22, 2024".
func TestCrossDocDatePrecisionNotAConflict(t *testing.T) {
	qA := "In April 2024 Ostrowski instructed Chen to clean up the shared drive."
	qB := "On or about April 22, 2024, Ostrowski instructed Chen to clean up the shared drive."
	hits := []figureHit{
		fh("April 2024", qA, "Memo", "Ostrowski", "obstruction instruction date"),
		fh("April 22, 2024", qB, "SEC Referral", "Ostrowski", "obstruction instruction date"),
	}
	o := crossDocTestOrchestrator()
	if fs := o.crossDocFindings("task-1", hits, crossDocCorpus(hits), nil, nil, ""); len(fs) != 0 {
		t.Fatalf("coarse vs precise dates must not conflict, got: %s", fs[0].Content)
	}
}

func TestParseCrossDocDate(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Mon, 15 Apr 2024 09:23:47 -0400", "2024-04-15", true},
		{"April 22, 2024", "2024-04-22", true},
		{"on or about April 22, 2024", "2024-04-22", true},
		{"15 Apr 2024", "2024-04-15", true},
		{"2024-04-15", "2024-04-15", true},
		{"04/22/2024", "2024-04-22", true},
		{"May 2022", "2022-05", true},
		{"05/2022", "2022-05", true},
		{"early 2022", "2022", true},
		{"2021", "2021", true},
		{"4,217", "", false},
		{"not a date", "", false},
	}
	for _, c := range cases {
		d, ok := parseCrossDocDate(c.in)
		if ok != c.ok {
			t.Errorf("parseCrossDocDate(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && d.key() != c.want {
			t.Errorf("parseCrossDocDate(%q) = %s, want %s", c.in, d.key(), c.want)
		}
	}
}

// The writer picks crossdoc findings up under the existing discrepancy section framing.
func TestCrossDocFindingsRideDiscrepancySection(t *testing.T) {
	o := crossDocTestOrchestrator()
	f := crossDocBuildFinding([]crossDocEntry{
		{hit: fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count")},
		{hit: fh("4,312", qTradesExhibit, "Exhibit C", "", "omnibus trade count")},
	}, "Ostrowski", "omnibus trade count", "", crossDocTemplateNote)
	task := &types.Task{ID: "task-1", Findings: []types.Finding{f}}
	body := o.appendDiscrepancies(task, "THE MEMO BODY")
	if !strings.Contains(body, "## Discrepancies and Defense Issues") {
		t.Fatal("crossdoc finding did not produce the discrepancy section")
	}
	if !strings.Contains(body, "4,217") || !strings.Contains(body, "4,312") {
		t.Errorf("discrepancy section lost the conflicting values:\n%s", body)
	}
}
