// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Behavioral tests for the re-entrant machinery hook (reentry.go). No network, no real
// model: extraction calls go to an in-process httptest server speaking the
// OpenAI-compatible chat wire format (the crossdoc_test.go pattern), and the specifics
// re-sweep runs against a fake tool registry.

package orchestrator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Fakes ──────────────────────────────────────────────────────────────────

// reentryFakeModel serves the absorption extraction calls, dispatching on the system
// prompt: entity/relation/allegation passes (ExtractInto) and the typed-triple pass
// (ExtractTriplesInto). Unconfigured passes return an empty array. calls counts every
// model call, so a no-op boundary can assert zero.
type reentryFakeModel struct {
	mu         sync.Mutex
	calls      int64
	entityJSON string
	tripleJSON string
}

func (f *reentryFakeModel) set(entityJSON, tripleJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entityJSON, f.tripleJSON = entityJSON, tripleJSON
}

func newReentryFakeServer(t *testing.T, f *reentryFakeModel) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt64(&f.calls, 1)
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
		var all string
		for _, m := range body.Messages {
			var s string
			_ = json.Unmarshal(m.Content, &s)
			all += s + "\n"
		}
		f.mu.Lock()
		entityJSON, tripleJSON := f.entityJSON, f.tripleJSON
		f.mu.Unlock()

		reply := "[]"
		switch {
		case strings.Contains(all, "extract entities"):
			if entityJSON != "" {
				reply = entityJSON
			}
		case strings.Contains(all, "Extract RDF triples"):
			if tripleJSON != "" {
				reply = tripleJSON
			}
		case strings.Contains(all, "genuine inconsistency"):
			reply = `{"contradiction": true, "significance": "The record states this differently across documents."}`
		case strings.Contains(all, "same underlying entity"):
			reply = `{"same": false}`
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 40, "completion_tokens": 20},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// reentryFakeTools records every extract_specifics topic and replies with canned rows.
type reentryFakeTools struct {
	mu     sync.Mutex
	topics []string
	rows   []map[string]interface{}
}

func (f *reentryFakeTools) SchemasFor([]string) []providers.ToolParam { return nil }

func (f *reentryFakeTools) Execute(name string, input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
	if name != "extract_specifics" {
		return nil, fmt.Errorf("unexpected tool %q", name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	topic, _ := input["topic"].(string)
	f.topics = append(f.topics, topic)
	return map[string]interface{}{"results": f.rows}, nil
}

func (f *reentryFakeTools) topicCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.topics)
}

func (f *reentryFakeTools) topicBlob() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.ToLower(strings.Join(f.topics, "\n"))
}

func reentryTestOrchestrator(tools agents.ToolRegistry) *Orchestrator {
	return &Orchestrator{
		cfg:     &config.Config{ReentrantMachinery: true},
		costs:   &cost.Store{},
		egraphs: map[string]*evidencegraph.Graph{},
		reentry: map[string]*reentryState{},
		tools:   tools,
	}
}

func agentFinding(content string, round int) types.Finding {
	return types.Finding{ID: content[:8], AgentID: "securities-analyst", AgentName: "Securities Analyst", Content: content, Round: round}
}

func countByAgent(fs []types.Finding, agentID string) int {
	n := 0
	for _, f := range fs {
		if f.AgentID == agentID {
			n++
		}
	}
	return n
}

// ─── Delta detection + targeted re-sweep ───────────────────────────────────

const reentryRoundContent = "Bayshore Palms LLC received payments totaling $103,800 from the omnibus account. Crescent Bay Partners was harmed by the scheme."

const reentryEntityJSON = `[
 {"entity":"Bayshore Palms LLC","attribute":"received payments totaling $103,800","value":"$103,800","quote":"Bayshore Palms LLC received payments totaling $103,800"},
 {"entity":"Crescent Bay Partners","attribute":"was harmed by the scheme","value":"","quote":"Crescent Bay Partners was harmed by the scheme"}
]`

// A round that discovers two new entities: both are identified in the delta and the
// targeted re-sweep fires entity-named queries through the round-0 sweep machinery.
func TestReentry_DeltaDetectsNewEntitiesAndResweeps(t *testing.T) {
	fm := &reentryFakeModel{}
	fm.set(reentryEntityJSON, "")
	srv := newReentryFakeServer(t, fm)
	defer srv.Close()

	tools := &reentryFakeTools{rows: []map[string]interface{}{
		{"snippet": "Bayshore Palms LLC account 801-74892 opened May 2022", "title": "Exhibit D"},
	}}
	o := reentryTestOrchestrator(tools)
	prov := crossDocTestProvider(t, srv.URL)

	task := &types.Task{ID: "t-delta", CurrentRound: 2, Findings: []types.Finding{agentFinding(reentryRoundContent, 2)}}
	snap := o.snapshot(task)
	o.runMachineryReentry(task, snap, snap.Findings, prov, "test-model", "test-model")

	// Both new entities were identified and targeted (2 queries each).
	if got := tools.topicCount(); got != 4 {
		t.Fatalf("want 4 targeted queries (2 per new entity), got %d: %v", got, tools.topics)
	}
	blob := tools.topicBlob()
	for _, e := range []string{"bayshore palms llc", "crescent bay partners"} {
		if !strings.Contains(blob, e) {
			t.Errorf("re-sweep queries missing new entity %q:\n%s", e, blob)
		}
	}
	// The grounded snippet entered the pool exactly once (4 queries returned the same
	// row; the finding-key dedup collapses it).
	if n := countByAgent(task.Findings, reentryResweepAgentID); n != 1 {
		t.Errorf("want exactly 1 re-sweep finding, got %d", n)
	}
	for _, f := range task.Findings {
		if f.AgentID == reentryResweepAgentID && f.Round != 2 {
			t.Errorf("re-sweep finding carries round %d, want 2", f.Round)
		}
	}
	// The baseline advanced: the same entities do not re-trigger next boundary.
	st := o.reentryStateFor(task.ID)
	for _, e := range []string{"bayshore palms llc", "crescent bay partners"} {
		if !st.prevEntities[e] {
			t.Errorf("baseline not advanced for %q", e)
		}
	}
}

// A duplicate of an existing pool finding does not re-enter through the re-sweep.
func TestReentry_ResweepDedupesAgainstPool(t *testing.T) {
	fm := &reentryFakeModel{}
	fm.set(reentryEntityJSON, "")
	srv := newReentryFakeServer(t, fm)
	defer srv.Close()

	existing := "Bayshore Palms LLC account 801-74892 opened May 2022"
	tools := &reentryFakeTools{rows: []map[string]interface{}{
		{"snippet": existing, "title": "Exhibit D"},
	}}
	o := reentryTestOrchestrator(tools)
	prov := crossDocTestProvider(t, srv.URL)

	task := &types.Task{ID: "t-dedup", CurrentRound: 2, Findings: []types.Finding{
		{ID: "f0", AgentID: "specifics-sweep", AgentName: "Specifics Sweep", Content: existing, Round: 0},
		agentFinding(reentryRoundContent, 2),
	}}
	snap := o.snapshot(task)
	o.runMachineryReentry(task, snap, snap.Findings[1:], prov, "test-model", "test-model")

	if tools.topicCount() == 0 {
		t.Fatal("re-sweep did not fire despite a new entity")
	}
	if n := countByAgent(task.Findings, reentryResweepAgentID); n != 0 {
		t.Errorf("a snippet already in the pool re-entered %d time(s)", n)
	}
}

// ─── No-change round: strict no-op ──────────────────────────────────────────

func TestReentry_NoChangeRoundIsNoOp(t *testing.T) {
	fm := &reentryFakeModel{}
	srv := newReentryFakeServer(t, fm)
	defer srv.Close()
	tools := &reentryFakeTools{}
	o := reentryTestOrchestrator(tools)
	// provReg deliberately nil: the no-op paths must return before provider resolution.

	task := &types.Task{ID: "t-noop", CurrentRound: 2, Findings: []types.Finding{agentFinding("prior round content here", 1)}}

	// Case 1: the round added nothing.
	o.machineryReentry(task, len(task.Findings))

	// Case 2: the round added only machinery findings (no agent discoveries).
	task.Findings = append(task.Findings, types.Finding{ID: "m1", AgentID: "figure-harvest", Content: "Total: 4,312"})
	o.machineryReentry(task, len(task.Findings)-1)

	if n := atomic.LoadInt64(&fm.calls); n != 0 {
		t.Errorf("no-change boundary made %d model calls, want 0", n)
	}
	if n := tools.topicCount(); n != 0 {
		t.Errorf("no-change boundary ran %d tool queries, want 0", n)
	}
	if len(task.Findings) != 2 {
		t.Errorf("no-change boundary mutated the pool: %d findings", len(task.Findings))
	}
}

// ─── Cross-document re-join dedupe ──────────────────────────────────────────

// Two invocations of the re-join over the same corpus emit each discrepancy exactly once.
func TestReentry_RejoinDedupes(t *testing.T) {
	hits := []figureHit{
		fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "Exhibit C — Trade Analysis", "", "omnibus trade count"),
	}
	o := reentryTestOrchestrator(&reentryFakeTools{})
	st := o.reentryStateFor("t-rejoin")
	st.rawFigs = hits
	docs := crossDocCorpus(hits)

	first := o.rejoinCrossDoc("t-rejoin", st, docs, nil, nil, "")
	if len(first) != 1 {
		t.Fatalf("first re-join: want 1 discrepancy, got %d", len(first))
	}
	if second := o.rejoinCrossDoc("t-rejoin", st, docs, nil, nil, ""); len(second) != 0 {
		t.Errorf("second re-join re-emitted %d discrepancies: %s", len(second), second[0].Content)
	}
}

// A discrepancy the ROUND-0 pass already emitted is seeded into the dedup set at init,
// so the first boundary re-join does not duplicate it either.
func TestReentry_RejoinDedupesAgainstRoundZero(t *testing.T) {
	hits := []figureHit{
		fh("4,217", qTradesReferral, "SEC Referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "Exhibit C — Trade Analysis", "", "omnibus trade count"),
	}
	o := reentryTestOrchestrator(&reentryFakeTools{})
	docs := crossDocCorpus(hits)
	roundZero := o.crossDocFindings("t-r0", hits, docs, nil, nil, "")
	if len(roundZero) != 1 {
		t.Fatalf("fixture: want 1 round-0 discrepancy, got %d", len(roundZero))
	}
	task := &types.Task{ID: "t-r0", Findings: roundZero}
	o.initReentryState(task)
	st := o.reentryStateFor(task.ID)
	st.rawFigs = hits

	if fs := o.rejoinCrossDoc(task.ID, st, docs, nil, nil, ""); len(fs) != 0 {
		t.Errorf("re-join duplicated a round-0 discrepancy: %s", fs[0].Content)
	}
}

// ─── Defense-lens re-derivation at the round boundary ───────────────────────

const reentryConductContent = "The Division alleges the trade allocation scheme violated Section 206(1) of the Advisers Act."

const reentryTripleJSON = `[
 {"s":"Trade Allocation Scheme","sClass":"Conduct","p":"violates","o":"Section 206(1)","oClass":"Authority","value":"","quote":"trade allocation scheme violated Section 206(1)"}
]`

// A conduct claim absorbed in round 2 produces its defense issue at the round-2
// boundary — not only at round 0 — and an already-emitted issue never duplicates.
func TestReentry_LensFiresAtRoundBoundaryOnce(t *testing.T) {
	fm := &reentryFakeModel{}
	srv := newReentryFakeServer(t, fm)
	defer srv.Close()
	tools := &reentryFakeTools{}
	o := reentryTestOrchestrator(tools)
	prov := crossDocTestProvider(t, srv.URL)

	task := &types.Task{ID: "t-lens", CurrentRound: 0}
	o.initReentryState(task) // round-0 baseline: no charges anywhere, no issues derivable

	// Round 2 discovers the charged conduct.
	fm.set("", reentryTripleJSON)
	task.CurrentRound = 2
	f2 := agentFinding(reentryConductContent, 2)
	task.Findings = append(task.Findings, f2)
	snap := o.snapshot(task)
	o.runMachineryReentry(task, snap, []types.Finding{f2}, prov, "test-model", "test-model")

	var scienter []types.Finding
	for _, f := range task.Findings {
		if f.AgentID == defenseLensAgentID && strings.Contains(strings.ToLower(f.Content), "scienter") {
			scienter = append(scienter, f)
		}
	}
	if len(scienter) != 1 {
		t.Fatalf("want the 206(1) scienter lens exactly once at the round-2 boundary, got %d", len(scienter))
	}
	if scienter[0].Round != 2 {
		t.Errorf("lens finding carries round %d, want 2", scienter[0].Round)
	}

	// Round 3 discovers something unrelated: the same lenses must not re-emit.
	fm.set(reentryEntityJSON, "")
	task.CurrentRound = 3
	f3 := agentFinding(reentryRoundContent, 3)
	task.Findings = append(task.Findings, f3)
	snap = o.snapshot(task)
	before := countByAgent(task.Findings, defenseLensAgentID)
	o.runMachineryReentry(task, snap, []types.Finding{f3}, prov, "test-model", "test-model")
	if after := countByAgent(task.Findings, defenseLensAgentID); after != before {
		t.Errorf("already-emitted defense issues duplicated: %d → %d lens findings", before, after)
	}
}

// ─── Kill switch ─────────────────────────────────────────────────────────────

func TestReentry_KillSwitch(t *testing.T) {
	fm := &reentryFakeModel{}
	srv := newReentryFakeServer(t, fm)
	defer srv.Close()
	tools := &reentryFakeTools{rows: []map[string]interface{}{
		{"snippet": "should never be retrieved", "title": "Exhibit"},
	}}
	o := reentryTestOrchestrator(tools)
	o.cfg.ReentrantMachinery = false

	task := &types.Task{ID: "t-kill", CurrentRound: 2, Findings: []types.Finding{agentFinding(reentryRoundContent, 2)}}
	o.initReentryState(task)
	o.machineryReentry(task, 0)

	if n := atomic.LoadInt64(&fm.calls); n != 0 {
		t.Errorf("kill switch off but %d model calls were made", n)
	}
	if n := tools.topicCount(); n != 0 {
		t.Errorf("kill switch off but %d tool queries ran", n)
	}
	if len(task.Findings) != 1 {
		t.Errorf("kill switch off but the pool changed: %d findings", len(task.Findings))
	}
	o.reentryMu.Lock()
	states := len(o.reentry)
	o.reentryMu.Unlock()
	if states != 0 {
		t.Errorf("kill switch off but %d re-entry state(s) were created", states)
	}
}
