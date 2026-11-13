// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs/ooxml"
	"github.com/discover-legal/BigLaw/internal/store"
)

// The track-changes child (README § Track changes): the .docx writer runs in the same bounded
// child as the reader (MaybeRunChild: deadline, memory cap, no environment, dies with its parent),
// under a second private argument. Its stdin is one JSON line (docxRequest) then the file; its
// stdout one JSON line (docxOutput: outcomes, revisions, a refusal, and the lengths that follow)
// then the output .docx, read through hard bounds (readDocxChild).

const docxArg = "__biglaw-docx"

// docxRequest is what the server sends the child.
type docxRequest struct {
	Verb        string // edits, compare (the same: apply the edits), accept, reject, revisions
	Author      string // the tracked changes' author
	Date        string // RFC 3339, the change set's creation time
	Text        string // the document's stored text, which the file's must still equal
	Edits       []Edit // edits and compare: located again against Text at every render
	RevisionIDs []int  // accept, reject (empty: all)
}

// docxOutput is the child's header line.
type docxOutput struct {
	Outcomes  []Outcome
	Revisions []docxRevision
	Err       string
	Len       int // bytes of .docx that follow
}

// docxRevision is one tracked change as /revisions lists it.
type docxRevision = ooxml.Revision

// maxDocxHeader bounds the child's header line (outcomes for 500 edits, or a long file's revisions).
const maxDocxHeader = 16 << 20

// runDocxChild is the child's side: read the request and the file, do the verb, write the answer.
func runDocxChild() {
	br := bufio.NewReaderSize(os.Stdin, 64<<10)
	line, err := io.ReadAll(io.LimitReader(&lineReader{br}, maxDocxHeader))
	var req docxRequest
	if err != nil || json.Unmarshal(line, &req) != nil {
		os.Exit(2)
	}
	data, err := io.ReadAll(io.LimitReader(br, store.MaxFileSize+1))
	if err != nil || len(data) > store.MaxFileSize {
		os.Exit(2)
	}
	out, docx := docxVerb(req, data)
	out.Len = len(docx)
	w := bufio.NewWriter(os.Stdout)
	if json.NewEncoder(w).Encode(out) != nil || func() error { w.Write(docx); return w.Flush() }() != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// lineReader reads up to and including the first newline, then EOF, leaving the rest in r.
type lineReader struct{ r *bufio.Reader }

func (l *lineReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := l.r.ReadByte()
		if err != nil {
			return n, err
		}
		p[n] = b
		n++
		if b == '\n' {
			return n, io.EOF
		}
	}
	return n, nil
}

// docxVerb does the verb on the file (in the child; pure, so it is tested in-process too).
func docxVerb(req docxRequest, data []byte) (docxOutput, []byte) {
	d, err := ooxml.OpenAll(data)
	if err != nil {
		return docxOutput{Err: ErrUnreadable.Error() + ": " + err.Error()}, nil
	}
	if req.Verb == "revisions" {
		revs, err := d.Revisions()
		if err != nil {
			return docxOutput{Err: ErrUnreadable.Error() + ": " + err.Error()}, nil
		}
		return docxOutput{Revisions: revs}, nil
	}
	when, err := time.Parse(time.RFC3339, req.Date)
	if err != nil {
		return docxOutput{Err: "docs: the change set's date is malformed"}, nil
	}
	text, err := d.Text()
	if err != nil {
		return docxOutput{Err: ErrUnreadable.Error() + ": " + err.Error()}, nil
	}
	var v ooxml.Visitor
	var outcomes []Outcome
	switch req.Verb {
	case KindEdits, KindCompare:
		if clean(text) != req.Text {
			outcomes = make([]Outcome, len(req.Edits))
			for i := range outcomes {
				outcomes[i] = Outcome{Status: StatusFailed, Reason: "text_drift: the file's text no longer equals the stored text; file the document again"}
			}
			return docxOutput{Outcomes: outcomes}, nil
		}
		runs, err := d.Runs()
		if err != nil {
			return docxOutput{Err: ErrUnreadable.Error() + ": " + err.Error()}, nil
		}
		v, outcomes = applyEdits(runs, req.Edits, locateAll(req.Text, req.Edits))
	case KindAccept, KindReject:
		v, err = d.Decide(req.Verb == KindAccept, req.RevisionIDs)
		if err != nil {
			return docxOutput{Err: ErrBadInput.Error() + ": " + strings.TrimPrefix(err.Error(), "ooxml: ")}, nil
		}
		revs, _ := d.Revisions()
		for _, r := range revs {
			if len(req.RevisionIDs) == 0 || containsAny(r.IDs, req.RevisionIDs) {
				outcomes = append(outcomes, Outcome{Status: StatusApplied, Method: req.Verb, Start: r.Start, End: r.End, Reason: fmt.Sprintf("%s %d (%s by %s)", req.Verb, r.ID, r.Kind, r.Author)})
			}
		}
	default:
		return docxOutput{Err: "docs: unknown verb"}, nil
	}
	if err := d.Walk(v, ooxml.NewRevisions(req.Author, when)); err != nil {
		return docxOutput{Err: ErrUnreadable.Error() + ": " + strings.TrimPrefix(err.Error(), "ooxml: ")}, nil
	}
	out, err := d.Bytes()
	if err != nil {
		return docxOutput{Err: ErrUnreadable.Error() + ": " + err.Error()}, nil
	}
	if outcomes == nil {
		outcomes = []Outcome{}
	}
	return docxOutput{Outcomes: outcomes}, out
}

func containsAny(ids, want []int) bool {
	for _, a := range ids {
		for _, b := range want {
			if a == b {
				return true
			}
		}
	}
	return false
}

// applyEdits plans the located edits over the runs and returns the visitor that applies them, with
// an outcome per edit: an edit not located at POST keeps its outcome; one whose text lies in a run
// the writer cannot edit (a field, a content control, a deleted run) fails with the reason; the
// rest are applied: each overlapped run gets its deletion, the replacement and the comment ride on
// the last one, a pure insertion lands in the run holding its point.
func applyEdits(runs []ooxml.Run, edits []Edit, located []Outcome) (ooxml.Visitor, []Outcome) {
	type key struct{ para, index int }
	ops := map[key][]ooxml.Op{}
	outcomes := make([]Outcome, len(edits))
	fail := func(i int, reason string) {
		outcomes[i] = Outcome{Status: StatusFailed, Method: located[i].Method, Start: located[i].Start, End: located[i].End, Reason: reason}
	}
	for i, e := range edits {
		if i >= len(located) || located[i].Status != StatusLocated {
			if i < len(located) {
				outcomes[i] = located[i]
			} else {
				outcomes[i] = Outcome{Status: StatusFailed, Reason: "never located"}
			}
			continue
		}
		start, end := located[i].Start, located[i].End
		var touched []ooxml.Run
		for _, r := range runs {
			if r.Index < 0 {
				if e.Kind != "" && r.Start == start {
					touched = append(touched, r)
				}
				continue
			}
			rEnd := r.Start + len(r.Text)
			switch {
			case e.Kind != "": // a paragraph edit: any run of the paragraph line [start, end)
				if r.Start >= start && r.Start < end || r.Start == start && start == end {
					touched = append(touched, r)
				}
			case start == end: // an insertion point: the run holding it (at its end: the one before)
				if start > r.Start && start <= rEnd || start == 0 && r.Start == 0 && len(touched) == 0 {
					touched = append(touched, r)
				}
			default:
				if r.Start < end && rEnd > start && rEnd > r.Start {
					touched = append(touched, r)
				}
			}
		}
		if e.Kind != "" && len(touched) > 1 {
			touched = touched[:1]
		}
		if len(touched) == 0 {
			fail(i, "the text's run was not found in the file")
			continue
		}
		if start == end && e.Kind == "" && len(touched) > 1 {
			touched = touched[:1]
		}
		skipped := ""
		for _, r := range touched {
			if r.Skip != "" && !(e.Kind != "" && r.Skip == "empty") {
				skipped = r.Skip
			}
		}
		if skipped != "" {
			fail(i, map[string]string{"field": "the text is a Word field (its code or result)", "sdt": "the text is in a content control", "move": "the text is in a tracked move",
				"complex": "the text's run holds a drawing, symbol or reference the writer cannot reproduce", "hidden": "the text is hidden", "deleted": "the text is already a tracked deletion", "empty": "the paragraph is empty"}[skipped])
			continue
		}
		covered := 0
		for k, r := range touched {
			kk := key{r.Para, r.Index}
			switch {
			case e.Kind == KindInsertParagraph:
				ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.InsertParagraphAfter, Text: e.Replace})
			case e.Kind == KindInsertParagraphBefore:
				ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.InsertParagraphBefore, Text: e.Replace})
			case e.Kind == KindDeleteParagraph:
				ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.DeleteParagraph})
			case start == end:
				if at := start - r.Start; at == len(r.Text) {
					ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.InsertAfter, Text: e.Replace})
				} else {
					ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.Replace, Start: at, End: at, Text: e.Replace})
				}
			default:
				s, en := max(start, r.Start)-r.Start, min(end, r.Start+len(r.Text))-r.Start
				covered += en - s
				op := ooxml.Op{Kind: ooxml.Delete, Start: s, End: en}
				if k == len(touched)-1 && e.Replace != "" {
					op.Kind, op.Text = ooxml.Replace, e.Replace
				}
				ops[kk] = append(ops[kk], op)
			}
			if k == len(touched)-1 && e.Comment != "" && r.Index >= 0 {
				ops[kk] = append(ops[kk], ooxml.Op{Kind: ooxml.Comment, Text: e.Comment})
			}
		}
		if e.Kind == "" && start < end && covered != end-start {
			fail(i, "the text spans a paragraph break")
			continue
		}
		outcomes[i] = Outcome{Status: StatusApplied, Method: located[i].Method, Start: start, End: end}
	}
	return func(r ooxml.Run) ([]ooxml.Op, error) { return ops[key{r.Para, r.Index}], nil }, outcomes
}

// docxInChild runs the verb on data in a child (as extractInChild runs parse) and returns its
// answer and the output .docx. A crash, a timeout or garbage output is ErrUnreadable.
func docxInChild(ctx context.Context, req docxRequest, data []byte) (docxOutput, []byte, error) {
	exe, err := selfExe()
	if !childReady {
		return docxOutput{}, nil, errNoChild
	} else if err != nil {
		return docxOutput{}, nil, fmt.Errorf("docs: file writer: %w", err)
	}
	header, err := json.Marshal(req)
	if err != nil {
		return docxOutput{}, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, childTimeout)
	defer cancel()
	cmd := childCommand(cctx, exe)
	cmd.Args[1] = docxArg
	cmd.Stdin = io.MultiReader(bytes.NewReader(header), strings.NewReader("\n"), bytes.NewReader(data))
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	var out docxOutput
	var docx []byte
	ok := false
	if err != nil && cctx.Err() == nil {
		return docxOutput{}, nil, fmt.Errorf("docs: the file writer could not start: %w", err)
	} else if err == nil {
		if out, docx, ok = readDocxChild(stdout); !ok {
			_ = cmd.Process.Kill()
		}
		err = cmd.Wait()
	}
	switch {
	case ctx.Err() != nil:
		return docxOutput{}, nil, ctx.Err()
	case cctx.Err() != nil:
		return docxOutput{}, nil, fmt.Errorf("%w: writing the file took longer than %s", ErrUnreadable, childTimeout)
	case err != nil || !ok:
		return docxOutput{}, nil, fmt.Errorf("%w: the file writer failed on this file (it may be damaged, hostile, or need more than %d MiB)", ErrUnreadable, childMemory>>20)
	case strings.HasPrefix(out.Err, ErrBadInput.Error()):
		return docxOutput{}, nil, fmt.Errorf("%w%s", ErrBadInput, strings.TrimPrefix(out.Err, ErrBadInput.Error()))
	case out.Err != "":
		return docxOutput{}, nil, fmt.Errorf("%w%s", ErrUnreadable, strings.TrimPrefix(out.Err, ErrUnreadable.Error()))
	}
	return out, docx, nil
}

// readDocxChild reads the child's output through hard bounds: a header line of at most
// maxDocxHeader, then exactly Len ≤ MaxFileSize bytes, then the end.
func readDocxChild(r io.Reader) (out docxOutput, docx []byte, ok bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	line, err := io.ReadAll(io.LimitReader(&lineReader{br}, maxDocxHeader))
	if err != nil || len(line) == 0 || line[len(line)-1] != '\n' || json.Unmarshal(line, &out) != nil || out.Len < 0 || out.Len > store.MaxFileSize {
		return out, nil, false
	}
	docx = make([]byte, out.Len)
	if _, err := io.ReadFull(br, docx); err != nil {
		return out, nil, false
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		return out, nil, false
	}
	return out, docx, true
}
