// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package demo records the UI's recorded engine (ui/src/recorded/recording.json, README § UI).
// It is test-only: no production lines. TestDemoRecording ingests three of the synthetic
// proof-run matters (eval/tasks) through v2's own docs code and writes, or compares against the
// committed file, a recording whose documents, outlines, sections, chunks, search hits and quote
// verifications are all computed by v2 code. Claims and plans come from each matter's answer key
// (hand-written reference answers), because no model has run these matters: no model is called.
//
//	DEMO_RECORD=1 BIGLAW_TEST_DSN=… go test -run TestDemoRecording ./eval/demo   (make demo-record)
package demo

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

const recordingPath = "../../ui/src/recorded/recording.json"

// Provenance is shown in the UI's demo banner and stays in the file's header.
const provenance = "Recorded run, not live. No model was called to make this recording and none is called while you use it. " +
	"The documents are BigLaw's synthetic proof-run matters (fictional parties, eval/tasks). Their outlines, sections, search chunks, " +
	"the search hits in the run log and every quote's verification badge were computed by BigLaw v2's own code (internal/docs: ingest, " +
	"outline, section reader, lexical search, VerifyQuote) when the recording was made (eval/demo/record_test.go). The plan, the claims " +
	"and the memo text are the matters' answer keys (reference answers written by hand for scoring), standing in for a model's output: " +
	"no v2 agent workflow exists yet (PR 4), and no real model run on these matters was ever recorded. Steps marked 'scripted' stand " +
	"for model calls; steps marked 'computed' ran v2 code. Costs are $0 because no model was called."

type taskSpec struct {
	key, title, prompt, jurisdiction string
	keywords                         []string
	facts                            []string          // answer-key ids, in order
	sections                         []string          // memo section per fact (same length)
	sectionTitles                    map[string]string // section key → heading
}

type matterSpec struct {
	dir, client, number, title string
	tasks                      []taskSpec
}

func seq(prefix string, ids ...int) []string {
	out := make([]string, len(ids))
	for i, n := range ids {
		out[i] = fmt.Sprintf("%s%02d", prefix, n)
	}
	return out
}

func same(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

var matters = []matterSpec{
	{dir: "litigation/greystone-v-talbot-defense", client: "Talbot Fresh Foods, Inc.", number: "DEMO-LIT-1",
		title: "Greystone Packaging v. Talbot Fresh Foods (defense)", tasks: []taskSpec{
			{key: "lit-deadlines", title: "Deadlines Talbot faces", jurisdiction: "US",
				prompt:   "Compute every deadline Talbot faces in the case under the Federal Rules of Civil Procedure and the court's orders.",
				keywords: []string{"deadline", "deadlines", "due", "answer", "frcp", "rule", "calendar", "status", "report", "disclosure", "date", "dates", "holiday"},
				facts:    []string{"LIT-D1", "LIT-D2", "LIT-D3", "LIT-D4"}, sections: same("d", 4),
				sectionTitles: map[string]string{"d": "Deadlines"}},
			{key: "lit-issues", title: "Defense issues list", jurisdiction: "US",
				prompt:   "Prepare the defense issues list: every defense, limitation, damages challenge and potential counterclaim, with the contract section and figures.",
				keywords: []string{"defense", "defence", "defenses", "issues", "issue", "limitation", "limitations", "damages", "counterclaim", "setoff", "shortfall", "lost", "profits", "termination", "liability", "cap", "claim"},
				facts:    []string{"LIT-I1", "LIT-I2", "LIT-I3", "LIT-I4", "LIT-I5", "LIT-I6", "LIT-I7", "LIT-I8"},
				sections: []string{"l", "d", "d", "d", "p", "t", "t", "c"},
				sectionTitles: map[string]string{"l": "Limitations", "d": "Damages and caps", "p": "Pre-suit dispute resolution",
					"t": "Termination for cause", "c": "Counterclaim and setoff"}},
			{key: "lit-chronology", title: "Chronology", jurisdiction: "US",
				prompt:   "Build a dated chronology of every material event from contract formation to today, each entry citing its source.",
				keywords: []string{"chronology", "timeline", "events", "event", "dated", "history", "when", "sequence"},
				facts:    []string{"LIT-C1", "LIT-C2", "LIT-C3", "LIT-C4", "LIT-C5", "LIT-C6"}, sections: same("c", 6),
				sectionTitles: map[string]string{"c": "Chronology"}},
		}},
	{dir: "competition-regulatory/altamira-sorell-merger-control", client: "Altamira Health Technologies, Inc.", number: "DEMO-REG-1",
		title: "Project Lantern: acquisition of Sorell Diagnostics (merger control)", tasks: []taskSpec{
			{key: "reg-hsr", title: "HSR filing analysis", jurisdiction: "US",
				prompt:   "Analyse the US HSR Act filing: transaction value, size-of-transaction and size-of-person tests, the foreign issuer exemption, the fee, the waiting period and gun-jumping.",
				keywords: []string{"hsr", "us", "united", "states", "ftc", "filing", "fee", "waiting", "period", "size", "transaction", "value", "person", "foreign", "exemption", "gun", "jumping", "conduct", "integration"},
				facts:    seq("REG-", 1, 2, 3, 4, 5, 6, 7), sections: []string{"v", "v", "v", "v", "f", "f", "g"},
				sectionTitles: map[string]string{"v": "Is the deal reportable?", "f": "Fee and waiting period", "g": "Conduct before clearance"}},
			{key: "reg-eumr", title: "EU merger control and market shares", jurisdiction: "EU",
				prompt:   "Analyse EU Merger Regulation jurisdiction from the turnover data, the notification and standstill, and the parties' market shares.",
				keywords: []string{"eu", "eumr", "european", "commission", "merger", "regulation", "turnover", "member", "states", "notification", "standstill", "market", "share", "shares", "eea", "netherlands", "italy", "two-thirds"},
				facts:    seq("REG-", 8, 9, 10, 11, 12, 13, 14), sections: []string{"j", "j", "j", "j", "n", "m", "m"},
				sectionTitles: map[string]string{"j": "EU jurisdiction", "n": "Notification and standstill", "m": "Market shares"}},
		}},
	{dir: "ip-licensing/quillon-fernhill-license-review", client: "Fernhill Robotics, Inc.", number: "DEMO-IP-1",
		title: "Quillon patent and software license (licensee review)", tasks: []taskSpec{
			{key: "ip-deviations", title: "Draft vs signed term sheet", jurisdiction: "US",
				prompt:   "Identify every place the draft license departs from the signed term sheet, quoting both.",
				keywords: []string{"term", "sheet", "deviation", "deviations", "departs", "draft", "compare", "field", "exclusive", "territory", "royalty", "rate", "base", "improvements", "warranty", "indemnity", "assign", "assignment", "minimum", "escrow"},
				facts:    seq("IP-", 1, 2, 3, 4, 9, 11, 12, 13, 15), sections: []string{"g", "g", "r", "r", "o", "o", "o", "r", "o"},
				sectionTitles: map[string]string{"g": "Grant", "r": "Royalties", "o": "Other departures"}},
			{key: "ip-schedules", title: "Patents, open source and enforceability", jurisdiction: "US",
				prompt:   "Review Schedule A patent by patent, the open-source components in Schedule B, and the royalty term and termination rights.",
				keywords: []string{"patent", "patents", "schedule", "status", "owner", "expiry", "open", "source", "gpl", "software", "components", "royalty", "term", "expire", "termination", "enforceability"},
				facts:    seq("IP-", 5, 6, 7, 8, 10, 14), sections: []string{"e", "p", "p", "p", "e", "s"},
				sectionTitles: map[string]string{"p": "Schedule A patents", "s": "Schedule B open source", "e": "Royalty term and termination"}},
		}},
}

// Recording shapes. Everything a transport returns uses the contract shapes (DESIGN § UI contract).
type rCitation struct {
	DocumentID string  `json:"documentId"`
	Quote      string  `json:"quote"`
	Locator    string  `json:"locator"`
	SectionID  string  `json:"sectionId"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
	Match      string  `json:"match"` // the verbatim source text the ladder matched ("" when unverified)
	Note       string  `json:"note,omitempty"`
}

type rFinding struct {
	FID       string      `json:"fid"`
	IssueID   string      `json:"issueId"`
	Question  string      `json:"question"`
	Claim     string      `json:"claim"`
	Severity  string      `json:"severity"`
	Status    string      `json:"status"` // verified | quarantined
	Citations []rCitation `json:"citations"`
	AtStep    int         `json:"atStep"`
}

type rStep struct {
	Step    int    `json:"step"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Input   any    `json:"input"`
	Output  any    `json:"output"`
	Source  string `json:"source"` // computed (v2 code ran) | scripted (stands for a model call)
	At      int    `json:"at"`     // replay offset, ms
}

type rTask struct {
	Key          string     `json:"key"`
	MatterNumber string     `json:"matterNumber"`
	Title        string     `json:"title"`
	Prompt       string     `json:"prompt"`
	Keywords     []string   `json:"keywords"`
	WorkflowType string     `json:"workflowType"`
	Jurisdiction string     `json:"jurisdiction"`
	DocumentIDs  []string   `json:"documentIds"`
	Steps        []rStep    `json:"steps"`
	Findings     []rFinding `json:"findings"`
	Output       string     `json:"output"`
	DurationMs   int        `json:"durationMs"`
}

type rChunk struct {
	SectionID string `json:"sectionId"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Context   string `json:"context,omitempty"`
}

type rDocument struct {
	docs.Document
	MatterNumber string              `json:"matterNumber"`
	Outline      []docs.OutlineEntry `json:"outline"`
	SectionText  map[string]string   `json:"sectionText"`
	ChunkList    []rChunk            `json:"chunkList"`
}

type rMatter struct {
	Number      string   `json:"number"`
	Title       string   `json:"title"`
	DocumentIDs []string `json:"documentIds"`
}

type rClient struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Matters []rMatter `json:"matters"`
}

type recording struct {
	Schema     int         `json:"schema"`
	Provenance string      `json:"provenance"`
	Generator  string      `json:"generator"`
	Clients    []rClient   `json:"clients"`
	Documents  []rDocument `json:"documents"`
	Tasks      []rTask     `json:"tasks"`
}

type answerKey struct {
	AsOf  string `json:"as_of"`
	Facts []struct {
		ID, Title, Question, Category, Severity string
		Documents, Evidence                     []string
		ModelAnswer                             string `json:"model_answer"`
	} `json:"facts"`
}

// stableID is a UUID-shaped id derived from the name, so the recording is byte-stable.
func stableID(name string) string {
	h := sha256.Sum256([]byte("biglaw-demo:" + name))
	return fmt.Sprintf("%x-%x-4%x-8%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])[:36]
}

func round(f float64) float64 { return math.Round(f*1e4) / 1e4 }

var fidRe = regexp.MustCompile(`\[(F\d+)\]`)

func TestDemoRecording(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := docs.New(st)
	rec := recording{Schema: 1, Provenance: provenance, Generator: "eval/demo/record_test.go (make demo-record)", Clients: []rClient{}, Documents: []rDocument{}, Tasks: []rTask{}}
	for _, m := range matters {
		realIDs := map[string]string{} // file name → stored id
		stable := map[string]string{}  // stored id → recording id
		var scope, ids []string
		dir := filepath.Join("..", "tasks", m.dir)
		var key answerKey
		raw, err := os.ReadFile(filepath.Join(dir, "answer_key.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &key); err != nil {
			t.Fatal(err)
		}
		asOf, err := time.Parse("2006-01-02", key.AsOf)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Join(dir, "documents"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			text, err := os.ReadFile(filepath.Join(dir, "documents", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			d, err := svc.IngestText(t.Context(), e.Name(), string(text))
			if err != nil {
				t.Fatal(err)
			}
			sid := stableID(m.dir + "/" + e.Name())
			realIDs[e.Name()], stable[d.ID] = d.ID, sid
			scope, ids = append(scope, d.ID), append(ids, sid)
			full, err := svc.Get(t.Context(), []string{d.ID}, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			outline, err := svc.Outline(t.Context(), []string{d.ID}, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			rd := rDocument{Document: d, MatterNumber: m.number, Outline: outline, SectionText: map[string]string{}, ChunkList: []rChunk{}}
			// the matter's as-of date stands for the ingest time, so the recording is byte-stable
			rd.ID, rd.CreatedAt = sid, asOf
			for _, o := range outline {
				var b strings.Builder
				for off := 0; ; {
					p, err := svc.Section(t.Context(), []string{d.ID}, d.ID, o.SectionID, off, docs.MaxSectionTokens)
					if err != nil {
						t.Fatal(err)
					}
					b.WriteString(p.Text)
					if !p.Truncated {
						break
					}
					off = p.Next
				}
				rd.SectionText[o.SectionID] = b.String()
			}
			secs := docs.Parse(full.Text)
			if len(secs) != len(outline) {
				t.Fatalf("%s: Parse gives %d sections, the outline %d", e.Name(), len(secs), len(outline))
			}
			for _, c := range docs.Chunkify(full.Text, secs) {
				rd.ChunkList = append(rd.ChunkList, rChunk{SectionID: fmt.Sprintf("s%d", c.Section), Title: c.Locator, Text: c.Text, Context: c.Context})
			}
			rec.Documents = append(rec.Documents, rd)
		}
		rec.Clients = append(rec.Clients, rClient{ID: stableID("client:" + m.client), Name: m.client,
			Matters: []rMatter{{Number: m.number, Title: m.title, DocumentIDs: ids}}})
		for _, ts := range m.tasks {
			rec.Tasks = append(rec.Tasks, recordTask(t, svc, m, ts, key, scope, ids, realIDs, stable, rec.Documents))
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(rec); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DEMO_RECORD") == "1" {
		if err := os.WriteFile(recordingPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", recordingPath, buf.Len())
		return
	}
	old, err := os.ReadFile(recordingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, buf.Bytes()) {
		t.Fatalf("%s is stale: v2's docs code now computes a different recording; re-record with make demo-record and review the diff", recordingPath)
	}
}

func recordTask(t *testing.T, svc *docs.Service, m matterSpec, ts taskSpec, key answerKey, scope, ids []string,
	realIDs, stable map[string]string, all []rDocument) rTask {
	t.Helper()
	locator := map[string]string{} // recording doc id + section id → section path
	for _, d := range all {
		var path []string
		for _, o := range d.Outline {
			name := strings.TrimSpace(o.Number + " " + o.Title)
			if o.Level < len(path) {
				path = path[:o.Level]
			}
			for len(path) < o.Level {
				path = append(path, "")
			}
			path = append(path, name)
			var parts []string
			for _, p := range path {
				if p != "" {
					parts = append(parts, p)
				}
			}
			locator[d.ID+"/"+o.SectionID] = strings.Join(parts, " > ")
		}
	}
	task := rTask{Key: ts.key, MatterNumber: m.number, Title: ts.title, Prompt: ts.prompt, Keywords: ts.keywords,
		WorkflowType: "memo", Jurisdiction: ts.jurisdiction, DocumentIDs: ids, Steps: []rStep{}, Findings: []rFinding{}}
	at := 0
	add := func(tool, summary, source string, ms int, in, out any) int {
		at += ms
		task.Steps = append(task.Steps, rStep{Step: len(task.Steps) + 1, Kind: "tool_call", Tool: tool, Status: "committed",
			Summary: summary, Input: in, Output: out, Source: source, At: at})
		return len(task.Steps)
	}
	facts := map[string]int{}
	for i, f := range key.Facts {
		facts[f.ID] = i
	}
	type issue struct {
		ID       string `json:"id"`
		Question string `json:"question"`
	}
	var plan []issue
	for i, id := range ts.facts {
		f := key.Facts[facts[id]]
		if f.ID != id {
			t.Fatalf("%s: no answer-key fact %s", ts.key, id)
		}
		plan = append(plan, issue{fmt.Sprintf("I%d", i+1), f.Question})
	}
	add("ledger", fmt.Sprintf("plan recorded: %d issues", len(plan)), "scripted", 900,
		map[string]any{"op": "plan", "issues": plan}, map[string]any{"ok": true})
	for _, d := range all {
		if d.MatterNumber != m.number {
			continue
		}
		add("retrieve", fmt.Sprintf("outline of %s: %d sections", d.Name, len(d.Outline)), "computed", 350,
			map[string]any{"op": "outline", "documentId": d.ID}, map[string]any{"sections": len(d.Outline)})
	}
	for i, id := range ts.facts {
		f := key.Facts[facts[id]]
		hits, err := svc.Search(t.Context(), scope, f.Question, 3, 0)
		if err != nil {
			t.Fatal(err)
		}
		type hit struct {
			DocumentID string  `json:"documentId"`
			SectionID  string  `json:"sectionId"`
			Title      string  `json:"title"`
			Rank       float64 `json:"rank"`
		}
		out := []hit{}
		for _, h := range hits {
			out = append(out, hit{stable[h.DocumentID], h.SectionID, h.Title, round(h.Rank)})
		}
		add("retrieve", fmt.Sprintf("search %q: %d hits", f.Question, len(out)), "computed", 450,
			map[string]any{"op": "search", "query": f.Question, "k": 3}, map[string]any{"hits": out})
		fid := fmt.Sprintf("F%d", i+1)
		fd := rFinding{FID: fid, IssueID: fmt.Sprintf("I%d", i+1), Question: f.Question, Claim: f.ModelAnswer, Severity: f.Severity, Citations: []rCitation{}}
		verified := 0
		for _, q := range f.Evidence {
			c := rCitation{Quote: q, Method: "none"}
			order := append([]string{}, f.Documents...)
			for name := range realIDs {
				order = append(order, name)
			}
			for _, name := range sortedUnique(order) {
				rid, ok := realIDs[name]
				if !ok {
					continue
				}
				v, err := svc.VerifyQuote(t.Context(), scope, rid, q, nil)
				if err != nil {
					t.Fatal(err)
				}
				if c.DocumentID == "" {
					c.DocumentID = stable[rid]
				}
				if v.Verified {
					c.DocumentID, c.SectionID, c.Method, c.Confidence, c.Note = stable[rid], v.SectionID, v.Method, v.Confidence, v.Note
					c.Locator = locator[c.DocumentID+"/"+v.SectionID]
					d, _ := svc.Get(t.Context(), scope, rid)
					c.Match = d.Text[v.Start:v.End]
					verified++
					break
				}
				c.Note = v.Note
			}
			fd.Citations = append(fd.Citations, c)
		}
		fd.Status = "quarantined"
		if verified > 0 {
			fd.Status = "verified"
		}
		var quotes []map[string]any
		for _, c := range fd.Citations {
			quotes = append(quotes, map[string]any{"documentId": c.DocumentID, "sectionId": c.SectionID, "method": c.Method, "confidence": c.Confidence})
		}
		fd.AtStep = add("extract_facts", fmt.Sprintf("%s: %d of %d quotes verified (%s)", fid, verified, len(fd.Citations), fd.Status), "scripted", 1300,
			map[string]any{"question": f.Question, "issueId": fd.IssueID}, map[string]any{"findings": []string{fid}, "quotes": quotes})
		task.Findings = append(task.Findings, fd)
	}
	// The memo: one section per group, in first-appearance order, only verified findings (G3).
	var order []string
	groups := map[string][]rFinding{}
	for i, f := range task.Findings {
		g := ts.sections[i]
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], f)
	}
	var memo strings.Builder
	fmt.Fprintf(&memo, "# %s\n\n**Matter:** %s (%s)  \n**Question:** %s  \n**As of:** %s\n\n", ts.title, m.title, m.number, ts.prompt, key.AsOf)
	quarantined, methods := 0, map[string]int{}
	for _, g := range order {
		var used []string
		for _, f := range groups[g] {
			if f.Status != "verified" {
				quarantined++
				continue
			}
			used = append(used, f.FID)
		}
		fmt.Fprintf(&memo, "## %s\n\n", ts.sectionTitles[g])
		for _, f := range groups[g] {
			if f.Status == "verified" {
				fmt.Fprintf(&memo, "- %s [%s]\n", f.Claim, f.FID)
			}
		}
		memo.WriteString("\n")
		add("draft_section", fmt.Sprintf("section %q from %d findings", ts.sectionTitles[g], len(used)), "scripted", 1100,
			map[string]any{"issueIds": used}, map[string]any{"heading": ts.sectionTitles[g], "findings": used})
	}
	cites := 0
	for _, f := range task.Findings {
		for _, c := range f.Citations {
			methods[c.Method]++
			cites++
		}
	}
	memo.WriteString("## Limitations\n\n- This is a recorded demonstration. No model wrote this memo: each claim is the matter's answer-key reference answer, and the quote behind it was verified by BigLaw v2's verification ladder.\n" +
		"- No committee (prosecutor, reproducer, gap auditor) ran: critics are model calls, and this recording made none.\n- The matter is synthetic; the parties are fictional. Not legal advice.\n\n")
	fmt.Fprintf(&memo, "## Evidence Note\n\n%d findings, %d citations. Verified: %d exact, %d tolerant, %d by a machine transcript (@transcript). "+
		"Unverified and excluded from this memo: %d citations; %d findings quarantined.\n", len(task.Findings), cites, methods["exact"], methods["tolerant"],
		methods["exact@transcript"]+methods["tolerant@transcript"], methods["none"], quarantined)
	task.Output = memo.String()
	unresolved := 0
	ok := map[string]bool{}
	for _, f := range task.Findings {
		ok[f.FID] = f.Status == "verified"
	}
	for _, m := range fidRe.FindAllStringSubmatch(task.Output, -1) {
		if !ok[m[1]] {
			unresolved++
		}
	}
	if unresolved > 0 {
		t.Fatalf("%s: the memo cites %d unverified or unknown findings", ts.key, unresolved)
	}
	add("finish", "check: every [F-id] resolves to a verified finding", "computed", 600,
		map[string]any{"op": "check"}, map[string]any{"ready": true, "lint": []string{}, "quarantined": quarantined})
	add("finish", "submit accepted: the deliverable gate is open", "scripted", 700,
		map[string]any{"op": "submit"}, map[string]any{"accepted": true, "gate": "deliverable"})
	task.DurationMs = at
	return task
}

func sortedUnique(names []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
