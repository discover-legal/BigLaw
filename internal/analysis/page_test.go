// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

// TestAnalysisToolsPageByType (owner decision, lab-r3 r4: tool outputs are bounded by type, never
// trimmed): numbers handles and table and join_discrepancies, filled past their budget, return one
// valid page of at most agent.PageBytes (whole items, whole table rows) that says truncated.
func TestAnalysisToolsPageByType(t *testing.T) {
	var fs []Finding
	for i := range 300 {
		for _, d := range []struct{ doc, v string }{{"a", fmt.Sprintf("$%d,000", 100+i)}, {"b", fmt.Sprintf("$%d,500", 600+i)}} {
			metric := fmt.Sprintf("fee number %d for Smith & Jones <LLP> %s", i, strings.Repeat("under the engagement letter ", 6))
			fs = append(fs, Finding{FID: fmt.Sprintf("F%d", len(fs)+1), Verified: true,
				Quotes: []Quote{{DocumentID: d.doc, Quote: "The " + metric + " is " + d.v + " payable by Buyer."}},
				Values: []Value{{Entity: "Buyer", Metric: metric, Value: d.v, Class: "Money"}}})
		}
	}
	rt := &agent.Runtime{Chat: model.NewFake(nil), Reg: agent.NewRegistry()}
	for _, tl := range Tools(src(fs)) {
		if err := rt.Reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	fids := make([]string, 500)
	for i := range fids {
		fids[i] = fs[i].FID
	}
	ids, _ := json.Marshal(fids)
	for _, c := range []struct{ name, in string }{
		{"numbers", `{"op":"handles","fn":null,"operands":null,"holidays":null,"fids":` + string(ids) + `,"salientTop":null}`},
		{"numbers", `{"op":"table","fn":null,"operands":null,"holidays":null,"fids":` + string(ids) + `,"salientTop":null}`},
		{"join_discrepancies", `{"fids":null,"aliases":null}`},
	} {
		res, err := rt.Invoke(context.Background(), agent.Agent{Name: "lead", Visible: []string{"numbers", "join_discrepancies"}}, agent.Call{TaskID: "t"}, c.name, json.RawMessage(c.in), agent.NewBudget(10, 1, 0), false, 0)
		var page struct {
			Truncated     bool
			TableMarkdown *string
		}
		if err != nil || len(res.Output) > agent.PageBytes || json.Unmarshal(res.Output, &page) != nil || !page.Truncated {
			t.Errorf("%s %.40s: %d bytes (page %d), truncated %v, err %v", c.name, c.in, len(res.Output), agent.PageBytes, page.Truncated, err)
		}
		if page.TableMarkdown != nil && !strings.HasSuffix(*page.TableMarkdown, "\n") {
			t.Errorf("a table page ends mid-row: …%q", (*page.TableMarkdown)[max(0, len(*page.TableMarkdown)-80):])
		}
	}
}
