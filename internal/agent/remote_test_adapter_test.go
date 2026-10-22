// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

// remote is the adapter DESIGN § Services and boundaries sketches (agent.RemoteTool, ~60 lines
// when a tool moves out of process): it POSTs {name, call} and decodes the Result. Kept as a test
// until a tool actually moves, so the path is proven without production lines.
type remote struct {
	spec agent.Spec
	url  string
}

type wire struct {
	Name   string       `json:"name"`
	Call   agent.Call   `json:"call"`
	Result agent.Result `json:"result"`
	Error  string       `json:"error,omitempty"`
}

func (r remote) Spec() agent.Spec { return r.spec }
func (r remote) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	body, _ := json.Marshal(wire{Name: r.spec.Name, Call: c})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return agent.Result{}, &model.Error{Kind: model.ErrUnavailable, Detail: "remote tool unreachable"} // G6, never an empty result
	}
	defer resp.Body.Close()
	var w wire
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return agent.Result{}, &model.Error{Kind: model.ErrUnavailable, Detail: "remote tool answered nonsense"}
	}
	if w.Error != "" {
		return w.Result, agent.Fail(w.Error)
	}
	return w.Result, nil
}

// serve is the remote side: the same Tool, behind HTTP.
func serve(tools map[string]agent.Tool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var w wire
		_ = json.NewDecoder(req.Body).Decode(&w)
		res, err := tools[w.Name].Call(req.Context(), w.Call)
		out := wire{Result: res}
		if err != nil {
			out.Error = err.Error()
		}
		_ = json.NewEncoder(rw).Encode(out)
	}))
}

type ledger []analysis.Finding

func (l ledger) Findings(context.Context, string) ([]analysis.Finding, error) { return l, nil }

// Every Go tool gives the same result through an httptest adapter as in process, failures included,
// and G1 holds remotely: a document outside the call's scope is not found.
func TestToolsTransportAgnostic(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := docs.New(st)
	in, err := svc.IngestText(t.Context(), "msa.txt", "1. Liability\nThe Supplier's aggregate liability shall not exceed the fees paid in the twelve months.\n2. Term\nThe term is three years.")
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.IngestText(t.Context(), "other.txt", "1. Liability\nUnlimited liability for the other matter.")
	if err != nil {
		t.Fatal(err)
	}
	fs := ledger{{FID: "F1", Verified: true, Quotes: []analysis.Quote{{DocumentID: in.ID, Quote: "fees"}}, Values: []analysis.Value{{Metric: "fee", Value: "$1,200"}}},
		{FID: "F2", Verified: true, Quotes: []analysis.Quote{{DocumentID: out.ID, Quote: "fees"}}, Values: []analysis.Value{{Metric: "fee", Value: "$1,500"}}}}
	local := map[string]agent.Tool{}
	for _, tl := range append(svc.Tools(model.NewFake(nil)), analysis.Tools(fs)...) {
		local[tl.Spec().Name] = tl
	}
	srv := serve(local)
	defer srv.Close()
	cases := []struct{ tool, input string }{
		{"retrieve", `{"op":"search","query":"aggregate liability","documentId":null,"sectionId":null,"k":8,"offset":null,"limit":null}`},
		{"retrieve", `{"op":"outline","query":null,"documentId":"` + in.ID + `","sectionId":null,"k":null,"offset":null,"limit":null}`},
		{"retrieve", `{"op":"section","query":null,"documentId":"` + in.ID + `","sectionId":"s1","k":null,"offset":0,"limit":5}`},
		{"retrieve", `{"op":"outline","query":null,"documentId":"` + out.ID + `","sectionId":null,"k":null,"offset":null,"limit":null}`},
		{"verify_quote", `{"documentId":"` + in.ID + `","quote":"the  supplier's aggregate LIABILITY","claim":null}`},
		{"verify_quote", `{"documentId":"` + out.ID + `","quote":"Unlimited liability","claim":null}`},
		{"numbers", `{"op":"calc","fn":"sum","operands":[{"ref":"F1.v1","literal":null,"unit":null},{"ref":"F2.v1","literal":null,"unit":null}],"holidays":null,"fids":null,"salientTop":null}`},
		{"numbers", `{"op":"table","fn":null,"operands":null,"holidays":null,"fids":null,"salientTop":null}`},
		{"join_discrepancies", `{"fids":null,"aliases":null}`},
	}
	for _, c := range cases {
		call := agent.Call{TaskID: "t1", DocumentIDs: []string{in.ID}, Input: json.RawMessage(c.input), Budget: agent.Grant{Calls: 4, USD: 1}}
		lr, lerr := local[c.tool].Call(t.Context(), call)
		rr, rerr := remote{local[c.tool].Spec(), srv.URL}.Call(t.Context(), call)
		var lo, ro bytes.Buffer
		_ = json.Compact(&lo, lr.Output)
		_ = json.Compact(&ro, rr.Output)
		if string(lr.Output) == "" { // JSON carries a nil output as null
			lo.WriteString("null")
		}
		if lo.String() != ro.String() || lr.Usage != rr.Usage || (lerr == nil) != (rerr == nil) || lerr != nil && lerr.Error() != rerr.Error() {
			t.Errorf("%s %s:\n local  %s %v\n remote %s %v", c.tool, c.input, lo.String(), lerr, ro.String(), rerr)
		}
		var f *agent.Failure
		if out.ID != "" && bytes.Contains([]byte(c.input), []byte(out.ID)) && !errors.As(rerr, &f) {
			t.Errorf("%s reached a document outside the scope remotely: %s", c.tool, ro.String())
		}
	}
}
