// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r3 round-5 verifier's repros (critique/biglaw-lab-r3-r5-verifier.md), kept as regression
// tests; each failed on 84adfa0.

// docxOf builds a .docx (zip + word/document.xml) holding the paragraphs.
func docxOf(paras ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		sb.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(sb.String()))
	_ = zw.Close()
	return buf.Bytes()
}

// M2: the .docx reader ends a paragraph with one line break, so a requirement that is a short
// paragraph past the document's first ~3 KB was always truncated (Low, gated, and a false claim).
func TestV5DocxParagraphNotTruncated(t *testing.T) {
	paras := []string{"Memorandum"}
	for range 70 {
		paras = append(paras, "The Trustee shall keep the accounts of the trust and report them annually to each beneficiary.")
	}
	req := "The Trustee may name Sophia Davenport-Chen as a successor trustee."
	paras = append(paras, req, "The Trustee shall retain the residence.")
	x, err := docs.Extract(context.Background(), docxOf(paras...))
	if err != nil || !strings.Contains(x.Text, "beneficiary.\n"+req+"\n") {
		t.Fatalf("extract: %v %.200q", err, x.Text)
	}
	spans, bad, _ := placeRequirement(context.Background(), x.Text, req)
	if bad != "" {
		t.Fatalf("bad %q", bad)
	}
	if r := newReq(x.Text, spans); r.Truncated || len(r.Context()) > ctxMax {
		t.Errorf("a whole .docx paragraph inside the window is truncated (%d bytes)", len(r.Context()))
	}
	// a paragraph that does start above the window still is
	long := strings.Repeat("the Trustee shall keep the accounts of the trust and report them annually to each beneficiary, ", 60)
	x, _ = docs.Extract(context.Background(), docxOf("Memorandum", "Subject to the protector's consent, "+long+req))
	spans, _, _ = placeRequirement(context.Background(), x.Text, req)
	if r := newReq(x.Text, spans); !r.Truncated {
		t.Error("a .docx paragraph starting 6 KB above the requirement is not truncated")
	}
}

// M2 end to end: the same shape as text with one line break per paragraph is admitted at full
// confidence on a "yes".
func TestV5DocxRequirementAlwaysLow(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for b.Len() < 6000 {
		b.WriteString("The Trustee shall keep the accounts of the trust and report them annually to each beneficiary.\n")
	}
	req := "The Trustee may name Sophia Davenport-Chen as a successor trustee."
	memo := "Memorandum\n" + b.String() + req + "\nThe Trustee shall retain the residence.\n"
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "yes", "reason": "the paragraph says she may be named"})})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || fs[0].Low {
		t.Errorf("a requirement that is a whole paragraph, its start inside the window, is not admitted at full confidence: %+v", fs)
	}
}

// L1: a "shall not:" chapeau > 3 KB above blank-line-separated items: the window started mid-list,
// untruncated, and a "yes" admitted a confirmed HIGH deviation without the chapeau.
func TestV5LongListChapeauConfirmed(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("7. Restrictions\n\nThe Trustee shall not:\n\n")
	for i := range 30 {
		b.WriteString("(" + string(rune('a'+i%26)) + ") Sell, lease, mortgage or otherwise encumber any parcel of the family land held in the trust estate without the protector's written consent.\n\n")
	}
	req := "Name Sophia Davenport-Chen as a successor trustee."
	yes := []model.Reply{text(m{"answer": "yes", "reason": "the text says to name her"})}
	f, v := r4Dev(t, b.String()+"(u) "+req+"\n", req, req, yes)
	for _, x := range ledgerFindings(t, f, v.ID) {
		if !x.Low && !strings.Contains(x.Claim, "shall not:") {
			t.Errorf("a confirmed HIGH deviation lacks the list's \"shall not:\": %.300q", x.Claim)
		}
	}
}

// L3: "No." is no: the verdict is dropped, not admitted as unclear.
func TestV5CheckNoWithPeriodDrops(t *testing.T) {
	t.Parallel()
	memo := "Note\n\nDo NOT name Sophia Davenport-Chen as a successor trustee.\n"
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "No.", "reason": "the text forbids naming her"})})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 0 {
		t.Errorf("a check that answered \"No.\" admitted the verdict: %+v", fs)
	}
}

// L2: unclear on a truncated window is worded as unclear, not "agreed, but …".
func TestV5UnclearTruncatedSaysUnclear(t *testing.T) {
	t.Parallel()
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	big := "Note\n" + strings.Repeat("The settlor has considered the family's circumstances and the views of the family council. ", 120) + req + "\n" // in a 11 KB paragraph
	f, v := r4Dev(t, big, req, req, []model.Reply{text(m{"answer": "unclear", "reason": "cannot tell"})})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || !fs[0].Low || strings.Contains(fs[0].Claim, "agreed") || !strings.Contains(fs[0].Claim, "could not tell") {
		t.Errorf("an unclear check on a truncated window: %+v", fs)
	}
}
