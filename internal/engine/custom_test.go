// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

const testSecret = "s3cret-VALUE-never-shown"

// fakeMCP is a Streamable HTTP MCP server for tests: initialize (a session id), the initialized
// notification, tools/list (one convertible tool, one not) and tools/call (answered as an SSE
// stream, echoing its arguments). It records what it was sent and refuses a wrong secret.
type fakeMCP struct {
	*httptest.Server
	mu    sync.Mutex
	calls []string          // methods, in order
	args  []json.RawMessage // tools/call arguments
}

func newFakeMCP(t *testing.T) *fakeMCP {
	f := &fakeMCP{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID     any
			Method string
			Params json.RawMessage
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		f.mu.Unlock()
		res := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			res(map[string]any{"protocolVersion": mcpProtocol, "serverInfo": map[string]string{"name": "Fake Court Records"}, "capabilities": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if r.Header.Get("Mcp-Session-Id") != "sess-1" {
				w.WriteHeader(400)
				return
			}
			res(map[string]any{"tools": []map[string]any{
				{"name": "search", "description": "Searches court records by party name", "inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{"party": map[string]any{"type": "string", "minLength": 1}, "court": map[string]any{"type": "string", "format": "uri"}}, "required": []string{"party"}}},
				{"name": "lookup", "description": "Looks up.", "inputSchema": map[string]any{"anyOf": []any{map[string]any{"type": "string"}}}},
			}})
		case "tools/call":
			var p struct{ Arguments json.RawMessage }
			json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			f.args = append(f.args, p.Arguments)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": "Docket 1:26-cv-09184 found for " + string(p.Arguments)}}}})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMCP) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += btoi(c == method)
	}
	return n
}

// syncBuf is a log sink tests read (the engine logs from several goroutines).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// remoteEngine is f's engine over scripts, logging to logs, allowed to reach srv (a local TLS server).
func remoteEngine(f *fixture, chat model.Chatter, srv *httptest.Server, logs io.Writer) *Engine {
	f.e = New(f.st, f.d, chat, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.NewTextHandler(logs, nil)))
	if srv != nil {
		f.e.SetRemotePolicy(RemotePolicy{AllowLoopback: true, RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	}
	return f.e
}

func save(t *testing.T, e *Engine, id string, base int, files map[string]string) {
	t.Helper()
	if _, _, err := e.Save(context.Background(), id, SaveIn{Base: base, Files: files}, "Ada"); err != nil {
		t.Fatalf("save %s: %s", id, problems(err))
	}
}

func serverFile(name, url string) map[string]string {
	b, _ := json.Marshal(Server{Name: name, URL: url, Enabled: true, Auth: Auth{Secret: "court_key", Header: "Authorization", Prefix: "Bearer "}})
	return map[string]string{serversDir + name + ".json": string(b)}
}

// TestRemoteRefusesNonPublicTargets (SSRF): a saved URL must be https with no user info and no
// non-public host; every connection is checked at dial time (a name that resolves to a private
// address included); redirects stay on the host; an answer over 1 MB is refused.
func TestRemoteRefusesNonPublicTargets(t *testing.T) {
	t.Parallel()
	c := newRemoteClient(RemotePolicy{})
	for _, u := range []string{"http://example.com/mcp", "https://user:pw@example.com/", "https://localhost/", "https://api.localhost/", "https://127.0.0.1/",
		"https://10.1.2.3/", "https://172.16.0.1/", "https://192.168.1.1/", "https://169.254.169.254/latest/meta-data/", "https://[::1]/", "https://[fd00::1]/",
		"https://[::ffff:10.0.0.1]/", "https://100.64.0.1/", "https://0.0.0.0/", "https://metadata.google.internal/", "ftp://example.com/", "/relative", "https://example.com/#frag"} {
		if err := c.checkURL(u); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
	for _, u := range []string{"https://example.com/mcp", "https://8.8.8.8:8443/x?q=1"} {
		if err := c.checkURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, a := range []string{"127.0.0.1:443", "10.0.0.5:443", "[fe80::1]:443", "169.254.169.254:80", "[::ffff:192.168.0.1]:443", "100.100.100.200:80"} {
		if err := c.dialCheck(a); !errors.Is(err, errRemoteAddr) {
			t.Errorf("dial %s: %v", a, err)
		}
	}
	if err := c.dialCheck("93.184.216.34:443"); err != nil {
		t.Errorf("a public address: %v", err)
	}
	if c.http.Timeout != remoteTimeout || remoteTimeout.Seconds() != 30 || c.http.Transport.(*http.Transport).Proxy != nil {
		t.Error("the client's timeout is not 30 s, or it uses a proxy (which would connect past the address check)")
	}
	if (&remoteClient{policy: RemotePolicy{AllowLoopback: true}}).deniedAddr(netip.MustParseAddr("10.0.0.1")) == "" {
		t.Error("the tests' loopback allowance let a private address through")
	}

	// a real connection: refused at dial time under the production policy, the redirect and size rules under the tests'
	big := strings.Repeat("x", remoteMaxBytes+1)
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "elsewhere") }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			io.WriteString(w, big)
		case "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if _, err := c.do(ctx, "GET", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)+"/ok", nil, nil, "", ""); err == nil {
		t.Error("production policy reached a loopback server")
	}
	tc := newRemoteClient(RemotePolicy{AllowLoopback: true, RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	if a, err := tc.do(ctx, "GET", srv.URL+"/here", nil, nil, "", ""); err != nil || string(a.body) != "ok" {
		t.Errorf("a same-host redirect: %v", err)
	}
	if _, err := tc.do(ctx, "GET", srv.URL+"/away", nil, nil, "Authorization", testSecret); err == nil || !strings.Contains(err.Error(), "redirect was refused") {
		t.Errorf("a redirect to another host: %v", err)
	}
	if _, err := tc.do(ctx, "GET", srv.URL+"/big", nil, nil, "", ""); err == nil || !strings.Contains(err.Error(), "over 1 MB") {
		t.Errorf("an answer over 1 MB: %v", err)
	}
}

// TestSchemaConversion: an MCP input schema becomes BigLaw's strict subset (optional fields
// nullable, descriptive keywords dropped), refused where the subset cannot say it; the example
// made from it validates.
func TestSchemaConversion(t *testing.T) {
	t.Parallel()
	in, err := convertSchema(json.RawMessage(`{"type":"object","title":"x","properties":{"q":{"type":"string","format":"uri","default":"a"},
		"n":{"type":"integer","minimum":2},"tags":{"type":"array","items":{"type":"string","enum":["a","b"]}}},"required":["q"]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"additionalProperties":false,"properties":{"n":{"minimum":2,"type":["integer","null"]},"q":{"type":["string"]},"tags":{"items":{"enum":["a","b"],"type":["string"]},"type":["array","null"]}},"required":["n","q","tags"],"type":["object"]}`
	if string(in) != want {
		t.Fatalf("converted:\n%s\nwant\n%s", in, want)
	}
	sch, _ := agent.Compile(in)
	ex, _ := json.Marshal(exampleFor(sch))
	if err := sch.Validate(ex); err != nil {
		t.Fatalf("example %s: %v", ex, err)
	}
	for _, bad := range []string{`{"anyOf":[{"type":"string"}]}`, `{"type":"object","properties":{"a":{"$ref":"#/x"}}}`, `{"type":"object"}`, `{"type":"array"}`, `[1]`} {
		if _, err := convertSchema(json.RawMessage(bad)); err == nil {
			t.Errorf("%s converted", bad)
		}
	}
	if got := dropNulls(map[string]any{"a": nil, "b": map[string]any{"c": nil, "d": 1}}); fmt.Sprint(got) != "map[b:map[d:1]]" {
		t.Errorf("dropNulls: %v", got)
	}
	if toolName("Court-Records", "search.cases v2") != "court_records_search_cases_v2" || toolName("9", "x") != "" {
		t.Error("toolName")
	}
}

// TestMCPImportAndCall: a server saved with a named secret, its tools discovered (the handshake,
// the session id, a schema it cannot import reported), one imported as a custom tool and called by
// the lead in a run that crashes right after the call commits and resumes: the call is replayed,
// never sent twice; its answer is labelled untrusted; the secret reaches the server and nothing else
// (no route, log line, error or export shows it).
func TestMCPImportAndCall(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mcp := newFakeMCP(t)
	logs := &syncBuf{}
	sc := f.happy()
	sc["lead"] = slices.Insert(sc["lead"], 1, calls(tc("courts_search", m{"party": "Greystone", "court": nil})))
	k := &killer{Fake: newFake(sc)}
	e := remoteEngine(f, k, mcp.Server, logs)
	ctx := context.Background()
	save(t, e, "server.courts", 0, serverFile("courts", mcp.URL))
	if _, err := e.Discover(ctx, "server.courts"); err == nil || strings.Contains(err.Error(), testSecret) || !strings.Contains(err.Error(), `secret "court_key" is not set`) {
		t.Fatalf("discover with its secret unset: %v", err)
	}
	if err := e.SetSecret(ctx, "court_key", testSecret, "Ada"); err != nil {
		t.Fatal(err)
	}
	d, err := e.Discover(ctx, "server.courts")
	if err != nil || d.ServerName != "Fake Court Records" || len(d.Tools) != 2 || d.Tools[1].Problem == "" || !strings.Contains(d.Tools[1].Problem, "anyOf") {
		t.Fatalf("discover: %+v %v", d, err)
	}
	res, err := e.Import(ctx, "server.courts", nil, true, "Ada")
	if err != nil || len(res) != 2 || res[0].Item != "tool.courts_search" || res[1].Problem == "" {
		t.Fatalf("import: %+v %v", res, err)
	}
	snap, _, _ := e.current(ctx)
	sp, _ := newSpecs(snap)
	if !slices.Contains(leadVisible(sp), "courts_search") {
		t.Fatal("an imported tool offered to the lead is not in its list")
	}
	task := f.submit(0)
	e.afterCommit = func(id string, n int) {
		if n == 4 { // the custom tool's step
			e.active(id).cancel()
		}
	}
	f.step(ctx)
	if st := f.steps(task.ID); !strings.HasSuffix(st, "4:tool_call:courts_search:committed") {
		t.Fatalf("first process: %s", st)
	}
	e2 := remoteEngine(f, k, mcp.Server, logs)
	f.stale(task.ID)
	f.step(ctx)
	if v := f.get(task.ID); stop(v) != "awaiting_gate/finished" {
		t.Fatalf("resumed: %s %s", stop(v), f.steps(task.ID))
	}
	if n := mcp.count("tools/call"); n != 1 {
		t.Fatalf("the remote tool was called %d times across the crash", n)
	}
	if string(mcp.args[0]) != `{"party":"Greystone"}` {
		t.Fatalf("sent %s (a null optional field must not be sent)", mcp.args[0])
	}
	out := f.outputs(task.ID, "courts_search")
	if len(out) != 1 || !strings.Contains(out[0], `"untrusted":true`) || !strings.Contains(out[0], `"source":"mcp:courts/search"`) || !strings.Contains(out[0], "Docket 1:26-cv-09184") {
		t.Fatalf("output: %v", out)
	}
	for name, s := range map[string]string{"logs": logs.String(), "steps": f.steps(task.ID)} {
		if strings.Contains(s, testSecret) {
			t.Errorf("the secret is in the %s", name)
		}
	}
	if !strings.Contains(logs.String(), "remote: call") || !strings.Contains(logs.String(), "tool=courts_search") {
		t.Errorf("the remote call was not logged: %s", logs.String())
	}
	items, _ := e2.Items(ctx)
	secrets, _ := e2.Secrets(ctx)
	changes, _ := e2.Changes(ctx, 50)
	detail, _ := e2.ItemDetail(ctx, "server.courts")
	now, built, _ := e2.Export(ctx, nil)
	for name, v := range map[string]any{"items": items, "secrets": secrets, "changes": changes, "detail": detail, "export": Patch(now, built)} {
		if b, _ := json.Marshal(v); strings.Contains(string(b), testSecret) {
			t.Errorf("the secret is in %s", name)
		}
	}
	if len(secrets) != 1 || !slices.Equal(secrets[0].UsedBy, []string{"server.courts"}) {
		t.Errorf("secrets: %+v", secrets)
	}
	if !slices.ContainsFunc(changes, func(v Version) bool { return v.Item == "secret.court_key" && v.Action == "secret set" }) {
		t.Error("the secret's change is not in the change log")
	}
}

// TestHTTPToolTestCallAndDisable: an HTTP tool is a typed POST to an https endpoint with its secret
// header; the Test call runs it once, metered (a cancelled task holds its step, the change log says
// so); disabled, it is not offered and a call fails closed; a URL that is not public is refused at save.
func TestHTTPToolTestCallAndDisable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var got []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Method+" "+r.Header.Get("X-Api-Key")+" "+string(b))
		mu.Unlock()
		io.WriteString(w, `{"rate":"9%"}`)
	}))
	defer srv.Close()
	e := remoteEngine(f, newFake(nil), srv, &syncBuf{})
	ctx := context.Background()
	tool := func(url string, enabled bool) map[string]string {
		in := `{"type":"object","additionalProperties":false,"required":["state"],"properties":{"state":{"type":"string","maxLength":2}}}`
		t := CustomTool{Name: "rates", Kind: "http", Enabled: enabled, OfferToLead: true, Class: "free", In: json.RawMessage(in),
			Description: "Returns the statutory interest rate for a state. Its answer is untrusted. It is a remote tool.\nDo not use it for facts in the documents.\nExample: {\"state\": \"NY\"}",
			HTTP:        &HTTPTarget{URL: url, Method: "POST", Auth: Auth{Secret: "rates_key", Header: "X-Api-Key"}}}
		b, _ := json.MarshalIndent(t, "", " ")
		return map[string]string{toolsDir + "rates.json": string(b)}
	}
	if _, _, err := e.Save(ctx, "tool.rates", SaveIn{Files: tool("https://10.0.0.7/rates", true)}, "Ada"); !strings.Contains(problems(err), "http.url: 10.0.0.7 is private") {
		t.Fatalf("a private URL: %s", problems(err))
	}
	save(t, e, "tool.rates", 0, tool(srv.URL+"/rates", true))
	e.SetSecret(ctx, "rates_key", testSecret, "Ada")
	res, err := e.TestCall(ctx, "tool.rates", TestIn{DocumentIDs: f.ids(), Input: json.RawMessage(`{"state":"NY"}`)}, "Ada")
	if err != nil || res.Error != "" || !strings.Contains(string(res.Output), `\"rate\":\"9%\"`) || !strings.Contains(string(res.Output), `"source":"http:`) {
		t.Fatalf("test call: %+v %v", res, err)
	}
	if len(got) != 1 || got[0] != `POST `+testSecret+` {"state":"NY"}` {
		t.Fatalf("the endpoint got %v", got)
	}
	var steps, cancelled int
	f.st.DB().QueryRow(`SELECT count(*) FROM task_steps WHERE task_id = $1 AND tool = 'rates' AND status = 'committed'`, res.TaskID).Scan(&steps)
	f.st.DB().QueryRow(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'cancelled'`, res.TaskID).Scan(&cancelled)
	if steps != 1 || cancelled != 1 {
		t.Fatalf("the test call is not metered as a step of a cancelled task (%d, %d)", steps, cancelled)
	}
	if ch, _ := e.Changes(ctx, 10); !slices.ContainsFunc(ch, func(v Version) bool { return v.Item == "tool.rates" && v.Action == "test" }) {
		t.Fatal("the test call is not in the change log")
	}
	save(t, e, "tool.rates", 1, tool(srv.URL+"/rates", false))
	snap, _, _ := e.current(ctx)
	sp, _ := newSpecs(snap)
	if slices.Contains(leadVisible(sp), "rates") {
		t.Fatal("a disabled tool is offered to the lead")
	}
	res, _ = e.TestCall(ctx, "tool.rates", TestIn{DocumentIDs: f.ids(), Input: json.RawMessage(`{"state":"NY"}`)}, "Ada")
	if !strings.Contains(res.Error, "disabled by an admin") || len(got) != 1 {
		t.Fatalf("a disabled tool's call: %+v (endpoint got %d)", res, len(got))
	}
	if _, err := e.TestCall(ctx, "pass.sweep", TestIn{DocumentIDs: f.ids(), Input: json.RawMessage(`{}`)}, "Ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a pass was tested: %v", err)
	}
}

// customAgent is a new subagent's files.
func customAgent(name string, allowed ...string) map[string]string {
	spec := map[string]any{"name": name, "kind": "subagent", "class": "evidence", "maxTurns": 2, "role": "specialist", "allowed": allowed,
		"description": "Summarises one document's section for the lead. It reads only what it is given. It answers in one short paragraph.\nDo not use it for findings: use extract_facts.\nExample: {\"documentId\": \"d1\", \"focus\": \"damages\"}",
		"in": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"documentId", "focus"},
			"properties": map[string]any{"documentId": map[string]any{"type": "string"}, "focus": map[string]any{"type": "string"}}},
		"out": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"summary"}, "properties": map[string]any{"summary": map[string]any{"type": "string"}}}}
	return map[string]string{agentsDir + name + ".json": pretty(spec), agentsDir + name + ".md": "You summarise one section for the lead, from the documents only."}
}

// TestCustomSubagentEndToEnd: an admin's new subagent is offered to the lead and runs with typed
// output enforcement and metering in a real run; the Test call runs it alone; deleting what Go
// depends on is refused with the reason, and a custom subagent's deletion is revertable.
func TestCustomSubagentEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sc := f.happy()
	sc["lead"] = slices.Insert(sc["lead"], 1, calls(tc("summarize", m{"documentId": f.docs[0].ID, "focus": "damages"})))
	sc["summarize"] = []model.Reply{text(m{"summary": "Greystone claims damages."}), text(m{"summary": "Tested."})}
	e, fake := f.engine(sc, Config{})
	ctx := context.Background()
	if _, _, err := e.Save(ctx, "agent.summarize", SaveIn{Files: customAgent("summarize", "web_search")}, "Ada"); !strings.Contains(problems(err), `allowed tool "web_search"`) {
		t.Fatalf("an unknown allowed tool: %s", problems(err))
	}
	if _, _, err := e.Save(ctx, "agent.retrieve", SaveIn{Files: customAgent("retrieve")}, "Ada"); err == nil {
		t.Fatal("a subagent named like a built-in tool was saved")
	}
	save(t, e, "agent.summarize", 0, customAgent("summarize", "retrieve"))
	items, _ := e.Items(ctx)
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "agent.summarize" })
	if i < 0 || items[i].Builtin || !slices.Contains(items[i].UsedBy, "lead") {
		t.Fatalf("catalogue: %+v", items[i])
	}
	run := f.get(f.submit(0).ID)
	f.step(ctx)
	run = f.get(run.ID)
	if stop(run) != "awaiting_gate/finished" || !strings.Contains(f.steps(run.ID), "4:tool_call:summarize:committed") {
		t.Fatalf("run: %s %s", stop(run), f.steps(run.ID))
	}
	if out := f.outputs(run.ID, "summarize"); len(out) != 1 || !strings.Contains(out[0], "Greystone claims damages.") {
		t.Fatalf("output: %v", out)
	}
	if !slices.ContainsFunc(fake.Calls(), func(r model.Request) bool { return r.Agent == "summarize" }) {
		t.Fatal("the subagent made no model call")
	}
	res, err := f.e.TestCall(ctx, "agent.summarize", TestIn{DocumentIDs: f.ids(), Input: json.RawMessage(`{"documentId":"` + f.docs[0].ID + `","focus":"x"}`)}, "Ada")
	if err != nil || res.Error != "" || res.Usage.Calls != 1 || !strings.Contains(string(res.Output), "Tested.") {
		t.Fatalf("test call: %+v %v", res, err)
	}
	for _, id := range []string{"agent.read_figures", "agent.judge_figures", "agent.draft_section", "tool.retrieve", "pass.sweep", "prompt.lead", "pack.jurisdiction.us"} {
		if _, err := f.e.Delete(ctx, id, 0, "", "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot be deleted") {
			t.Errorf("delete %s: %v", id, err)
		}
	}
	if _, err := f.e.Delete(ctx, "agent.read_figures", 0, "", "Ada"); !strings.Contains(err.Error(), "Go calls it") {
		t.Errorf("the reason: %v", err)
	}
	if v, err := f.e.Delete(ctx, "agent.summarize", 1, "", "Ada"); err != nil || !v.Deleted {
		t.Fatalf("delete the custom subagent: %+v %v", v, err)
	}
	if _, err := f.e.Revert(ctx, "agent.summarize", 1, 2, "", "Ada"); err != nil {
		t.Fatalf("restore it: %v", err)
	}
}

// TestDeleteRefusedWhileReferenced: a custom tool an agent's allowed list names, or a server a tool
// uses, cannot be deleted: the set would not load.
func TestDeleteRefusedWhileReferenced(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mcp := newFakeMCP(t)
	e := remoteEngine(f, newFake(nil), mcp.Server, &syncBuf{})
	ctx := context.Background()
	save(t, e, "server.courts", 0, serverFile("courts", mcp.URL))
	e.SetSecret(ctx, "court_key", testSecret, "Ada")
	if _, err := e.Import(ctx, "server.courts", []string{"search"}, false, "Ada"); err != nil {
		t.Fatal(err)
	}
	save(t, e, "agent.summarize", 0, customAgent("summarize", "retrieve", "courts_search"))
	if _, err := e.Delete(ctx, "tool.courts_search", 1, "", "Ada"); !strings.Contains(problems(err), "courts_search") {
		t.Fatalf("a tool an agent's allowed list names was deleted: %s", problems(err))
	}
	if _, err := e.Delete(ctx, "server.courts", 1, "", "Ada"); !strings.Contains(problems(err), "server") {
		t.Fatalf("a server a tool uses was deleted: %s", problems(err))
	}
}
