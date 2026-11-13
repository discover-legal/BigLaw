// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/docs/ooxml"
)

// The track-changes routes (README § Track changes).

// minimalDocx is a .docx of the paragraphs (the docs package's test helper, repeated here).
func minimalDocx(paras ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		sb.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte(sb.String()))
	zw.Close()
	return buf.Bytes()
}

// upload files data as name through POST /documents/upload and returns the document's id.
func (s *server) upload(name string, data []byte) string {
	s.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", s.URL+"/documents/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.client().Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var d docs.Document
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 || json.Unmarshal(b, &d) != nil {
		s.t.Fatalf("upload %s: %d %s", name, resp.StatusCode, b)
	}
	return d.ID
}

// TestChangeSetRoutes (AC-18, AC-21, AC-22): a change set is posted against a task's document
// (strict body; a document outside the task's set is 404, a text document 422 not_docx), listed
// and read; its download is the rendered .docx with the local user's name on every change, the
// row's outcomes updated; a second download is byte-identical; the document's revisions page.
func TestChangeSetRoutes(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, nil, roles, doc1)
	docx := s.upload("contract.docx", minimalDocx("SUPPLY AGREEMENT", "1. Delivery. The Supplier shall deliver the Goods within 30 days of the order.", "2. Payment. Payment is due within 60 days of invoice."))
	other := s.upload("other.docx", minimalDocx("Another document."))
	code, body := s.do("POST", "/tasks", m{"description": "d", "workflowType": "memo", "documentIds": []string{docx, ids[0]}})
	if code != 201 {
		t.Fatalf("POST /tasks: %d %s", code, body)
	}
	var task struct{ ID string }
	json.Unmarshal([]byte(body), &task)
	changes := "/tasks/" + task.ID + "/changes"

	code, body = s.do("POST", changes, m{"documentId": docx, "kind": "edits", "edits": []m{{"find": "30 days", "replace": "45 days", "comment": "Our floor."}, {"find": "nowhere", "replace": "x"}}})
	if code != 201 {
		t.Fatalf("POST changes: %d %s", code, body)
	}
	var cs docs.ChangeSet
	json.Unmarshal([]byte(body), &cs)
	if cs.Author != "Local user" || cs.Kind != "edits" || len(cs.Outcomes) != 2 || cs.Outcomes[0].Status != "located" || cs.Outcomes[1].Status != "not_found" || !strings.Contains(string(cs.CreatedBy), `"name":"Local user"`) || strings.Contains(string(cs.CreatedBy), "email") {
		t.Fatalf("created: %s", body)
	}
	for name, c := range map[string]struct {
		body m
		code int
		kind string
	}{
		"outside the task":  {m{"documentId": other, "kind": "edits", "edits": []m{{"find": "a", "replace": "b"}}}, 404, "not_found"},
		"a text document":   {m{"documentId": ids[0], "kind": "edits", "edits": []m{{"find": "a", "replace": "b"}}}, 422, "not_docx"},
		"an unknown field":  {m{"documentId": docx, "kind": "edits", "author": "Eve", "edits": []m{{"find": "a", "replace": "b"}}}, 400, "bad_request"},
		"an unknown kind":   {m{"documentId": docx, "kind": "redline"}, 400, "bad_request"},
		"a misspelt edit":   {m{"documentId": docx, "kind": "edits", "edits": []m{{"fnd": "a", "replace": "b"}}}, 400, "bad_request"},
		"no edits":          {m{"documentId": docx, "kind": "edits", "edits": []m{}}, 400, "bad_request"},
		"compare with none": {m{"documentId": docx, "kind": "compare"}, 400, "bad_request"},
	} {
		code, body := s.do("POST", changes, c.body)
		if code != c.code || !strings.Contains(body, `"errorType":"`+c.kind+`"`) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if code, body := s.do("POST", "/tasks/00000000-0000-0000-0000-000000000000/changes", m{"documentId": docx, "kind": "edits", "edits": []m{{"find": "a", "replace": "b"}}}); code != 404 {
		t.Errorf("no such task: %d %s", code, body)
	}
	code, body = s.do("GET", changes, nil)
	var page docs.ChangeSetPage
	if json.Unmarshal([]byte(body), &page); code != 200 || len(page.ChangeSets) != 1 || page.ChangeSets[0].ID != cs.ID || page.Next != nil {
		t.Errorf("GET changes: %d %s", code, body)
	}
	if code, body := s.do("GET", changes+"/"+cs.ID, nil); code != 200 || !strings.Contains(body, `"id":"`+cs.ID+`"`) {
		t.Errorf("GET one: %d %s", code, body)
	}
	if code, _ := s.do("GET", changes+"/00000000-0000-0000-0000-000000000000", nil); code != 404 {
		t.Errorf("GET a missing one: %d", code)
	}
	// the download
	resp, err := s.client().Get(s.URL + changes + "/" + cs.ID + "/file")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || !strings.Contains(resp.Header.Get("Content-Disposition"), "contract.changes.docx") || resp.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("download: %d %v %.100s", resp.StatusCode, resp.Header, out)
	}
	d, err := ooxml.OpenAll(out)
	if err != nil {
		t.Fatal(err)
	}
	revs, _ := d.Revisions()
	if len(revs) != 1 || revs[0].Author != "Local user" || revs[0].Inserted != "45 days" || revs[0].Deleted != "30 days" {
		t.Errorf("the rendered changes: %+v", revs)
	}
	if cm, ok := d.Part("word/comments.xml"); !ok || !strings.Contains(cm, "Our floor.") || !strings.Contains(cm, `w:author="Local user"`) {
		t.Errorf("the comment: %q", cm)
	}
	if settings, ok := d.Part("word/settings.xml"); !ok || !strings.Contains(settings, "trackRevisions") {
		t.Errorf("settings: %q", settings)
	}
	_, body = s.do("GET", changes+"/"+cs.ID, nil)
	var after docs.ChangeSet
	if json.Unmarshal([]byte(body), &after); after.Outcomes[0].Status != "applied" || after.Outcomes[1].Status != "not_found" {
		t.Errorf("outcomes after the render: %s", body)
	}
	resp, _ = s.client().Get(s.URL + changes + "/" + cs.ID + "/file")
	again, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(out, again) {
		t.Error("a second download differs")
	}
	// the rendered file's revisions, as a document of the task
	rendered := s.upload("contract.changes.docx", out)
	if code, body := s.do("GET", "/tasks/"+task.ID+"/documents/"+rendered+"/revisions", nil); code != 404 {
		t.Errorf("a document outside the task: %d %s", code, body)
	}
	_, body = s.do("POST", "/tasks", m{"description": "d", "workflowType": "memo", "documentIds": []string{rendered}})
	var task2 struct{ ID string }
	json.Unmarshal([]byte(body), &task2)
	code, body = s.do("GET", "/tasks/"+task2.ID+"/documents/"+rendered+"/revisions", nil)
	var rp docs.RevisionsPage
	if json.Unmarshal([]byte(body), &rp); code != 200 || len(rp.Revisions) != 1 || rp.Revisions[0].Kind != "substitution" || rp.Next != nil {
		t.Errorf("revisions: %d %s", code, body)
	}
	// accept them all
	code, body = s.do("POST", "/tasks/"+task2.ID+"/changes", m{"documentId": rendered, "kind": "accept"})
	var acc docs.ChangeSet
	if json.Unmarshal([]byte(body), &acc); code != 201 {
		t.Fatalf("accept: %d %s", code, body)
	}
	resp, _ = s.client().Get(s.URL + "/tasks/" + task2.ID + "/changes/" + acc.ID + "/file")
	clean, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cd, err := ooxml.OpenAll(clean)
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := cd.Revisions(); len(left) != 0 {
		t.Errorf("after accept-all: %+v", left)
	}
	if text, _ := cd.Text(); !strings.Contains(text, "within 45 days") {
		t.Errorf("accepted text: %q", text)
	}
}

// TestCompareRoute: a compare change set names both documents and renders the difference as
// tracked changes on the older one.
func TestCompareRoute(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t, nil, roles)
	v1 := s.upload("v1.docx", minimalDocx("Clause 1. The Supplier delivers within 30 days.", "Clause 2. Payment within 60 days."))
	v2 := s.upload("v2.docx", minimalDocx("Clause 1. The Supplier delivers within 45 days.", "Clause 1A. Acceptance on delivery.", "Clause 2. Payment within 60 days."))
	_, body := s.do("POST", "/tasks", m{"description": "d", "workflowType": "memo", "documentIds": []string{v1, v2}})
	var task struct{ ID string }
	json.Unmarshal([]byte(body), &task)
	code, body := s.do("POST", "/tasks/"+task.ID+"/changes", m{"documentId": v1, "kind": "compare", "againstDocumentId": v2})
	var cs docs.ChangeSet
	if json.Unmarshal([]byte(body), &cs); code != 201 || cs.AgainstID != v2 || len(cs.Edits) != 2 || cs.Edits[0].Find != "30 " || cs.Edits[1].Kind != "insert_paragraph" {
		t.Fatalf("compare: %d %s", code, body)
	}
	resp, _ := s.client().Get(s.URL + "/tasks/" + task.ID + "/changes/" + cs.ID + "/file")
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d, err := ooxml.OpenAll(out)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := d.Decide(true, nil)
	d.Walk(v, ooxml.NewRevisions("x", cs.CreatedAt))
	if text, _ := d.Text(); text != "Clause 1. The Supplier delivers within 45 days.\nClause 1A. Acceptance on delivery.\nClause 2. Payment within 60 days.\n" {
		t.Errorf("accepted: %q", text)
	}
}
