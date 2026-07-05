// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Behavioral tests for the deviation-detection fix-wave port (deviations.go). No network,
// no real model: model calls go to an in-process httptest server speaking the
// OpenAI-compatible chat wire format (the tabreview_test.go / crossdoc_test.go pattern).
//
// Acceptance fixtures mirror the compare-trust-documents plateau forensics: fabricated
// summary values must be withheld/flagged (never emitted as-is), requirement-vs-document
// numeric mismatches must be caught mechanically with BOTH verbatim quotes, multi-part
// requirements must get per-part verdicts (partial coverage never presents as full), and
// the deterministic section walk must represent BOTH documents under review (the
// pour-over-will blind spot).

package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Fake model server ────────────────────────────────────────────────────────

// newDevFakeServer routes each model call to a per-test reply function keyed on the
// user message. Unmatched calls answer "{}" (parse-miss → dropped, the safe default).
func newDevFakeServer(t *testing.T, reply func(user string) string) *httptest.Server {
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
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply(user)},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 50, "completion_tokens": 20},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// ─── Fixtures: instruction memo + two drafts (trust AND will) ─────────────────

const (
	devMemoTitle  = "Client Instruction Memo"
	devTrustTitle = "Draft Revocable Trust Agreement"
	devWillTitle  = "Draft Pour-Over Will"
)

const devMemoText = `Section 1. Residuary Estate.
The residuary estate shall be divided as follows: David shall receive Forty Percent (40%), Sophia shall receive Thirty-Five Percent (35%), and Tommy shall receive Twenty-Five Percent (25%).

Section 2. Piano Bequest.
The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy and shall be held by the trustee until Tommy reaches age 30.

Section 3. Disinheritance.
The pour-over will must include a clause expressly disinheriting any person not named in this memorandum.
`

const devTrustText = `Article I. Residuary.
The residuary trust estate shall be distributed forty percent (40%) to David, thirty percent (30%) to Sophia, and thirty percent (30%) to Tommy.

Article II. Tangible Property.
The Steinway piano shall pass to Tommy.
`

const devWillText = `Article 1. Pour-Over.
All the residue of my estate I give to the trustee of the Davenport Trust to be administered under its terms.

Article 2. Guardian.
I nominate Jennifer Chen as guardian of my minor children.
`

func devTestCorpus() *devCorpus {
	return newDevCorpus(
		[]string{devMemoTitle, devTrustTitle, devWillTitle},
		[]string{devMemoText, devTrustText, devWillText},
	)
}

func devTestTask() *types.Task { return &types.Task{ID: "task-dev"} }

// ─── Corpus: classification + section-walk determinism (both documents) ───────

func TestDevCorpusClassificationAndDeterminism(t *testing.T) {
	c1, c2 := devTestCorpus(), devTestCorpus()
	if len(c1.docs) != 3 {
		t.Fatalf("want 3 docs, got %d", len(c1.docs))
	}
	for i, d := range c1.docs {
		wantCtrl := d.title == devMemoTitle
		if d.controlling != wantCtrl {
			t.Errorf("doc %q controlling = %v, want %v", d.title, d.controlling, wantCtrl)
		}
		// The section walk is deterministic: same text in, same chunks out …
		if !reflect.DeepEqual(d.chunks, c2.docs[i].chunks) {
			t.Errorf("doc %q: section walk is not deterministic across builds", d.title)
		}
		// … and lossless: concatenation reproduces the source byte-for-byte.
		if got := strings.Join(d.chunks, ""); got != d.text {
			t.Errorf("doc %q: section chunks do not reproduce the text", d.title)
		}
	}
}

// The title heuristic failing to split sides falls back to instruction-density: the doc
// densest in shall/must/require language becomes the controlling source.
func TestDevCorpusDensityFallback(t *testing.T) {
	c := newDevCorpus(
		[]string{"Document A", "Document B"}, // neither title says memo/instruction
		[]string{
			"The trustee shall distribute the estate. Shares must equal the required split. The protector shall not modify interests.",
			"I give my property to my children in equal shares.",
		},
	)
	if !c.docs[0].controlling || c.docs[1].controlling {
		t.Errorf("density fallback misclassified: A ctrl=%v B ctrl=%v", c.docs[0].controlling, c.docs[1].controlling)
	}
}

// ─── Retrieval: both documents under review are always represented ─────────────

func TestDevRetrieveRepresentsBothReviewDocuments(t *testing.T) {
	o := crossDocTestOrchestrator() // no tool registry → deterministic floor only
	ctx := o.retrieveForDeviation(devTestTask(), devTestCorpus(), "Residuary split: David 40%, Sophia 35%, Tommy 25%")
	for _, want := range []string{
		"CONTROLLING SOURCE — " + devMemoTitle,
		"DOCUMENT UNDER REVIEW — " + devTrustTitle,
		"DOCUMENT UNDER REVIEW — " + devWillTitle,
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("retrieval context missing section %q:\n%s", want, ctx)
		}
	}
	// The requirement's controlling passage and the trust's conflicting passage both land.
	if !strings.Contains(ctx, "Thirty-Five Percent (35%)") || !strings.Contains(ctx, "thirty percent (30%) to Sophia") {
		t.Errorf("retrieval lost the paired should-be/is passages:\n%s", ctx)
	}
	// A requirement no document addresses yields the explicit per-document omission signal
	// rather than silence.
	ctx2 := o.retrieveForDeviation(devTestTask(), devTestCorpus(), "Section 3 disinheritance clause requirement")
	if !strings.Contains(ctx2, "(No provision addressing this requirement was found in this document.)") {
		t.Errorf("empty review side must read as an explicit omission signal:\n%s", ctx2)
	}
}

// ─── Grounded conflict: both verbatim quotes, mechanically-verified citations ──

func devConflictReply(summary, recommendation string) string {
	v := map[string]interface{}{
		"type":             "conflict",
		"document":         devTrustTitle,
		"instructionQuote": "Sophia shall receive Thirty-Five Percent (35%)",
		"draftQuote":       "thirty percent (30%) to Sophia",
		"summary":          summary,
		"severity":         "high",
		"recommendation":   recommendation,
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestDevConflictCarriesBothQuotesAndCitations(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			return devConflictReply(
				"The memo requires Sophia's residuary share to be 35% but the draft trust grants only 30%.",
				"Change Sophia's share to Thirty-Five Percent (35%).",
			)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d: %v", len(fs), fs)
	}
	f := fs[0]
	if !strings.Contains(f.Content, "35%") || !strings.Contains(f.Content, "30%") {
		t.Errorf("verified summary was not kept: %s", f.Content)
	}
	// Both verbatim quotes travel with the finding …
	for _, q := range []string{"Sophia shall receive Thirty-Five Percent (35%)", "thirty percent (30%) to Sophia"} {
		if !strings.Contains(f.Content, q) {
			t.Errorf("finding is missing the verbatim quote %q: %s", q, f.Content)
		}
	}
	// … the deviating document is named …
	if !strings.Contains(f.Content, devTrustTitle) {
		t.Errorf("finding does not name the deviating document: %s", f.Content)
	}
	// … and both quotes are mechanically-verified citations with real source attribution.
	if len(f.Citations) != 2 {
		t.Fatalf("want 2 citations, got %d", len(f.Citations))
	}
	srcs := map[string]string{}
	for _, c := range f.Citations {
		if !c.MechanicallyVerified {
			t.Errorf("citation %q not mechanically verified", c.Quote)
		}
		srcs[c.Source] = c.Quote
	}
	if srcs[devMemoTitle] == "" || srcs[devTrustTitle] == "" {
		t.Errorf("citations must attribute to memo and trust, got %v", srcs)
	}
	if f.EvidenceStatus != types.EvidenceGrounded {
		t.Errorf("cited deviation should be EvidenceGrounded, got %q", f.EvidenceStatus)
	}
}

// ─── Grounded-value discipline ─────────────────────────────────────────────────

// A summary asserting a value present nowhere in the retrieved passages is fabricated:
// it must be withheld and replaced by the verbatim quotes plus an explicit flag — never
// emitted as-is. A recommendation with a fabricated value is replaced by the generic
// correction.
func TestDevFabricatedSummaryValueWithheld(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			return devConflictReply(
				"The memo requires Sophia's share to be 20% but the draft grants a different amount.", // 20% appears nowhere
				"Change Sophia's share to 20%.",
			)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding (grounded quotes survive; only the summary is withheld), got %d", len(fs))
	}
	f := fs[0]
	if strings.Contains(f.Content, "20%") {
		t.Errorf("fabricated value 20%% must never be emitted: %s", f.Content)
	}
	if !strings.Contains(f.Content, "[model summary withheld") {
		t.Errorf("withheld summary must be flagged: %s", f.Content)
	}
	for _, q := range []string{"Sophia shall receive Thirty-Five Percent (35%)", "thirty percent (30%) to Sophia"} {
		if !strings.Contains(f.Content, q) {
			t.Errorf("withheld summary must be replaced by the verbatim quotes; missing %q: %s", q, f.Content)
		}
	}
	if !strings.Contains(f.Content, "Conform the document under review to the quoted controlling-source language.") {
		t.Errorf("fabricated recommendation must be replaced by the generic correction: %s", f.Content)
	}
}

// Legitimate derived arithmetic is NOT fabrication: "the shares total 100%" verifies as
// the sum of grounded values (40 + 30 + 30), so the summary is kept.
func TestDevDerivedTotalNotFlagged(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			return devConflictReply(
				"The draft's shares (40% + 30% + 30% = 100%) do not match the required 40%/35%/25% split.",
				"Restore the required 40%/35%/25% split.",
			)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if !strings.Contains(fs[0].Content, "100%") {
		t.Errorf("derived total must not be treated as fabricated: %s", fs[0].Content)
	}
}

func TestDevUnverifiedValues(t *testing.T) {
	ground := "Forty Percent (40%) … Thirty-Five Percent (35%) … Twenty-Five Percent (25%) … Serial #612847 … $92,600"
	cases := []struct {
		assertion string
		bad       int
	}{
		{"requires 35% not 25%", 0},
		{"the shares total 100%", 0},         // 40+35+25
		{"understated by 10%", 0},            // 35-25
		{"the serial is 612847", 0},          // grounded identifier
		{"$92,600 in compensation", 0},       // grounded money
		{"requires 30% for Sophia", 1},       // fabricated percent
		{"a bequest of $150,000", 1},         // fabricated money
		{"serial number 999999", 1},          // fabricated identifier
		{"no numeric assertion here", 0},     // nothing to verify
		{"paragraph 3 of the memo lists", 0}, // small integers out of scope
	}
	for _, c := range cases {
		if got := len(devUnverifiedValues(c.assertion, ground)); got != c.bad {
			t.Errorf("devUnverifiedValues(%q) = %d unverified, want %d", c.assertion, got, c.bad)
		}
	}
}

// ─── Substring lock (fabricated quotes) ────────────────────────────────────────

func TestDevFabricatedInstructionQuoteDropped(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			v := map[string]interface{}{
				"type":             "conflict",
				"document":         devTrustTitle,
				"instructionQuote": "Sophia shall receive Fifty Percent (50%)", // appears nowhere
				"draftQuote":       "thirty percent (30%) to Sophia",
				"summary":          "The memo requires 50% for Sophia.",
				"severity":         "high",
				"recommendation":   "Fix it.",
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, "")
	if len(fs) != 0 {
		t.Fatalf("a fabricated instruction quote must be dropped by the substring lock, got: %s", fs[0].Content)
	}
}

// The conform-leak guard is preserved: a "deviation" whose own summary affirms conformance
// is dropped, and type=none emits nothing.
func TestDevConformLeakGuardPreserved(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			return devConflictReply("The document correctly states Sophia's share as required.", "")
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)
	if fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, ""); len(fs) != 0 {
		t.Fatalf("conformance summary must be dropped, got: %s", fs[0].Content)
	}
}

// ─── Multi-part decomposition: per-part verdicts ───────────────────────────────

func TestDevMultiPartPerPartVerdicts(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		switch {
		case strings.HasPrefix(user, "REQUIRED PROVISION:"):
			return "ABSENT"
		case strings.HasPrefix(user, "REQUIREMENT:"):
			v := map[string]interface{}{
				"type":             "none", // the top-level verdict alone would read as conformance
				"document":         devTrustTitle,
				"instructionQuote": "The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy",
				"summary":          "The piano bequest is present but incomplete.",
				"severity":         "medium",
				"recommendation":   "Add the identifying details and the holding instruction.",
				"parts": []map[string]interface{}{
					{"part": "piano passes to Tommy", "status": "conforms"},
					{"part": "serial number", "status": "omission", "instructionQuote": "Serial #612847"},
					{"part": "held until age 30", "status": "omission", "instructionQuote": "shall be held by the trustee until Tommy reaches age 30"},
				},
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Piano bequest to Tommy"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("partial coverage must surface as a deviation even when type=none, got %d findings", len(fs))
	}
	f := fs[0]
	if !strings.Contains(f.Content, "only in part") {
		t.Errorf("partial implementation must be named as such: %s", f.Content)
	}
	if !strings.Contains(f.Content, "piano passes to Tommy") {
		t.Errorf("conforming sub-part must be listed (so partial never reads as full): %s", f.Content)
	}
	for _, q := range []string{"Serial #612847", "until Tommy reaches age 30"} {
		if !strings.Contains(f.Content, q) {
			t.Errorf("deviating sub-part quote %q missing: %s", q, f.Content)
		}
	}
	if !strings.Contains(f.Content, "OMITTED") {
		t.Errorf("omitted sub-parts must carry a per-part verdict: %s", f.Content)
	}
	if len(f.Citations) != 2 {
		t.Errorf("want a mechanically-verified citation per deviating part, got %d", len(f.Citations))
	}
}

// A part whose quote fails the substring lock is dropped — parts are grounded one by one.
func TestDevMultiPartUngroundedPartDropped(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		switch {
		case strings.HasPrefix(user, "REQUIRED PROVISION:"):
			return "ABSENT"
		case strings.HasPrefix(user, "REQUIREMENT:"):
			v := map[string]interface{}{
				"type": "none", "document": devTrustTitle,
				"summary": "incomplete", "severity": "medium",
				"parts": []map[string]interface{}{
					{"part": "serial number", "status": "omission", "instructionQuote": "Serial #612847"},
					{"part": "insurance rider", "status": "omission", "instructionQuote": "a $50,000 insurance rider"}, // fabricated
				},
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Piano bequest to Tommy"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if strings.Contains(fs[0].Content, "insurance rider") {
		t.Errorf("ungrounded part must be dropped: %s", fs[0].Content)
	}
	if !strings.Contains(fs[0].Content, "Serial #612847") {
		t.Errorf("grounded part must survive: %s", fs[0].Content)
	}
}

// ─── Omission in the SECOND document under review ──────────────────────────────

// The memo requires the pour-over will to carry a disinheritance clause; the will fixture
// lacks it. The omission must be confirmed against the WILL's own sections (not masked by
// the trust) and the finding must name the will.
func TestDevOmissionInSecondDocument(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		switch {
		case strings.HasPrefix(user, "REQUIRED PROVISION:"):
			return "ABSENT"
		case strings.HasPrefix(user, "REQUIREMENT:"):
			v := map[string]interface{}{
				"type":              "omission",
				"document":          devWillTitle,
				"instructionQuote":  "The pour-over will must include a clause expressly disinheriting any person not named",
				"requiredProvision": "disinheritance clause",
				"summary":           "The pour-over will omits the required disinheritance clause.",
				"severity":          "high",
				"recommendation":    "Add an express disinheritance clause to the pour-over will.",
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Disinheritance clause in the pour-over will"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 omission finding, got %d", len(fs))
	}
	f := fs[0]
	if !strings.HasPrefix(f.Content, "OMISSION") {
		t.Errorf("omission label missing: %s", f.Content)
	}
	if !strings.Contains(f.Content, devWillTitle) {
		t.Errorf("the deviating document (the will) must be named: %s", f.Content)
	}
	if len(f.Citations) != 1 || f.Citations[0].Source != devMemoTitle || !f.Citations[0].MechanicallyVerified {
		t.Errorf("omission must cite the controlling source's verbatim requirement, got %+v", f.Citations)
	}
}

// The per-document omission scope: the draft context for a will-attributed omission comes
// from the will only — a matching provision in the trust must not mask the will's gap.
func TestDevRetrieveDraftContextScopedToDocument(t *testing.T) {
	o := crossDocTestOrchestrator()
	corpus := devTestCorpus()
	// "piano" lives in the trust. Scoped to the will, the context must be empty.
	if got := o.retrieveDraftContext(devTestTask(), corpus, "Steinway piano bequest", devWillTitle); strings.TrimSpace(got) != "" {
		t.Errorf("will-scoped context must not carry trust passages, got: %q", got)
	}
	if got := o.retrieveDraftContext(devTestTask(), corpus, "Steinway piano bequest", devTrustTitle); !strings.Contains(got, "Steinway") {
		t.Errorf("trust-scoped context should carry the trust's piano section, got: %q", got)
	}
	// Unscoped: all documents under review, never the controlling memo.
	if got := o.retrieveDraftContext(devTestTask(), corpus, "residuary estate division", ""); strings.Contains(got, "Thirty-Five Percent (35%)") {
		t.Errorf("draft context must never include the controlling source, got: %q", got)
	}
}

// ─── Mechanical numeric join ───────────────────────────────────────────────────

func TestDevNumericJoinBothQuotes(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.Contains(user, "genuine inconsistency") {
			return `{"contradiction": true, "significance": "requirement value differs"}`
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	qMemo := "Sophia shall receive Thirty-Five Percent (35%)"
	qTrust := "thirty percent (30%) to Sophia"
	raw := []figureHit{
		fh("35%", qMemo, devMemoTitle, "Sophia", "sophia residuary share percentage"),
		fh("30%", qTrust, devTrustTitle, "Sophia", "sophia residuary share percentage"),
	}
	fs, sigs := o.deviationNumericJoin("task-dev", raw, devTestCorpus(), prov, "test-model")
	if len(fs) != 1 {
		t.Fatalf("want 1 mechanical deviation, got %d: %v", len(fs), fs)
	}
	f := fs[0]
	if !strings.Contains(f.Content, qMemo) || !strings.Contains(f.Content, qTrust) {
		t.Errorf("numeric join must carry BOTH verbatim quotes: %s", f.Content)
	}
	if !strings.Contains(f.Content, devMemoTitle) || !strings.Contains(f.Content, devTrustTitle) {
		t.Errorf("numeric join must name both documents: %s", f.Content)
	}
	if len(f.Citations) != 2 {
		t.Fatalf("want 2 citations, got %d", len(f.Citations))
	}
	for _, c := range f.Citations {
		if !c.MechanicallyVerified {
			t.Errorf("citation %q not mechanically verified", c.Quote)
		}
	}
	if f.AgentID != "deviation-detector" {
		t.Errorf("numeric-join findings must ride the deviations section (AgentID), got %q", f.AgentID)
	}
	if len(sigs) != 1 {
		t.Errorf("numeric join must return dedup signatures, got %d", len(sigs))
	}
}

// Enumeration guard: a reviewed value the controlling source ALSO states is conformance
// restated, not a deviation.
func TestDevNumericJoinEnumerationGuard(t *testing.T) {
	o := crossDocTestOrchestrator()
	raw := []figureHit{
		fh("40%", "David shall receive Forty Percent (40%)", devMemoTitle, "David", "david residuary share percentage"),
		fh("40%", "forty percent (40%) to David", devTrustTitle, "David", "david residuary share percentage"),
	}
	if fs, _ := o.deviationNumericJoin("task-dev", raw, devTestCorpus(), nil, ""); len(fs) != 0 {
		t.Fatalf("a matching value is conformance, not deviation, got: %s", fs[0].Content)
	}
}

// The substring lock holds on the mechanical path too: a quote not verbatim in its
// document never joins.
func TestDevNumericJoinSubstringLock(t *testing.T) {
	o := crossDocTestOrchestrator()
	raw := []figureHit{
		fh("35%", "Sophia shall receive Thirty-Five Percent (35%)", devMemoTitle, "Sophia", "sophia share"),
		fh("30%", "a fabricated quote that appears nowhere", devTrustTitle, "Sophia", "sophia share"),
	}
	if fs, _ := o.deviationNumericJoin("task-dev", raw, devTestCorpus(), nil, ""); len(fs) != 0 {
		t.Fatalf("a fabricated quote must be dropped by the substring lock, got: %s", fs[0].Content)
	}
}

// Without a judge the join is conservative: the referent floor must hold (crossdoc's
// deterministic degradation), so a weakly-tied pair is not flagged.
func TestDevNumericJoinNoJudgeConservative(t *testing.T) {
	o := crossDocTestOrchestrator()
	raw := []figureHit{
		fh("35%", "Sophia shall receive Thirty-Five Percent (35%)", devMemoTitle, "Sophia", "sophia share"),
		fh("30%", "thirty percent (30%) to Sophia", devTrustTitle, "Sophia", "sophia share"),
	}
	// The two quotes share only "sophia" as a referent token (< crossDocMinSharedReferents).
	if fs, _ := o.deviationNumericJoin("task-dev", raw, devTestCorpus(), nil, ""); len(fs) != 0 {
		t.Fatalf("below the referent floor with no judge, nothing is flagged, got: %s", fs[0].Content)
	}
}

// ─── Deliverable discipline: deviations ride the writer's deviations section ──

func TestDevFindingsRideDeviationsSection(t *testing.T) {
	srv := newDevFakeServer(t, func(user string) string {
		if strings.HasPrefix(user, "REQUIREMENT:") {
			return devConflictReply("The memo requires Sophia's share to be 35% but the draft grants 30%.", "Change Sophia's share to Thirty-Five Percent (35%).")
		}
		return "{}"
	})
	defer srv.Close()
	o := crossDocTestOrchestrator()
	prov := crossDocTestProvider(t, srv.URL)

	fs := o.deviationFindings(devTestTask(), devTestCorpus(), []string{"Sophia's residuary share percentage"}, nil, prov, "test-model", nil, "")
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	task := &types.Task{ID: "task-dev", Findings: fs}
	body := o.appendDiscrepancies(task, "THE MEMO BODY")
	if !strings.Contains(body, "## Deviations Identified") {
		t.Fatal("deviation finding did not produce the deviations section")
	}
	if !strings.Contains(body, "thirty percent (30%) to Sophia") {
		t.Errorf("deviations section lost the verbatim quote:\n%s", body)
	}
}
