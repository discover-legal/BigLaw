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
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

const recordingPath = "../../ui/src/recorded/recording.json"

// Provenance is shown in the UI ("How this demo works") and stays in the file's header.
const provenance = "Recorded run, not live. No model was called to make this recording and none is called while you use it. " +
	"The documents are BigLaw's synthetic proof-run matters (fictional parties, eval/tasks), plus one scanned exhibit drawn for the demo " +
	"(an affidavit of service in the Greystone matter). Their outlines, sections, search chunks, the search hits in the run log and on the " +
	"Documents screen's recorded searches, and every quote's verification badge were computed by BigLaw v2's own code (internal/docs: ingest, " +
	"outline, section reader, lexical search, VerifyQuote with no judge, so the judge and ensemble rungs never ran) when the recording was made " +
	"(eval/demo/record_test.go). The scan went through v2's vision path, but its reader was scripted (model.Fake): both readings are the text " +
	"the image was drawn from, so its @transcript badges show the ladder's real cap and labels, not a vision model's accuracy. The plan, the claims " +
	"and the memo text are the matters' answer keys (reference answers written by hand for scoring), standing in for a model's output: no v2 " +
	"agent workflow exists yet (PR 4), and no real model run on these matters was ever recorded. A few quotes and one finding were written for " +
	"the demo to exercise the ladder (a quote typed with curly quotes, two quotes from the scan, and a misquoted figure that fails verification " +
	"and is quarantined); the memo's Limitations name them. Steps marked 'scripted' stand for model calls; steps marked 'computed' ran v2 code. " +
	"Costs are $0 because no model was called."

// demoQuotes are quotes written for the demo (not in the answer keys) to exercise the ladder's
// other rungs; each is verified by VerifyQuote like any other. Key: answer-key fact id.
var demoQuotes = map[string][]string{
	// curly quotes where the source has straight ones: tolerant
	"LIT-D2": {"The “next day” is determined by continuing to count forward"},
	"LIT-I2": {"Buyer’s sole liability for the shortfall shall be a fee equal to thirty-five percent (35%)"},
	// the scanned affidavit of service: exact@transcript, and across a line break tolerant@transcript
	"LIT-D1": {"On October 21, 2026 at 2:14 p.m., I served the Summons"},
	"LIT-C6": {"by delivering them to Illinois Corporate Agents LLC, its registered agent"},
}

// demoFacts are findings written for the demo, appended to a matter's answer key. DEMO-D5 misquotes
// the rule's figure (the excerpt says 21 days): its quote genuinely fails VerifyQuote, so it is
// quarantined, left out of the memo and disclosed in the Evidence Note.
var demoFacts = map[string][]demoFact{
	"litigation/greystone-v-talbot-defense": {{ID: "DEMO-D5", Severity: "high",
		Question:    "How long does Talbot have to answer after service?",
		ModelAnswer: "Talbot has 20 days after service to answer, so its answer was due November 10, 2026.",
		Documents:   []string{"05-federal-calendar-and-rules-excerpt.md"},
		Evidence:    []string{"A defendant must serve an answer within 20 days after being served"}}},
}

type demoFact struct {
	ID, Question, Severity, ModelAnswer string
	Documents, Evidence                 []string
}

// The scanned exhibit: the JPEG the UI bundles, and the text it was drawn from (make_scan.py).
const (
	scanMatter = "litigation/greystone-v-talbot-defense"
	scanName   = "06-affidavit-of-service-scan.jpg"
	scanImage  = "../../ui/src/recorded/" + scanName
	scanText   = "testdata/06-affidavit-of-service-scan.txt"
	scanReader = "model.Fake (scripted)"
)

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
	searches                   []string // offered on the Documents screen with v2's own hits
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
				facts:    []string{"LIT-D1", "LIT-D2", "LIT-D3", "LIT-D4", "DEMO-D5"}, sections: same("d", 5),
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
		}, searches: []string{"served registered agent", "answer due after service", "shortfall fee 35%", "lost profits consequential damages"}},
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
		}, searches: []string{"HSR filing fee", "Netherlands turnover", "waiting period", "pricing decisions from signing"}},
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
		}, searches: []string{"royalty rate net sales", "GPL statically linked", "maintenance fee lapsed", "assignment change of control"}},
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
	SectionID   string `json:"sectionId"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Search      string `json:"search,omitempty"` // what the server indexes instead of text (a table row with its headers)
	Context     string `json:"context,omitempty"`
	Transcribed bool   `json:"transcribed,omitempty"`
}

// rSearch is a search the Documents screen offers, with the hits v2's Search returned for it.
type rSearch struct {
	MatterNumber string `json:"matterNumber"`
	Query        string `json:"query"`
	Hits         []rHit `json:"hits"`
}

type rHit struct {
	DocumentID  string  `json:"documentId"`
	SectionID   string  `json:"sectionId"`
	Title       string  `json:"title"`
	Text        string  `json:"text"`
	Context     string  `json:"context,omitempty"`
	Rank        float64 `json:"rank"`
	Transcribed bool    `json:"transcribed,omitempty"`
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
	Schema      int         `json:"schema"`
	Provenance  string      `json:"provenance"`
	Generator   string      `json:"generator"`
	Clients     []rClient   `json:"clients"`
	Documents   []rDocument `json:"documents"`
	Tasks       []rTask     `json:"tasks"`
	Searches    []rSearch   `json:"searches"`
	BudgetCalls int         `json:"budgetCalls"` // BIGLAW_BUDGET_CALLS' default, what a replay's budget shows
}

type answerKey struct {
	AsOf  string    `json:"as_of"`
	Facts []keyFact `json:"facts"`
}

type keyFact struct {
	ID, Title, Question, Category, Severity string
	Documents, Evidence                     []string
	ModelAnswer                             string `json:"model_answer"`
}

func demoFactsOf(dir string) (out []keyFact) {
	for _, f := range demoFacts[dir] {
		out = append(out, keyFact{ID: f.ID, Question: f.Question, Severity: f.Severity, Documents: f.Documents, Evidence: f.Evidence, ModelAnswer: f.ModelAnswer})
	}
	return out
}

// ingest stores one document through v2's own paths: text as text, the scan through Ingest (the vision path).
func ingest(t *testing.T, svc *docs.Service, name string, data []byte) docs.Document {
	t.Helper()
	var d docs.Document
	var err error
	if strings.HasSuffix(name, ".jpg") {
		d, err = svc.Ingest(t.Context(), name, "image/jpeg", bytes.NewReader(data))
	} else {
		d, err = svc.IngestText(t.Context(), name, string(data))
	}
	if err != nil {
		t.Fatalf("ingest %s: %v", name, err)
	}
	return d
}

func transcriptPages(tr *docs.Transcript) []docs.PageRead {
	if tr == nil {
		return nil
	}
	return tr.Pages
}

// budgetCalls is BIGLAW_BUDGET_CALLS' default in this build's configuration table (cmd/biglaw/config.go envVars), read from its source.
func budgetCalls(t *testing.T) int {
	t.Helper()
	src, err := os.ReadFile("../../cmd/biglaw/config.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\{name: "BIGLAW_BUDGET_CALLS", def: "(\d+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no BIGLAW_BUDGET_CALLS default in cmd/biglaw/config.go; its table format changed")
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

// stableID is a UUID-shaped id derived from the name, so the recording is byte-stable.
func stableID(name string) string {
	h := fmt.Sprintf("%x", sha256.Sum256([]byte("biglaw-demo:"+name)))
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

func round(f float64) float64 { return math.Round(f*1e4) / 1e4 }

var fidRe = regexp.MustCompile(`\[(F\d+)\]`)

func TestDemoRecording(t *testing.T) {
	t.Parallel()
	st, err := store.OpenPool(t.Context(), storetest.DSN(t), nil, storetest.Pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := docs.New(st)
	// The scan's reader is scripted: both readings are the text the image was drawn from (make_scan.py).
	text, err := os.ReadFile(scanText)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	read := []model.Reply{{Text: string(text), Usage: model.Usage{CostUSD: &zero}}}
	svc.UseVision(model.NewFake(map[string][]model.Reply{"transcribe-a": read, "transcribe-b": read}), scanReader, "", nil)
	rec := recording{Schema: 3, Provenance: provenance, Generator: "eval/demo/record_test.go (make demo-record)", Clients: []rClient{}, Documents: []rDocument{},
		Tasks: []rTask{}, Searches: []rSearch{}, BudgetCalls: budgetCalls(t)}
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
		key.Facts = append(key.Facts, demoFactsOf(m.dir)...)
		type source struct {
			name string
			data []byte
		}
		var sources []source
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(dir, "documents", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, source{e.Name(), data})
		}
		if m.dir == scanMatter {
			data, err := os.ReadFile(scanImage)
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, source{scanName, data})
		}
		for _, src := range sources {
			d := ingest(t, svc, src.name, src.data)
			sid := stableID(m.dir + "/" + src.name)
			realIDs[src.name], stable[d.ID] = d.ID, sid
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
			// the matter's as-of date stands for the ingest (and transcription) time and the file id is
			// derived from the name, so the recording is byte-stable
			rd.ID, rd.CreatedAt = sid, asOf
			if rd.FileID != "" {
				rd.FileID = stableID("file:" + m.dir + "/" + src.name)
			}
			if rd.Transcript != nil {
				tr := *rd.Transcript
				tr.TranscribedAt = asOf
				rd.Transcript = &tr
			}
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
				t.Fatalf("%s: Parse gives %d sections, the outline %d", src.name, len(secs), len(outline))
			}
			for _, c := range docs.Chunkify(full.Text, secs) {
				transcribed := false
				for _, p := range transcriptPages(full.Transcript) {
					transcribed = transcribed || p.Start < c.End && c.Start < p.End
				}
				rd.ChunkList = append(rd.ChunkList, rChunk{SectionID: fmt.Sprintf("s%d", c.Section), Title: c.Locator, Text: c.Text,
					Search: c.Search, Context: c.Context, Transcribed: transcribed})
			}
			rec.Documents = append(rec.Documents, rd)
		}
		for _, q := range m.searches {
			hits, err := svc.Search(t.Context(), scope, q, 8, 0)
			if err != nil {
				t.Fatal(err)
			}
			rs := rSearch{MatterNumber: m.number, Query: q, Hits: []rHit{}}
			for _, h := range hits {
				rs.Hits = append(rs.Hits, rHit{stable[h.DocumentID], h.SectionID, h.Title, h.Text, h.Context, round(h.Rank), h.Transcribed})
			}
			rec.Searches = append(rec.Searches, rs)
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
	add("ledger", fmt.Sprintf("plan recorded: %s", plural(len(plan), "issue")), "scripted", 900,
		map[string]any{"op": "plan", "issues": plan}, map[string]any{"ok": true})
	for _, d := range all {
		if d.MatterNumber != m.number {
			continue
		}
		add("retrieve", fmt.Sprintf("outline of %s: %s", d.Name, plural(len(d.Outline), "section")), "computed", 350,
			map[string]any{"op": "outline", "documentId": d.ID}, map[string]any{"sections": len(d.Outline)})
	}
	names := make([]string, 0, len(realIDs)) // every document of the matter, in name order
	for name := range realIDs {
		names = append(names, name)
	}
	sort.Strings(names)
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
		add("retrieve", fmt.Sprintf("search %q: %s", f.Question, plural(len(out), "hit")), "computed", 450,
			map[string]any{"op": "search", "query": f.Question, "k": 3}, map[string]any{"hits": out})
		fid := fmt.Sprintf("F%d", i+1)
		fd := rFinding{FID: fid, IssueID: fmt.Sprintf("I%d", i+1), Question: f.Question, Claim: f.ModelAnswer, Severity: f.Severity, Citations: []rCitation{}}
		verified := 0
		for _, q := range append(append([]string{}, f.Evidence...), demoQuotes[id]...) {
			c := rCitation{Quote: q, Method: "none"}
			for _, name := range unique(append(append([]string{}, f.Documents...), names...)) { // the cited documents first
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
		fd.AtStep = add("extract_facts", fmt.Sprintf("%s: %d of %s verified%s", fid, verified, plural(len(fd.Citations), "quote"), map[bool]string{true: "", false: "; quarantined"}[verified > 0]), "scripted", 1300,
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
		add("draft_section", fmt.Sprintf("section %q from %s", ts.sectionTitles[g], plural(len(used), "finding")), "scripted", 1100,
			map[string]any{"issueIds": used}, map[string]any{"heading": ts.sectionTitles[g], "findings": used})
	}
	cites := 0
	for _, f := range task.Findings {
		for _, c := range f.Citations {
			methods[c.Method]++
			cites++
		}
	}
	memo.WriteString("## Limitations\n\n- This is a recorded demonstration. No model wrote this memo: each claim is the matter's answer-key reference answer, and the quote behind it was verified by BigLaw v2's verification ladder.\n")
	var written []string // what was written for the demo, not taken from the answer key
	for i, id := range ts.facts {
		f := task.Findings[i]
		if strings.HasPrefix(id, "DEMO-") {
			written = append(written, fmt.Sprintf("finding %s (its quote misstates the rule's figure)", f.FID))
		}
		for _, c := range f.Citations {
			switch {
			case !slices.Contains(demoQuotes[id], c.Quote):
			case strings.HasSuffix(c.Method, "@transcript"):
				written = append(written, fmt.Sprintf("in %s, the quote from the scanned affidavit of service (its transcript was scripted, not read by a model)", f.FID))
			default:
				written = append(written, fmt.Sprintf("in %s, a quote typed with curly quotes", f.FID))
			}
		}
	}
	if len(written) > 0 {
		fmt.Fprintf(&memo, "- Written for this demo, to show the whole verification ladder: %s. Each went through the same ladder as the rest.\n", strings.Join(written, "; "))
	}
	memo.WriteString("- No committee (prosecutor, reproducer, gap auditor) ran: critics are model calls, and this recording made none.\n- The matter is synthetic; the parties are fictional. Not legal advice.\n\n")
	fmt.Fprintf(&memo, "## Evidence Note\n\n%s, %s. Verified: %d exact, %d tolerant, %d in a machine transcript of a scan (@transcript, confidence capped at 85%%: check against the original). "+
		"No paraphrase judge ran (judge and ensemble are model calls). Unverified and excluded from this memo: %s; %s quarantined",
		plural(len(task.Findings), "finding"), plural(cites, "citation"), methods["exact"], methods["tolerant"],
		methods["exact@transcript"]+methods["tolerant@transcript"], plural(methods["none"], "citation"), plural(quarantined, "finding"))
	var out []string
	for _, f := range task.Findings {
		if f.Status != "verified" {
			out = append(out, fmt.Sprintf("%s (%q: not found in any of the matter's %d documents)", f.FID, f.Citations[0].Quote, len(names)))
		}
	}
	if len(out) > 0 {
		memo.WriteString(": " + strings.Join(out, "; "))
	}
	memo.WriteString(".\n")
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

// unique drops repeats, keeping first-appearance order.
func unique(names []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
