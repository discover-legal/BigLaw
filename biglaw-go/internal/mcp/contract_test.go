// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package mcp

// Contract test for the MCP backend seam: every MCP tool is called through
// an MCP server over backend.Local (the in-process owner) and through one
// over backend.Remote (a thin client of an httptest REST server backed by the
// very same services), and the two tool results must be identical.
//
// Read-only tools run against identical state and must match byte-for-byte
// (modulo the scheduler's wall-clock queue estimate). Mutating tools run once
// per side; their results are compared after masking the per-call identity
// and time fields (a fresh UUID and timestamp are the only legitimate
// difference). Error paths must produce the same error text.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/api"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/rag"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/tools"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	seedTaskID = "11111111-1111-4111-8111-111111111111"
	gateA      = "gate-a"
	gateB      = "gate-b"
	gateC      = "gate-c"
	gateD      = "gate-d"
)

type contractFixture struct {
	orch   *orchestrator.Orchestrator
	local  *MCPServer
	remote *MCPServer
}

// newContractFixture builds one owner stack (as cmd/biglaw does, minus the
// model-backed extras) in a temp working dir, serves its REST API from an
// httptest server, and returns MCP servers over both sides of the seam.
func newContractFixture(t *testing.T) *contractFixture {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir) // the REST /plugins route loads ./adapters/external
	if err := os.MkdirAll("adapters/external", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("adapters/external/acme.json", []byte(acmePlugin), 0o644); err != nil {
		t.Fatal(err)
	}

	// Every model / embedding endpoint points at a closed port: calls fail
	// fast and deterministically, so submitted tasks settle quickly.
	for k, v := range map[string]string{
		"RUVECTOR_DATA_DIR":     filepath.Join(dir, "data"),
		"DB_BACKEND":            "memory",
		"AUTH_ENABLED":          "false",
		"AUDIT_ENABLED":         "false",
		"LOCAL_EMBEDDINGS":      "true",
		"OLLAMA_URL":            "http://127.0.0.1:1",
		"LOCAL_INFERENCE_URL":   "http://127.0.0.1:1/v1",
		"LOCAL_INFERENCE_TIERS": "all",
		"QUEUE_CONCURRENCY":     "1",
		"LPM_ENABLED":           "false",
	} {
		t.Setenv(k, v)
	}
	cfg := config.Load()

	seedState(t, cfg)

	provReg := providers.NewRegistry(cfg)
	if err := cost.Default.Init(filepath.Join(dir, "data", "costs.jsonl")); err != nil {
		t.Fatal(err)
	}
	embedC := embeddings.NewClient(cfg)
	agentReg := agents.NewRegistry(embedC, cfg.VectorDB.DataDir)
	knowledgeStore := knowledge.NewStoreWithRepo(embedC, store.NewMemoryRepo())
	tmpl := templates.NewStore()
	tmpl.Add(types.TaskTemplate{
		ID: "contract-brief", Name: "Contract brief", Description: "Contract test template",
		WorkflowType: types.WorkflowCounsel, PromptTemplate: "Advise {{client}} on {{issue}}.",
		Substitutions: map[string]string{"client": "", "issue": ""},
	})
	profileStore := auth.NewProfileStore(cfg)
	clientStore := clients.NewClientStore()
	timeStore := timekeeping.NewTimeStore()

	toolReg := tools.NewRegistry(cfg, provReg, cost.Default, rag.New(rag.NewMemStore(), embedC, nil), nil)
	orch := orchestrator.New(cfg, provReg, cost.Default, embedC, agentReg,
		memory.NewInterRound(embedC), knowledgeStore, tmpl,
		settings.NewSettingsStore(cfg, cfg.Persistence.SettingsFile),
		profileStore, clientStore, timeStore, learning.Default, toolReg, agents.ROOT_ORCHESTRATOR)
	defs := []types.AgentDefinition{agents.ROOT_ORCHESTRATOR}
	defs = append(defs, agents.ALL_AGENT_DEFINITIONS[:3]...)
	if err := orch.Init(defs); err != nil {
		t.Fatalf("orchestrator init: %v", err)
	}
	fx := &contractFixture{orch: orch}
	// Runs before TempDir removal (cleanups are LIFO): never leave a task
	// worker writing into a directory being deleted, even when a check failed.
	t.Cleanup(func() { fx.waitIdle(60 * time.Second) })

	plugins := adapters.New()
	if err := plugins.LoadDirectory("adapters/external"); err != nil {
		t.Fatal(err)
	}

	srv := api.New(cfg, orch, provReg, profileStore, clientStore, timeStore, knowledgeStore, agentReg, cost.Default, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	remote, err := backend.NewRemote(ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	fx.local = New(backend.NewLocal(orch, knowledgeStore, agentReg, plugins, timeStore))
	fx.remote = New(remote)
	return fx
}

const acmePlugin = `{
  "id": "acme-research", "name": "Acme Research", "version": "1.0.0",
  "description": "Contract-test plugin",
  "auth": {"type": "api-key", "apiKeyEnvVar": "ACME_API_KEY", "endpointEnvVar": "ACME_MCP_URL"},
  "tools": [{"name": "acme_search", "description": "Search Acme",
             "inputSchema": {"type": "object", "properties": {"q": {"type": "string"}}},
             "remoteName": "search", "requiresAuth": true}],
  "agents": [], "workflows": []
}`

// seedState writes a finished task carrying two rounds and four pending
// gates, plus two time entries, where Init restores them from.
func seedState(t *testing.T, cfg *config.Config) {
	t.Helper()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	done := now.Add(time.Hour)
	finding := types.Finding{ID: "f-1", AgentID: "agent-x", Content: "The indemnity cap is 12 months' fees.", Citations: []types.Citation{}}
	gate := func(id string) types.GateRequest {
		return types.GateRequest{ID: id, TaskID: seedTaskID, FindingID: "f-1", Finding: finding, Status: "pending", CreatedAt: now}
	}
	round := func(n int) types.RoundState {
		return types.RoundState{
			RoundID: fmt.Sprintf("r-%d", n),
			Goal: types.RoundGoal{ID: fmt.Sprintf("g-%d", n), Round: n, Phase: types.PhaseResearch,
				Description: fmt.Sprintf("round %d", n), ExpectedOutputs: []string{}},
			ActiveAgentIDs: []string{"agent-x"}, Edges: []types.CommunicationEdge{},
			Messages: []types.AgentMessage{}, Findings: []types.Finding{finding},
			Status: "complete", StartedAt: now, CompletedAt: &done,
		}
	}
	task := types.Task{
		ID: seedTaskID, Description: "Seeded review of the Acme MSA", WorkflowType: types.WorkflowReview,
		Status: types.TaskStatusComplete, CurrentPhase: types.PhaseResearch, CurrentRound: 2,
		CreatedByProfileID: auth.LocalPartner.ProfileID, MatterNumber: "M-7",
		DocumentIDs: []string{}, ActiveAgentIDs: []string{}, Rounds: []types.RoundState{round(1), round(2)},
		Findings:     []types.Finding{finding},
		PendingGates: []types.GateRequest{gate(gateA), gate(gateB), gate(gateC), gate(gateD)},
		CreatedAt:    now, UpdatedAt: done, StartedAt: &now, CompletedAt: &done,
	}
	writeJSON(t, cfg.Persistence.TasksFile, []types.Task{task})

	end := now.Add(30 * time.Minute)
	writeJSON(t, cfg.Persistence.TimeFile, []types.TimeEntry{
		{ID: "te-1", ProfileID: "local-partner", TaskID: seedTaskID, MatterNumber: "M-7", Description: "review",
			Event: "task", StartedAt: now, EndedAt: &end, DurationMs: 1_800_000, BillingUnits: 5},
		{ID: "te-2", ProfileID: "lawyer-2", TaskID: "other", MatterNumber: "M-9", Description: "draft",
			Event: "task", StartedAt: now.AddDate(0, 0, 3), EndedAt: &end, DurationMs: 600_000, BillingUnits: 2},
	})
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── Driving the MCP servers ─────────────────────────────────────────────────

type toolOutcome struct {
	Text    string
	IsError bool
}

// call sends a tools/call JSON-RPC request through the MCP server exactly as
// the stdio transport would.
func call(t *testing.T, s *MCPServer, tool string, args map[string]any) toolOutcome {
	t.Helper()
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	raw, err := json.Marshal(s.srv.HandleMessage(context.Background(), req))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("%s: decode %s: %v", tool, raw, err)
	}
	if resp.Result == nil || len(resp.Result.Content) != 1 {
		t.Fatalf("%s: unexpected JSON-RPC response %s", tool, raw)
	}
	return toolOutcome{Text: resp.Result.Content[0].Text, IsError: resp.Result.IsError}
}

// normalize parses a tool's JSON text and drops wall-clock-derived fields;
// with mask set it also blanks the per-call identity and time fields.
func normalize(t *testing.T, text string, mask bool) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("tool returned non-JSON %q: %v", text, err)
	}
	return walk(v, mask)
}

var maskedKeys = map[string]bool{
	"id": true, "createdAt": true, "updatedAt": true, "ingestedAt": true,
	"reviewedAt": true, "documentId": true, "taskId": true, "gateId": true,
}

// lifecycleKeys appear, change or vanish as a worker picks a submitted task
// up asynchronously: its progress at response time is a race, not a
// contract. Dropped in masked comparisons (statuses are compared separately
// once tasks settle).
var lifecycleKeys = map[string]bool{
	"status": true, "currentPhase": true, "currentRound": true, "error": true,
	"startedAt": true, "completedAt": true, "activeTimeEntryId": true,
}

func walk(v any, mask bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k == "queue" { // scheduler estimate, recomputed from time.Now on every read
				continue
			}
			if mask && lifecycleKeys[k] {
				continue
			}
			if mask && maskedKeys[k] {
				out[k] = "<masked>"
				continue
			}
			out[k] = walk(val, mask)
		}
		return out
	case []any:
		for i := range x {
			x[i] = walk(x[i], mask)
		}
		return x
	}
	return v
}

// same calls tool on both servers and requires identical outcomes.
func (f *contractFixture) same(t *testing.T, tool string, args map[string]any) toolOutcome {
	t.Helper()
	l := call(t, f.local, tool, args)
	r := call(t, f.remote, tool, args)
	if l.IsError != r.IsError {
		t.Fatalf("%s %v: isError local=%v remote=%v\nlocal:  %s\nremote: %s", tool, args, l.IsError, r.IsError, l.Text, r.Text)
	}
	if l.IsError {
		if l.Text != r.Text {
			t.Fatalf("%s %v: error text differs\nlocal:  %s\nremote: %s", tool, args, l.Text, r.Text)
		}
		return l
	}
	lv, rv := normalize(t, l.Text, false), normalize(t, r.Text, false)
	if !reflect.DeepEqual(lv, rv) {
		t.Fatalf("%s %v: results differ\nlocal:  %s\nremote: %s", tool, args, l.Text, r.Text)
	}
	return l
}

// both runs a mutating tool once per side (argsL / argsR may differ, e.g. in
// which gate they act on) and compares the results with identity masked.
func (f *contractFixture) both(t *testing.T, tool string, argsL, argsR map[string]any) (toolOutcome, toolOutcome) {
	t.Helper()
	l := call(t, f.local, tool, argsL)
	r := call(t, f.remote, tool, argsR)
	if l.IsError || r.IsError {
		t.Fatalf("%s: unexpected error\nlocal:  %s\nremote: %s", tool, l.Text, r.Text)
	}
	if lv, rv := normalize(t, l.Text, true), normalize(t, r.Text, true); !reflect.DeepEqual(lv, rv) {
		t.Fatalf("%s: masked results differ\nlocal:  %s\nremote: %s", tool, l.Text, r.Text)
	}
	return l, r
}

func statusOf(t *testing.T, text string) string {
	t.Helper()
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v.Status
}

func idOf(t *testing.T, text string) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &v); err != nil || v.ID == "" {
		t.Fatalf("no id in %s", text)
	}
	return v.ID
}

// waitIdle waits until no task is queued or running (tasks fail fast: no
// model is reachable), reporting whether that happened within d.
func (f *contractFixture) waitIdle(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		busy := false
		for _, task := range f.orch.ListTasks() {
			switch task.Status {
			case types.TaskStatusQueued, types.TaskStatusPending, types.TaskStatusRunning:
				busy = true
			}
		}
		if !busy {
			time.Sleep(200 * time.Millisecond) // let trailing audit writes land
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// settle makes the read-only comparisons see a quiescent owner.
func (f *contractFixture) settle(t *testing.T) {
	t.Helper()
	if !f.waitIdle(60 * time.Second) {
		t.Fatal("submitted tasks did not settle within 60s")
	}
}

// ─── The contract ────────────────────────────────────────────────────────────

func TestMCPContractLocalVsRemote(t *testing.T) {
	f := newContractFixture(t)

	// Every registered tool must be exercised below.
	covered := map[string]bool{}
	mark := func(names ...string) {
		for _, n := range names {
			covered[n] = true
		}
	}

	// ── Mutations: one per side, identity masked ──────────────────────────
	tl, tr := f.both(t, "submit_task", map[string]any{
		"description": "Review the indemnity cap in the Acme MSA", "workflowType": "review",
		"jurisdiction": "us-ny", "matterNumber": "M-7",
	}, map[string]any{
		"description": "Review the indemnity cap in the Acme MSA", "workflowType": "review",
		"jurisdiction": "us-ny", "matterNumber": "M-7",
	})
	fl, fr := f.both(t, "submit_from_template",
		map[string]any{"templateId": "contract-brief", "substitutions": `{"client":"Acme","issue":"HSR"}`},
		map[string]any{"templateId": "contract-brief", "substitutions": `{"client":"Acme","issue":"HSR"}`})
	dl, dr := f.both(t, "ingest_document",
		map[string]any{"title": "Acme MSA", "content": "Clause 9.2: liability is capped at twelve months' fees."},
		map[string]any{"title": "Acme MSA", "content": "Clause 9.2: liability is capped at twelve months' fees."})
	f.both(t, "approve_gate",
		map[string]any{"taskId": seedTaskID, "gateId": gateA, "note": "verified"},
		map[string]any{"taskId": seedTaskID, "gateId": gateB, "note": "verified"})
	f.both(t, "reject_gate",
		map[string]any{"taskId": seedTaskID, "gateId": gateC, "reason": "unsupported"},
		map[string]any{"taskId": seedTaskID, "gateId": gateD, "reason": "unsupported"})
	mark("submit_task", "submit_from_template", "ingest_document", "approve_gate", "reject_gate")

	f.settle(t)

	// What each side created is now visible to the other: one store.
	for _, id := range []string{idOf(t, tl.Text), idOf(t, tr.Text), idOf(t, fl.Text), idOf(t, fr.Text)} {
		f.same(t, "get_task", map[string]any{"taskId": id})
	}
	// Tasks created through either side ran the same course.
	a := call(t, f.local, "get_task", map[string]any{"taskId": idOf(t, tl.Text)})
	b := call(t, f.local, "get_task", map[string]any{"taskId": idOf(t, tr.Text)})
	if !reflect.DeepEqual(normalize(t, a.Text, true), normalize(t, b.Text, true)) || statusOf(t, a.Text) != statusOf(t, b.Text) {
		t.Fatalf("local- and remote-submitted tasks diverged\nlocal:  %s\nremote: %s", a.Text, b.Text)
	}

	// ── Read-only tools over identical state: exact match ─────────────────
	f.same(t, "get_task", map[string]any{"taskId": seedTaskID})
	f.same(t, "list_tasks", nil)
	f.same(t, "get_round", map[string]any{"taskId": seedTaskID, "round": 2})
	f.same(t, "list_templates", nil)
	f.same(t, "list_agents", nil)
	f.same(t, "list_plugins", nil)
	f.same(t, "query_memory", nil)
	f.same(t, "get_audit", map[string]any{"limit": 20})
	f.same(t, "get_audit", map[string]any{"taskId": seedTaskID})
	f.same(t, "get_time_entries", nil)
	f.same(t, "get_time_entries", map[string]any{"matterNumber": "M-7"})
	f.same(t, "get_time_entries", map[string]any{"from": "2026-10-02", "to": "2026-12-31T00:00:00Z"})
	f.same(t, "search_knowledge", map[string]any{"query": "liability cap", "topK": 3})
	mark("get_task", "list_tasks", "get_round", "list_templates", "list_agents", "list_plugins",
		"query_memory", "get_audit", "get_time_entries", "search_knowledge")

	// Sanity: the state really is shared and non-trivial.
	if got := call(t, f.remote, "search_knowledge", map[string]any{"query": "liability"}); !strings.Contains(got.Text, idOf(t, dl.Text)) || !strings.Contains(got.Text, idOf(t, dr.Text)) {
		t.Fatalf("remote search misses a document ingested through a backend: %s", got.Text)
	}
	if got := call(t, f.remote, "list_plugins", nil); !strings.Contains(got.Text, `"version":"1.0.0"`) {
		t.Fatalf("list_plugins lost descriptor fields: %s", got.Text)
	}
	if got := call(t, f.remote, "get_task", map[string]any{"taskId": seedTaskID}); !strings.Contains(got.Text, `"status":"approved"`) || !strings.Contains(got.Text, `"status":"rejected"`) {
		t.Fatalf("gate decisions not visible: %s", got.Text)
	}

	// ── Error paths: same error text either side ──────────────────────────
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_task", map[string]any{"taskId": "no-such-task"}},
		{"get_task", map[string]any{}},
		{"get_round", map[string]any{"taskId": seedTaskID, "round": 9}},
		{"get_round", map[string]any{"taskId": "no-such-task", "round": 1}},
		{"approve_gate", map[string]any{"taskId": seedTaskID, "gateId": gateA}}, // already decided
		{"reject_gate", map[string]any{"taskId": seedTaskID, "gateId": "nope", "reason": "x"}},
		{"submit_task", map[string]any{"description": "x", "workflowType": "no-such-flow"}},
		{"submit_from_template", map[string]any{"templateId": "no-such-template"}},
		{"submit_from_template", map[string]any{"templateId": "contract-brief", "documentIds": "not json"}},
		{"get_time_entries", map[string]any{"from": "last tuesday"}},
		{"search_knowledge", map[string]any{}},
	} {
		if out := f.same(t, c.tool, c.args); !out.IsError {
			t.Fatalf("%s %v: expected an error, got %s", c.tool, c.args, out.Text)
		}
	}

	for _, name := range listToolNames(t, f.local) {
		if !covered[name] {
			t.Errorf("tool %q is not covered by the contract test", name)
		}
	}
}

func listToolNames(t *testing.T, s *MCPServer) []string {
	t.Helper()
	raw, _ := json.Marshal(s.srv.HandleMessage(context.Background(),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)))
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Result.Tools) == 0 {
		t.Fatalf("tools/list: %s (%v)", raw, err)
	}
	names := make([]string, len(resp.Result.Tools))
	for i, tool := range resp.Result.Tools {
		names[i] = tool.Name
	}
	return names
}

// The Remote backend forwards the bearer credential the REST API's auth
// middleware requires when AUTH_ENABLED=true, and the MCP surfaces a clear
// error when the backend refuses it.
func TestRemoteForwardsBearerUnderAuth(t *testing.T) {
	dir := t.TempDir()
	key := strings.Repeat("k", 40)
	for k, v := range map[string]string{
		"RUVECTOR_DATA_DIR": dir, "DB_BACKEND": "memory", "AUDIT_ENABLED": "false",
		"AUTH_ENABLED": "true", "SESSION_SECRET": strings.Repeat("s", 40),
		"API_KEY": key, "API_PROFILE_ID": "mcp-bot",
		"LOCAL_EMBEDDINGS": "true", "OLLAMA_URL": "http://127.0.0.1:1",
	} {
		t.Setenv(k, v)
	}
	cfg := config.Load()
	profiles := auth.NewProfileStore(cfg)
	if err := profiles.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.Create(auth.CreateProfileInput{Name: "MCP bot", Email: "bot@example.test", Role: "partner"}); err != nil {
		t.Fatal(err)
	}
	// CreateProfileInput assigns its own ID; bind API_PROFILE_ID to it.
	var botID string
	for _, p := range profiles.List() {
		if p.Email == "bot@example.test" {
			botID = p.ID
		}
	}
	cfg.API.ProfileID = botID

	embedC := embeddings.NewClient(cfg)
	agentReg := agents.NewRegistry(embedC, dir)
	_ = agentReg.RegisterAll(agents.ALL_AGENT_DEFINITIONS[:2])
	orch := orchestrator.New(cfg, providers.NewRegistry(cfg), cost.Default, embedC, agentReg,
		memory.NewInterRound(embedC), knowledge.NewStoreWithRepo(embedC, store.NewMemoryRepo()),
		templates.NewStore(), settings.NewSettingsStore(cfg, cfg.Persistence.SettingsFile),
		profiles, clients.NewClientStore(), timekeeping.NewTimeStore(), learning.Default, nil, agents.ROOT_ORCHESTRATOR)
	srv := api.New(cfg, orch, providers.NewRegistry(cfg), profiles, clients.NewClientStore(),
		timekeeping.NewTimeStore(), knowledge.NewStoreWithRepo(embedC, store.NewMemoryRepo()), agentReg, cost.Default, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Without the token: the startup probe explains what to set.
	anon, _ := backend.NewRemote(ts.URL, "")
	if _, err := anon.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "BIG_MICHAEL_API_TOKEN") {
		t.Fatalf("anonymous ping: want an auth hint, got %v", err)
	}
	if out := call(t, New(anon), "list_agents", nil); !out.IsError || !strings.Contains(out.Text, "bearer token") {
		t.Fatalf("anonymous list_agents: %+v", out)
	}

	// With it: the backend resolves the bound profile.
	authed, _ := backend.NewRemote(ts.URL, key)
	p, err := authed.Ping(context.Background())
	if err != nil {
		t.Fatalf("authed ping: %v", err)
	}
	if p.User.ProfileID != botID || !p.AuthEnabled {
		t.Fatalf("principal = %+v", p)
	}
	if out := call(t, New(authed), "list_agents", nil); out.IsError {
		t.Fatalf("authed list_agents: %s", out.Text)
	}
}

var _ = audit.Default // the fixture relies on the process-wide audit logger
