// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs/ooxml"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/lib/pq"
)

// Track changes (README § Track changes): the locator, the compare, the child verb and the
// change-set rows.

const contractText = "SUPPLY AGREEMENT\n1. Delivery. The Supplier shall deliver the Goods within 30 days of the order.\n2. Payment. Payment is due within 60 days of invoice. Payment is due in full.\n3. Term. This Agreement lasts two years.\n"

func contractDocx() []byte {
	return minimalDocx(strings.Split(strings.TrimSuffix(contractText, "\n"), "\n")...)
}

// TestLocateExactTolerantContextAmbiguous (AC-15): the three rungs and the refusals, table-driven.
func TestLocateExactTolerantContextAmbiguous(t *testing.T) {
	t.Parallel()
	text := "The “Effective Date” is the Supplier’s choice.\nPayment is due within 60 days.\nPayment is due within 60 days of the second invoice.\n"
	nt := NewText(text)
	for _, c := range []struct {
		name string
		e    Edit
		want Outcome
	}{
		{"exact unique", Edit{Find: "Supplier’s choice", Replace: "x"}, Outcome{Status: "located", Method: "exact", Start: 32, End: 51}},
		{"tolerant quotes", Edit{Find: `"Effective Date"`, Replace: "x"}, Outcome{Status: "located", Method: "tolerant", Start: 4, End: 24}},
		{"tolerant spacing and case", Edit{Find: "payment   is DUE within 60 days of the second", Replace: "x"}, Outcome{Status: "located", Method: "tolerant", Start: 84, End: 127}},
		{"recurring, placed by context", Edit{Find: "60 days", Replace: "45 days", After: " of the second"}, Outcome{Status: "located", Method: "exact", Start: 106, End: 113}},
		{"recurring, no context", Edit{Find: "60 days", Replace: "45 days"}, Outcome{Status: "ambiguous", Method: "exact"}},
		{"recurring, context that fits both", Edit{Find: "60 days", Replace: "45 days", Before: "within "}, Outcome{Status: "ambiguous", Method: "exact"}},
		{"not found", Edit{Find: "90 days", Replace: "x"}, Outcome{Status: "not_found"}},
		{"insertion by context", Edit{Replace: " (sixty)", Before: "within 60 days", After: " of the second"}, Outcome{Status: "located", Method: "context", Start: 113, End: 113}},
		{"insertion after alone", Edit{Replace: "Note. ", After: "The “Effective"}, Outcome{Status: "located", Method: "context", Start: 0, End: 0}},
		{"insertion before alone, recurring", Edit{Replace: "x", Before: "Payment is due within 60 days"}, Outcome{Status: "ambiguous"}},
		{"context only around a paraphrase", Edit{Find: "sixty days", Replace: "45 days", Before: "is due within ", After: " of the second"}, Outcome{Status: "located", Method: "context", Start: 106, End: 113}},
		{"inside its paragraph", Edit{Find: "60 days", Replace: "45 days", Paragraph: "Payment is due within 60 days of the second invoice."}, Outcome{Status: "located", Method: "exact", Start: 106, End: 113}},
		{"paragraph not found", Edit{Find: "60 days", Replace: "x", Paragraph: "No such paragraph."}, Outcome{Status: "not_found"}},
		{"delete paragraph", Edit{Kind: KindDeleteParagraph, Paragraph: "Payment is due within 60 days."}, Outcome{Status: "located", Method: "exact", Start: 53, End: 83}},
		{"insert paragraph, tolerant", Edit{Kind: KindInsertParagraph, Paragraph: "payment is due within 60 days.", Replace: "New."}, Outcome{Status: "located", Method: "tolerant", Start: 53, End: 83}},
		{"paragraph is a part of a line", Edit{Kind: KindDeleteParagraph, Paragraph: "60 days"}, Outcome{Status: "not_found"}},
	} {
		got := locate(nt, c.e)
		if got.Status != c.want.Status || got.Method != c.want.Method || got.Status == "located" && (got.Start != c.want.Start || got.End != c.want.End) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
		if got.Status == "located" && c.e.Find != "" && got.Method != "context" && !strings.EqualFold(normalized(text[got.Start:got.End]), normalized(c.e.Find)) {
			t.Errorf("%s: located %q for %q", c.name, text[got.Start:got.End], c.e.Find)
		}
	}
	// a span over a paragraph break is refused even when its words are found by context
	o := locate(NewText("one two\nthree four\n"), Edit{Find: "two three", Replace: "x", Before: "one ", After: " four"})
	if o.Status != StatusCrossesParagraph {
		t.Errorf("across paragraphs: %+v", o)
	}
	// two edits of one span: the second overlaps
	os := locateAll(text, []Edit{{Find: "Supplier’s", Replace: "a"}, {Find: "Supplier’s choice", Replace: "b"}, {Find: "Effective", Replace: "c"}})
	if os[0].Status != "located" || os[1].Status != StatusOverlaps || os[2].Status != "located" {
		t.Errorf("overlaps: %+v", os)
	}
}

func normalized(s string) string {
	n, _ := normalize(s)
	return strings.TrimSpace(n)
}

// TestCompareWordLevelRoundTrip (AC-16): compare original to revised, apply as tracked changes
// on the original; accepted it reads as the revised text, rejected as the original; the diff is
// word-level (no mid-word hunks) and paragraphs inserted, deleted and changed are each one edit.
func TestCompareWordLevelRoundTrip(t *testing.T) {
	t.Parallel()
	original := "SUPPLY AGREEMENT\n1. Delivery. The Supplier shall deliver the Goods within 30 days of the order.\n2. Payment. Payment is due within 60 days of invoice.\n3. Term. This Agreement lasts two years.\n4. Notices. Notices go by post.\n"
	revised := "SUPPLY AGREEMENT\n1. Delivery. The Supplier shall deliver the Products within 45 days of the order.\n1A. Acceptance. The Buyer inspects the Products on delivery.\n2. Payment. Payment is due within 60 days of invoice.\n3. Term. This Agreement lasts three years and renews.\n"
	edits := compareTexts(original, revised)
	var kinds []string
	for _, e := range edits {
		kinds = append(kinds, e.Kind+":"+e.Find+"->"+e.Replace)
	}
	want := []string{":Goods ->Products ", ":30 ->45 ", "insert_paragraph:->1A. Acceptance. The Buyer inspects the Products on delivery.", ":two years.->three years and renews.", "delete_paragraph:->"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("edits %q\nwant %q", kinds, want)
	}
	if edits[0].Paragraph != strings.Split(original, "\n")[1] || !strings.HasSuffix(edits[0].Before, "The Supplier shall deliver the ") || len(edits[0].Before) > editContext || edits[2].Paragraph != strings.Split(original, "\n")[1] || edits[4].Paragraph != "4. Notices. Notices go by post." {
		t.Errorf("paragraphs and context: %+v", edits)
	}
	// insertion before the first paragraph, and an edit of the first word
	if e := compareTexts("Alpha beta.\n", "Title\nAlpha gamma.\n"); len(e) != 2 || e[0].Kind != KindInsertParagraphBefore || e[0].Paragraph != "Alpha beta." || e[1].Find != "beta." || e[1].Replace != "gamma." {
		t.Errorf("insert before: %+v", e)
	}
	// render: the edits applied to the original .docx read as the revised text once accepted
	paras := strings.Split(strings.TrimSuffix(original, "\n"), "\n")
	out, docx := docxVerb(docxRequest{Verb: KindCompare, Author: "A", Date: "2026-11-03T10:00:00Z", Text: original, Edits: edits}, minimalDocx(paras...))
	if out.Err != "" {
		t.Fatal(out.Err)
	}
	for i, o := range out.Outcomes {
		if o.Status != StatusApplied {
			t.Errorf("edit %d: %+v", i, o)
		}
	}
	if got := decidedText(t, docx, true); got != revised {
		t.Errorf("accepted:\n%q\nwant\n%q", got, revised)
	}
	if got := decidedText(t, docx, false); got != original {
		t.Errorf("rejected:\n%q\nwant\n%q", got, original)
	}
	// compare of a document with itself: nothing
	if e := compareTexts(original, original); len(e) != 0 {
		t.Errorf("self-compare: %+v", e)
	}
	// footnotes are not compared
	if e := compareTexts("A.\n\nFootnotes\n[1] x\n", "A.\n\nFootnotes\n[1] y\n"); len(e) != 0 {
		t.Errorf("footnotes compared: %+v", e)
	}
}

func decidedText(t *testing.T, docx []byte, accept bool) string {
	t.Helper()
	d, err := ooxml.OpenAll(docx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.Decide(accept, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Walk(v, ooxml.NewRevisions("x", time.Now())); err != nil {
		t.Fatal(err)
	}
	s, _ := d.Text()
	return s
}

// TestApplyEditsOutcomes: an edit over a field or a content control fails with its reason; a
// comment rides on the change; an insertion lands at its point; the unlocated keep their outcome.
func TestApplyEditsOutcomes(t *testing.T) {
	t.Parallel()
	body := `<w:p><w:r><w:t xml:space="preserve">Page </w:t></w:r><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText>PAGE</w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>1</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r><w:r><w:t xml:space="preserve"> of the Agreement.</w:t></w:r></w:p>` +
		`<w:p><w:sdt><w:sdtContent><w:r><w:t>Acme Ltd</w:t></w:r></w:sdtContent></w:sdt><w:r><w:t xml:space="preserve"> is the Supplier.</w:t></w:r></w:p>`
	data := docxWithBody(body)
	d, _ := ooxml.OpenAll(data)
	text, _ := d.Text()
	edits := []Edit{{Find: "1", Replace: "2", Before: "Page ", After: " of the"}, {Find: "Acme Ltd", Replace: "Acme plc"}, {Find: "Agreement", Replace: "Contract", Comment: "Defined term."}, {Replace: " (the Buyer)", Before: " is the Supplier"}, {Find: "missing", Replace: "x"}}
	out, docx := docxVerb(docxRequest{Verb: KindEdits, Author: "A", Date: "2026-11-03T10:00:00Z", Text: clean(text), Edits: edits}, data)
	if out.Err != "" {
		t.Fatal(out.Err)
	}
	var got []string
	for _, o := range out.Outcomes {
		got = append(got, o.Status+":"+o.Reason)
	}
	want := []string{"failed:the text is a Word field (its code or result)", "failed:the text is in a content control", "applied:", "applied:", "not_found:the text is not in the document (neither exactly nor with quotes and spacing normalised), and no context was given to place it"}
	if !slices.Equal(got, want) {
		t.Errorf("outcomes %q\nwant %q", got, want)
	}
	if s := decidedText(t, docx, true); s != "Page 1 of the Contract.\nAcme Ltd is the Supplier (the Buyer).\n" {
		t.Errorf("accepted: %q", s)
	}
	od, _ := ooxml.OpenAll(docx)
	if cm, ok := od.Part("word/comments.xml"); !ok || !strings.Contains(cm, "Defined term.") {
		t.Errorf("the comment: %q", cm)
	}
}

func docxWithBody(body string) []byte {
	var buf bytes.Buffer
	w, closeZip := newZip(&buf)
	w("word/document.xml", `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+body+`</w:body></w:document>`)
	closeZip()
	return buf.Bytes()
}

// TestTextDriftRefused (AC-23): a file whose text differs from the stored text renders nothing
// and fails every edit with text_drift.
func TestTextDriftRefused(t *testing.T) {
	t.Parallel()
	edits := []Edit{{Find: "30 days", Replace: "45 days"}}
	out, docx := docxVerb(docxRequest{Verb: KindEdits, Author: "A", Date: "2026-11-03T10:00:00Z", Text: contractText + "extra\n", Edits: edits}, contractDocx())
	if out.Err != "" || docx != nil || len(out.Outcomes) != 1 || out.Outcomes[0].Status != StatusFailed || !strings.HasPrefix(out.Outcomes[0].Reason, "text_drift") {
		t.Errorf("drift: %+v %d bytes", out, len(docx))
	}
}

// TestDocxChildContract: the writer runs in the bounded child and its output is read through hard
// bounds; a hostile file is unreadable, never a crash of the test process; the accept and reject
// and revisions verbs answer; a bad revision id is bad input.
func TestDocxChildContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	edits := []Edit{{Find: "30 days", Replace: "45 days", Comment: "Floor."}}
	res, docx, err := docxInChild(ctx, docxRequest{Verb: KindEdits, Author: "Horatiu", Date: "2026-11-03T10:00:00Z", Text: contractText, Edits: edits}, contractDocx())
	if err != nil || len(res.Outcomes) != 1 || res.Outcomes[0].Status != StatusApplied {
		t.Fatalf("edits in the child: %v %+v", err, res)
	}
	if got := decidedText(t, docx, true); !strings.Contains(got, "within 45 days") {
		t.Errorf("accepted: %q", got)
	}
	revs, _, err := docxInChild(ctx, docxRequest{Verb: "revisions"}, docx)
	if err != nil || len(revs.Revisions) != 1 || revs.Revisions[0].Kind != "substitution" || revs.Revisions[0].Author != "Horatiu" {
		t.Fatalf("revisions in the child: %v %+v", err, revs.Revisions)
	}
	rej, back, err := docxInChild(ctx, docxRequest{Verb: KindReject, Author: "x", Date: "2026-11-03T10:00:00Z", RevisionIDs: []int{revs.Revisions[0].ID}}, docx)
	if err != nil || len(rej.Outcomes) != 1 || rej.Outcomes[0].Status != StatusApplied {
		t.Fatalf("reject in the child: %v %+v", err, rej)
	}
	if got := decidedText(t, back, true); got != contractText {
		t.Errorf("rejected back: %q", got)
	}
	if _, _, err := docxInChild(ctx, docxRequest{Verb: KindAccept, Author: "x", Date: "2026-11-03T10:00:00Z", RevisionIDs: []int{999}}, docx); !errors.Is(err, ErrBadInput) {
		t.Errorf("a bad revision id: %v", err)
	}
	if _, _, err := docxInChild(ctx, docxRequest{Verb: "revisions"}, []byte("not a zip")); !errors.Is(err, ErrUnreadable) {
		t.Errorf("not a zip: %v", err)
	}
	// a zip bomb in a text part dies in the child at its cap, or is refused by the reader's cap
	var bomb bytes.Buffer
	w, closeZip := newZip(&bomb)
	w("word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("A", 1<<20)+`</w:t></w:r></w:p></w:body></w:document>`)
	w("word/footnotes.xml", strings.Repeat(" ", 200<<20))
	closeZip()
	if _, _, err := docxInChild(ctx, docxRequest{Verb: "revisions"}, bomb.Bytes()); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a bomb: %v", err)
	}
	// the reader's bounds: a header with no newline, a Len past the cap, bytes after the end
	for name, out := range map[string]string{"no newline": `{"Len":0}`, "too long": `{"Len":` + strconv.Itoa(store.MaxFileSize+1) + "}\n", "short": `{"Len":5}` + "\nabc", "extra": `{"Len":1}` + "\nab"} {
		if _, _, ok := readDocxChild(strings.NewReader(out)); ok {
			t.Errorf("%s was accepted", name)
		}
	}
	if o, b, ok := readDocxChild(strings.NewReader(`{"Len":3,"Outcomes":[]}` + "\nxyz")); !ok || string(b) != "xyz" || o.Len != 3 {
		t.Errorf("a good answer: %v %q", ok, b)
	}
}

// TestDocxChildBoundsItself: the writer child dies at its deadline and with its parent, as the
// reader does (its argument differs, its bounds are the same code).
func TestDocxChildBoundsItself(t *testing.T) {
	if raceBuild() {
		t.Skip("the race detector's shadow memory is not under the cap")
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, docxArg, strconv.Itoa(os.Getpid()), "500", strconv.FormatUint(1<<30, 10))
	cmd.Env = append([]string{}, childEnv...)
	cmd.Stdin = strings.NewReader(`{"Verb":"revisions"}` + "\n" + string(contractDocx()))
	cmd.Stdout = io.Discard
	start := time.Now()
	if err := cmd.Run(); err != nil { // a fast file answers before the deadline
		t.Fatalf("the child failed: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the child took %s", time.Since(start))
	}
	slow := exec.Command(exe, docxArg, strconv.Itoa(os.Getpid()), "300", strconv.FormatUint(1<<30, 10))
	slow.Env = append([]string{}, childEnv...)
	pr, pw, err := os.Pipe() // an *os.File: Wait does not wait for a copying goroutine
	if err != nil {
		t.Fatal(err)
	}
	slow.Stdin = pr
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	pr.Close()
	t.Cleanup(func() { slow.Process.Kill(); pw.Close() })
	pw.Write([]byte(`{"Verb":"revisions"}` + "\n"))
	done := make(chan error, 1)
	go func() { done <- slow.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a child starved of its file exited 0")
		}
	case <-time.After(10 * time.Second):
		t.Error("the child did not exit at its deadline")
	}
}

// TestChangeSetRows (DB): a change set is stored with its outcomes, listed newest first and paged,
// rendered on demand (the row's outcomes updated), scoped to the task's documents, and refused
// for a document that is not a .docx; a compare names both documents; revisions page.
func TestChangeSetRows(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	doc, err := s.Ingest(ctx, "contract.docx", docxType, bytes.NewReader(contractDocx()))
	if err != nil {
		t.Fatal(err)
	}
	revised := minimalDocx("SUPPLY AGREEMENT", "1. Delivery. The Supplier shall deliver the Goods within 45 days of the order.", "2. Payment. Payment is due within 60 days of invoice. Payment is due in full.", "3. Term. This Agreement lasts two years.")
	doc2, err := s.Ingest(ctx, "contract-v2.docx", docxType, bytes.NewReader(revised))
	if err != nil {
		t.Fatal(err)
	}
	text := ingestText(t, s, "note.txt", "plain")
	var task string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO tasks (description, workflow_type, jurisdiction, matter_number, document_ids, max_steps, tool_bandit, lead_model) VALUES ('t', 'memo', '', '', $1, 10, false, 'm') RETURNING id`, pq.Array([]string{doc.ID, doc2.ID, text.ID})).Scan(&task); err != nil {
		t.Fatal(err)
	}
	scope := []string{doc.ID, doc2.ID, text.ID}
	by := json.RawMessage(`{"id":"u1","name":"Horatiu"}`)
	cs, err := s.CreateChangeSet(ctx, scope, task, "Horatiu Druma-Strugariu", by, ChangeIn{DocumentID: doc.ID, Kind: KindEdits, Edits: []Edit{{Find: "30 days", Replace: "45 days", Comment: "Floor."}, {Find: "due in full", Replace: "due in full, without set-off"}, {Find: "nowhere", Replace: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if cs.ID == "" || cs.Kind != KindEdits || cs.Author != "Horatiu Druma-Strugariu" || string(cs.CreatedBy) != string(by) || len(cs.Outcomes) != 3 || cs.Outcomes[0].Status != "located" || cs.Outcomes[2].Status != "not_found" {
		t.Fatalf("created: %+v", cs)
	}
	// scope and kind refusals
	if _, err := s.CreateChangeSet(ctx, []string{doc2.ID}, task, "a", nil, ChangeIn{DocumentID: doc.ID, Kind: KindEdits, Edits: []Edit{{Find: "x", Replace: "y"}}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("out of scope: %v", err)
	}
	if _, err := s.CreateChangeSet(ctx, scope, task, "a", nil, ChangeIn{DocumentID: text.ID, Kind: KindEdits, Edits: []Edit{{Find: "x", Replace: "y"}}}); !errors.Is(err, ErrNotDocx) {
		t.Errorf("a text document: %v", err)
	}
	for _, in := range []ChangeIn{{DocumentID: doc.ID, Kind: "x"}, {DocumentID: doc.ID, Kind: KindEdits}, {DocumentID: doc.ID, Kind: KindCompare}, {DocumentID: doc.ID, Kind: KindEdits, Edits: []Edit{{}}},
		{DocumentID: doc.ID, Kind: KindEdits, Edits: []Edit{{Find: "a\nb", Replace: "c"}}}, {DocumentID: doc.ID, Kind: KindAccept, Edits: []Edit{{Find: "a", Replace: "b"}}}, {DocumentID: doc.ID, Kind: KindEdits, Edits: []Edit{{Kind: "weird", Find: "a"}}}} {
		if _, err := s.CreateChangeSet(ctx, scope, task, "a", nil, in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	// compare
	cmp, err := s.CreateChangeSet(ctx, scope, task, "a", nil, ChangeIn{DocumentID: doc.ID, Kind: KindCompare, AgainstDocumentID: doc2.ID})
	if err != nil || cmp.AgainstID != doc2.ID || len(cmp.Edits) != 1 || cmp.Edits[0].Find != "30 " || cmp.Outcomes[0].Status != "located" {
		t.Fatalf("compare: %v %+v", err, cmp)
	}
	// list, newest first
	page, err := s.ChangeSets(ctx, task, 0)
	if err != nil || len(page.ChangeSets) != 2 || page.ChangeSets[0].ID != cmp.ID || page.Next != nil {
		t.Fatalf("list: %v %+v", err, page)
	}
	if got, err := s.ChangeSet(ctx, task, cs.ID); err != nil || got.ID != cs.ID || len(got.Edits) != 3 {
		t.Errorf("get: %v %+v", err, got)
	}
	if _, err := s.ChangeSet(ctx, "00000000-0000-0000-0000-000000000000", cs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another task's: %v", err)
	}
	// render
	var out bytes.Buffer
	var name string
	rendered, err := s.RenderChangeSet(ctx, scope, task, cs.ID, func(n string, size int64) error { name = n; return nil }, &out)
	if err != nil || name != "contract.changes.docx" || out.Len() == 0 {
		t.Fatalf("render: %v %q %d", err, name, out.Len())
	}
	if rendered.Outcomes[0].Status != StatusApplied || rendered.Outcomes[1].Status != StatusApplied || rendered.Outcomes[2].Status != "not_found" {
		t.Errorf("rendered outcomes: %+v", rendered.Outcomes)
	}
	if got, _ := s.ChangeSet(ctx, task, cs.ID); got.Outcomes[0].Status != StatusApplied {
		t.Errorf("the row's outcomes were not updated: %+v", got.Outcomes)
	}
	if text := decidedText(t, out.Bytes(), true); !strings.Contains(text, "within 45 days") || !strings.Contains(text, "without set-off") {
		t.Errorf("accepted: %q", text)
	}
	var again bytes.Buffer
	if _, err := s.RenderChangeSet(ctx, scope, task, cs.ID, func(string, int64) error { return nil }, &again); err != nil || !bytes.Equal(out.Bytes(), again.Bytes()) {
		t.Errorf("a second render differs (%v)", err)
	}
	if _, err := s.RenderChangeSet(ctx, []string{doc2.ID}, task, cs.ID, func(string, int64) error { return nil }, io.Discard); !errors.Is(err, ErrNotFound) {
		t.Errorf("render out of scope: %v", err)
	}
	// the rendered file's revisions, read from a document holding it
	doc3, err := s.Ingest(ctx, "contract.changes.docx", docxType, bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	revs, err := s.Revisions(ctx, []string{doc3.ID}, doc3.ID, 0)
	if err != nil || len(revs.Revisions) != 2 || revs.Revisions[0].Author != "Horatiu Druma-Strugariu" || revs.Revisions[0].Kind != "substitution" {
		t.Fatalf("revisions: %v %+v", err, revs)
	}
	if _, err := s.Revisions(ctx, []string{doc.ID}, doc3.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("revisions out of scope: %v", err)
	}
	// accept them all through a change set on that document
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET document_ids = $2 WHERE id = $1`, task, pq.Array([]string{doc.ID, doc2.ID, text.ID, doc3.ID})); err != nil {
		t.Fatal(err)
	}
	acc, err := s.CreateChangeSet(ctx, append(scope, doc3.ID), task, "Reviewer", nil, ChangeIn{DocumentID: doc3.ID, Kind: KindAccept})
	if err != nil {
		t.Fatal(err)
	}
	var clean bytes.Buffer
	if acc, err = s.RenderChangeSet(ctx, append(scope, doc3.ID), task, acc.ID, func(string, int64) error { return nil }, &clean); err != nil || len(acc.Outcomes) != 2 {
		t.Fatalf("accept render: %v %+v", err, acc)
	}
	d, _ := ooxml.OpenAll(clean.Bytes())
	left, _ := d.Revisions()
	if len(left) != 0 {
		t.Errorf("revisions left after accept-all: %+v", left)
	}
	// deleting the document deletes its change sets
	if err := s.Delete(ctx, doc3.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeSet(ctx, task, acc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the document went: %v", err)
	}
}

// TestChangeSetsPage (DB): the list pages at changesPage with next.
func TestChangeSetsPage(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	doc, err := s.Ingest(ctx, "c.docx", docxType, bytes.NewReader(contractDocx()))
	if err != nil {
		t.Fatal(err)
	}
	var task string
	if err := s.db.QueryRowContext(ctx, `INSERT INTO tasks (description, workflow_type, jurisdiction, matter_number, document_ids, max_steps, tool_bandit, lead_model) VALUES ('t', 'memo', '', '', $1, 10, false, 'm') RETURNING id`, pq.Array([]string{doc.ID})).Scan(&task); err != nil {
		t.Fatal(err)
	}
	for range changesPage + 2 {
		if _, err := s.CreateChangeSet(ctx, []string{doc.ID}, task, "a", nil, ChangeIn{DocumentID: doc.ID, Kind: KindReject}); err != nil {
			t.Fatal(err)
		}
	}
	p1, err := s.ChangeSets(ctx, task, 0)
	if err != nil || len(p1.ChangeSets) != changesPage || p1.Next == nil || *p1.Next != changesPage {
		t.Fatalf("page 1: %v %d %v", err, len(p1.ChangeSets), p1.Next)
	}
	p2, err := s.ChangeSets(ctx, task, *p1.Next)
	if err != nil || len(p2.ChangeSets) != 2 || p2.Next != nil {
		t.Fatalf("page 2: %v %d %v", err, len(p2.ChangeSets), p2.Next)
	}
}

// newZip writes parts to buf: write adds one, close finishes the archive.
func newZip(buf *bytes.Buffer) (write func(name, content string), closeZip func()) {
	zw := zip.NewWriter(buf)
	return func(name, content string) {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}, func() { zw.Close() }
}
