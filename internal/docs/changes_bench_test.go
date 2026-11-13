// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkApply150Edits (AC-19, recorded in the PR, not gated): 150 edits over a document whose
// word/document.xml is about 30 MB, rendered in-process (the child adds its start and the pipe).
func BenchmarkApply150Edits(b *testing.B) {
	var paras []string
	var text strings.Builder
	for i := range 200000 {
		p := fmt.Sprintf("%d. The Supplier shall deliver the Goods within %d days of the order, and Payment is due within 60 days of invoice (clause %d).", i, 30+i%7, i)
		paras = append(paras, p)
		text.WriteString(p + "\n")
	}
	data := minimalDocx(paras...)
	var edits []Edit
	for i := range 150 {
		edits = append(edits, Edit{Find: "60 days of invoice", Replace: "45 days of invoice", Paragraph: paras[i*1333]})
	}
	req := docxRequest{Verb: KindEdits, Author: "A", Date: "2026-11-03T10:00:00Z", Text: text.String(), Edits: edits}
	b.ReportMetric(float64(len(data))/1e6, "MB-docx")
	b.ResetTimer()
	for range b.N {
		out, docx := docxVerb(req, data)
		if out.Err != "" || len(docx) == 0 || out.Outcomes[149].Status != StatusApplied {
			b.Fatalf("%+v", out)
		}
	}
}
