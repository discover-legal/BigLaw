// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// TestRetrievePagesByType (owner decision, lab-r3 r4: tool outputs are bounded by type, never
// trimmed): outline, search and section each fill one page of at most agent.PageBytes past their
// budget and say how to get the rest; walking outline's next reaches every section exactly once.
func TestRetrievePagesByType(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var b strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&b, "%d. Clause %d on Smith & Jones <fees>\n%s\n", i, i, strings.Repeat("The retainer \"é\" is payable by Smith & Jones. ", 40))
	}
	d := ingestText(t, s, "long.txt", b.String())
	r := tool(t, s, nil, "retrieve")
	out, _ := agent.Compile(r.Spec().Out)
	call := func(in string) (page struct {
		Hits, Outline []json.RawMessage
		Section       *struct{ Truncated bool }
		Truncated     bool
		Next          *int
	}) {
		t.Helper()
		res, err := r.Call(t.Context(), agent.Call{DocumentIDs: []string{d.ID}, Input: json.RawMessage(in)})
		if err != nil || out.Validate(res.Output) != nil || len(res.Output) > agent.PageBytes || json.Unmarshal(res.Output, &page) != nil {
			t.Fatalf("%s: %d bytes (page %d), err %v, schema %v", in, len(res.Output), agent.PageBytes, err, out.Validate(res.Output))
		}
		return page
	}
	seen, off := 0, 0
	for pages := 0; ; pages++ {
		p := call(fmt.Sprintf(`{"op":"outline","query":null,"documentId":%q,"sectionId":null,"k":null,"offset":%d,"limit":null}`, d.ID, off))
		if seen += len(p.Outline); p.Next == nil || pages > 100 {
			break
		} else if !p.Truncated || *p.Next != off+len(p.Outline) {
			t.Fatalf("outline page at %d: truncated %v, next %d", off, p.Truncated, *p.Next)
		}
		off = *p.Next
	}
	if seen != d.Sections {
		t.Errorf("outline pages listed %d sections, the document has %d", seen, d.Sections)
	}
	if p := call(`{"op":"search","query":"retainer payable","documentId":null,"sectionId":null,"k":8,"offset":null,"limit":null}`); !p.Truncated || p.Next == nil || *p.Next != len(p.Hits) || len(p.Hits) == 0 {
		t.Errorf("search past the page: %d hits, truncated %v, next %v", len(p.Hits), p.Truncated, p.Next)
	}
	if p := call(fmt.Sprintf(`{"op":"section","query":null,"documentId":%q,"sectionId":"s3","k":null,"offset":0,"limit":8000}`, d.ID)); p.Section == nil {
		t.Error("section: no page")
	}
}

// TestWrappedLinesAreNotHeadings (lab-r3 r4 verifier H2): a PDF-wrapped line that starts with a
// cross-reference or a street number is body text; real headings of the same schemes still parse.
func TestWrappedLinesAreNotHeadings(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{"Article 4 of the Trust, name Sophia Davenport-Chen as a successor trustee.", false},
		{"Section 9, name Sophia Davenport-Chen as a successor trustee.", false},
		{"§ 5 of the Trust Code, name Sophia Davenport-Chen.", false},
		{"Schedule A, which lists the patents, is attached.", false},
		{"12 Elm Street, Springfield, as a successor trustee.", false},
		{"200 California Street, Suite 1050, San Francisco", false},
		{"ARTICLE IV TRUSTEES", true},
		{"Article 4. Successor Trustees", true},
		{"Section 5.1: Notices", true},
		{"§ 5 Notices", true},
		{"**Schedule A** — see the patent schedule circulated with this draft.", true},
		{"Schedule B", true},
		{"12 Indemnification", true},
		{"12 Court Procedures", true},
		{"12. Elm Street Lease", true},
		{"4.2 Name Sophia Davenport-Chen as a successor trustee.", true},
	} {
		if _, got := classify(c.line); got != c.want {
			t.Errorf("%q: heading %v, want %v", c.line, got, c.want)
		}
	}
}
