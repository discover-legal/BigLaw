// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// secItem is page n's two long sentences (about 1.1 KB each): the bench5 SEC referral's paragraphs are
// that long, and a drafter's request carries every named finding's quotes whole.
func secItem(n, k int) string {
	return fmt.Sprintf("Item %d disclosure %c: the Division alleges that the Firm failed to disclose to its advisory clients, in its Form ADV brochure and in its client communications for the period under review, the conflict of interest described in this paragraph, number %d, which arose from the allocation of omnibus trades, the directed brokerage arrangement and the personal accounts of its supervised persons, ", n, 'a'+k, n) +
		strings.Repeat(fmt.Sprintf("and the record for item %d shows the same pattern of allocation across the Review Period and the accounts concerned, ", n), 6) + "as the examination staff found."
}

// secShaped answers a SEC-shaped run by what each request holds, billed at bench5's measured costs
// per call (SEC referral at $11: a lead turn $0.026, a sweep visit $0.014, a drafter $0.026, a
// committee cycle $0.41 over its three critics, the coverage pass $0.52 for three analyses and their
// redrafts, so an analysis about $0.15).
type secShaped struct {
	mu   sync.Mutex
	lead []model.Reply
	n    int
	reqs []model.Request
}

var itemRe = regexp.MustCompile(`Item (\d+) disclosure a`)
var fidListRe = regexp.MustCompile(`"fid":"(F\d+)"`)

func (s *secShaped) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	usd := map[string]float64{"lead": 0.026, "extract_facts": 0.014, "draft_section": 0.026, "analyze_issue": 0.15, "prosecute": 0.14, "reproduce": 0.14,
		"audit_gaps": 0.13, "meta": 0.002, "section_check": 0.005, "read_figures": 0.003}[req.Agent]
	body := fmt.Sprint(req.Messages[len(req.Messages)-1].Content)
	var r model.Reply
	switch req.Agent {
	case "lead":
		s.mu.Lock()
		if s.n >= len(s.lead) {
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: the SEC-shaped lead has no more turns", model.ErrScriptExhausted)
		}
		r = s.lead[s.n]
		s.n++
		s.mu.Unlock()
	case "extract_facts":
		var in struct{ DocumentID, SectionText string }
		jsonDecode(body, &in)
		fs := []m{}
		for _, x := range itemRe.FindAllStringSubmatch(in.SectionText, -1) {
			var n int
			fmt.Sscan(x[1], &n)
			fs = append(fs, m{"issueId": fmt.Sprintf("I%d", n%6+1), "claim": fmt.Sprintf("The Firm failed to disclose conflict %d to its clients.", n),
				"quotes": []m{{"documentId": in.DocumentID, "quote": secItem(n, 0), "locator": nil}, {"documentId": in.DocumentID, "quote": secItem(n, 1), "locator": nil}},
				"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 0.9})
		}
		r = text(m{"findings": fs, "gaps": []string{}})
	case "draft_section":
		var cites []string
		for _, x := range fidListRe.FindAllStringSubmatch(strings.ReplaceAll(body, " ", ""), -1) {
			cites = append(cites, "["+x[1]+"]")
		}
		r = section("The record shows these failures to disclose "+strings.Join(cites, " ")+".", "the failures to disclose")
	case "analyze_issue":
		r = analysed("Section 206(2) reaches negligent nondisclosure of a conflict (general knowledge, not the record).", "The Firm did not disclose the conflicts.", "The nondisclosures support a 206(2) claim.")
	case "prosecute":
		r = text(m{"verdicts": []m{}})
	case "reproduce":
		r = noAnswers
	case "audit_gaps":
		r = noGaps
	case "read_figures": // the figure reader's stand-in (figreader_test.go), at a small critic call's cost
		r, _ = readerOracle(req)
	default:
		r = metaNote
	}
	resp := &model.Response{Model: req.Model, Text: r.Text, Usage: model.Usage{InputTokens: 2000, OutputTokens: 400, CostUSD: &usd}}
	for i, c := range r.ToolCalls {
		c.ID, c.Type = fmt.Sprintf("%s-%d-%d", req.Agent, s.n, i), "function"
		resp.ToolCalls = append(resp.ToolCalls, c)
	}
	return resp, nil
}

// TestSECShapedRunAnalysesAndPlacesWithin11 (bench5 SEC: every stage budget was hit, the coverage
// pass analysed three issues and then had $0.14 for a placement redraft whose worst case is more, so
// 296 findings were disclosed and none placed): a run shaped like it, at $11 and 900 calls, the same
// plan (three RedFlag issues, two Conduct, one Requirement), 142 pages swept, a lead that searches,
// drafts and reruns the committee until the budget stops it, priced per call as the bench measured.
// The coverage pass analyses every law issue the lead left touched and still places the strongest
// left-out findings, every issue is drafted, and the run spends no more than $11.
func TestSECShapedRunAnalysesAndPlacesWithin11(t *testing.T) {
	t.Parallel()
	var docTexts []string
	n := 0
	for _, pages := range []int{60, 40, 42} {
		var b strings.Builder
		for i := 1; i <= pages; i++ {
			n++
			fmt.Fprintf(&b, "%d. Allegation %d\n%s\n%s\n\n", i, n, secItem(n, 0), secItem(n, 1))
		}
		docTexts = append(docTexts, b.String())
	}
	f := newFixture(t, docTexts...)
	search := func(q string) model.Reply {
		return calls(tc("retrieve", m{"op": "search", "query": q, "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}))
	}
	lead := []model.Reply{calls(plan(issue("I1", "What are the specific allegations against the Firm?", "RedFlag"), issue("I2", "What evidence supports each allegation?", "RedFlag"),
		issue("I3", "What compliance violations are identified?", "Conduct"), issue("I4", "What are the key defense priorities?", "Conduct"),
		issue("I5", "What quantitative findings does the analysis reveal?", "RedFlag"), issue("I6", "What is the chronology of relevant events?", "Requirement"))),
		calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}
	for i := range 30 {
		lead = append(lead, search(fmt.Sprintf("disclosure conflict allocation item %d", i)))
	}
	for range 12 { // draft every issue, review, again: the bench lead's loop
		for k := 1; k <= 6; k++ {
			lead = append(lead, calls(draft(fmt.Sprintf("I%d", k), fmt.Sprintf("F%d", k), fmt.Sprintf("F%d", k+6))))
		}
		lead = append(lead, calls(committee()))
		for i := range 4 {
			lead = append(lead, search(fmt.Sprintf("failed to disclose %d", i)))
		}
	}
	chat := &secShaped{lead: lead}
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 5}, "spec-m": {"in": 1, "out": 5}, "crit-m": {"in": 1, "out": 5}}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{BudgetCalls: 900, BudgetUSD: 11, CollapseThreshold: 0.5, CollapseAction: "strict"}
	f.e = New(f.st, f.d, chat, testRoles, prices, cfg, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	c, _ := f.e.Cost(context.Background(), task.ID)
	cov, _ := f.e.coverRow(context.Background(), task.ID)
	placed := 0
	for _, r := range chat.reqs {
		placed += btoi(r.Agent == "draft_section" && strings.Contains(fmt.Sprint(r.Messages[1].Content), "These findings never reached the memo"))
	}
	secs := ledgerSections(t, f, task.ID)
	if n := strings.Count(deref(v.Output), "Coverage:"); n > 8 {
		t.Errorf("%d Coverage lines in the deliverable", n)
	}
	if c.Summary.USD == nil || *c.Summary.USD > 11 || cov == nil || strings.Join(cov.Analysed, ",") != "I3,I4,I6" || len(cov.Missing) != 0 || placed == 0 || len(secs) != 6 ||
		!strings.HasPrefix(stop(v), "awaiting_gate") {
		cs, _ := json.Marshal(cov)
		t.Fatalf("%s: cost %+v, cover %s, %d placement redrafts, %d sections\n%s", stop(v), c.Summary, cs, placed, len(secs), f.steps(task.ID))
	}
	t.Logf("spent $%.2f in %d calls; cover %+v (%d left out); %d placement redrafts", *c.Summary.USD, c.Summary.Calls, cov.Analysed, len(cov.LeftOut), placed)
}

func ledgerSections(t *testing.T, f *fixture, id string) []Section {
	t.Helper()
	ss, err := load[Section](context.Background(), f.e, id, kSection)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
