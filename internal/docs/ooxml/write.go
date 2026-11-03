// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The writer (PR 3d): Word track changes by way of a visitor called once per run. OpenAll keeps
// every part of the archive; Walk scans word/document.xml, calls the caller's Visitor for each run
// with its text, offsets and formatting, and applies the ops it returns as <w:ins>/<w:del>
// tracked changes (comments, accepted or rejected revisions, paragraphs inserted or deleted); Bytes
// re-zips the archive with every untouched part byte-identical and in its original order. No model
// is involved: the visitor is Go, and the caller owns the decision (README § Track changes).

// part is one archive entry, byte for byte.
type part struct {
	name string
	data []byte
}

// OpenAll parses a .docx keeping every part (the writer's Open): the same decompression cap as Open
// applies to the whole archive. A document from Open cannot be written back (ErrReadOnly).
func OpenAll(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotDocx
	}
	d, total := &Document{parts: map[string]string{}}, int64(0)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: part %q cannot be read", ErrNotDocx, f.Name)
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxDecompressed-total+1))
		rc.Close()
		if total += int64(len(b)); total > maxDecompressed {
			return nil, fmt.Errorf("ooxml: refusing a .docx that decompresses to more than %d bytes", maxDecompressed)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: part %q is corrupt", ErrNotDocx, f.Name)
		}
		if _, ok := textParts[f.Name]; ok {
			d.parts[f.Name] = string(b)
		}
		d.raw = append(d.raw, part{f.Name, b})
	}
	if _, ok := d.parts["word/document.xml"]; !ok {
		return nil, ErrNotDocx
	}
	return d, nil
}

// ErrReadOnly: the document was opened with Open (text parts only), so it cannot be written.
var ErrReadOnly = errors.New("ooxml: the document was opened for reading only (use OpenAll to write)")

// Bytes re-zips the archive: parts in their original order, each untouched one byte-identical;
// the ones the writer changed replaced, the ones it added last. The zip carries no timestamps,
// so the same document written twice is the same bytes.
func (d *Document) Bytes() ([]byte, error) {
	if d.raw == nil {
		return nil, ErrReadOnly
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	for _, p := range d.raw {
		data := p.data
		if s, ok := d.changed[p.name]; ok {
			data = []byte(s)
		}
		if err := write(p.name, data); err != nil {
			return nil, err
		}
	}
	for _, name := range d.added {
		if err := write(name, []byte(d.changed[name])); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Part is a part's text as it stands (changed by the writer, or as read); ok is false when the
// archive has no such part.
func (d *Document) Part(name string) (text string, ok bool) {
	if s, ok := d.changed[name]; ok {
		return s, true
	}
	if s, ok := d.parts[name]; ok {
		return s, true
	}
	for _, p := range d.raw {
		if p.name == name {
			return string(p.data), true
		}
	}
	return "", false
}

// setPart replaces (or adds) a part's text.
func (d *Document) setPart(name, text string) {
	if d.changed == nil {
		d.changed = map[string]string{}
	}
	if _, ok := d.Part(name); !ok && !slices.Contains(d.added, name) {
		d.added = append(d.added, name)
	}
	d.changed[name] = text
	if _, ok := d.parts[name]; ok {
		d.parts[name] = text
	}
}

// Run is one text-bearing <w:r> as the visitor sees it. Start is a byte offset into Text() (the
// body's part of it); Text is the run's visible text, "" for a deleted run (its deleted words are
// in Deleted). Skip names why the writer leaves the run alone ("" when it may be edited): field (a
// Word field's code or result), sdt (a content control), move (a tracked move), complex (a child the
// writer cannot reproduce: a drawing, a symbol, a footnote reference), hidden, deleted, empty (a
// paragraph with no run at all is visited once as this). Revision is the tracked change the run
// sits in, Mark the paragraph mark's; nil for none.
type Run struct {
	Para       int
	Index      int // -1 for an empty paragraph's visit
	Text       string
	Deleted    string
	Start      int
	Props      string
	Skip       string
	Revision   *Revision
	Mark       *Revision
	LastInPara bool
}

// OpKind is what an Op does to the visited run.
type OpKind int

const (
	Keep                  OpKind = iota
	Replace                      // [Start, End) of Run.Text becomes Text (a tracked deletion and insertion)
	Delete                       // [Start, End) is tracked-deleted
	InsertBefore                 // Text is tracked-inserted before the run
	InsertAfter                  // Text is tracked-inserted after the run
	Comment                      // a Word comment with body Text on the run
	Accept                       // the run's Revision is accepted (its whole element, once)
	Reject                       // the run's Revision is rejected (its whole element, once)
	AcceptMark                   // the paragraph mark's revision is accepted (once per paragraph)
	RejectMark                   // the paragraph mark's revision is rejected (once per paragraph)
	InsertParagraphAfter         // a new paragraph with Text after the run's paragraph
	InsertParagraphBefore        // a new paragraph with Text before the run's paragraph
	DeleteParagraph              // the run's whole paragraph is tracked-deleted (once per paragraph)
)

// Op is one thing to do to the visited run. Start and End are byte offsets into Run.Text.
type Op struct {
	Kind       OpKind
	Start, End int
	Text       string
}

// Visitor is called once per run, in document order, and returns the ops for that run; nil keeps
// it. An error stops the walk and nothing is written.
type Visitor func(Run) ([]Op, error)

// Revisions issues the ids and attributes of the changes one Walk writes: one author, one date,
// ids from above the highest already in the document.
type Revisions struct {
	Author string
	When   time.Time
	next   int
}

// NewRevisions is the factory for author at when (normalised to UTC). Ids start where Walk sets
// them (above the document's highest w:id).
func NewRevisions(author string, when time.Time) *Revisions {
	return &Revisions{Author: author, When: when.UTC()}
}

func (rv *Revisions) id() int {
	rv.next++
	return rv.next - 1
}

func (rv *Revisions) attrs() string {
	return `w:id="` + strconv.Itoa(rv.id()) + `" ` + rv.who()
}

func (rv *Revisions) who() string {
	return `w:author="` + strings.ReplaceAll(Escape(rv.Author), `"`, "&quot;") + `" w:date="` + rv.When.Format(time.RFC3339) + `"`
}

// ins renders text as a tracked insertion in a run with props.
func (rv *Revisions) ins(text, props string) string {
	return "<w:ins " + rv.attrs() + ">" + runXML(text, props, false) + "</w:ins>"
}

// del renders text as a tracked deletion.
func (rv *Revisions) del(text, props string) string {
	return "<w:del " + rv.attrs() + ">" + runXML(text, props, true) + "</w:del>"
}

// runXML renders text as one run: tabs, line breaks and non-breaking hyphens as their elements,
// the rest in <w:t> (or <w:delText>) with its space preserved. Empty text renders nothing.
func runXML(text, props string, deleted bool) string {
	if text == "" {
		return ""
	}
	tag := "w:t"
	if deleted {
		tag = "w:delText"
	}
	var b strings.Builder
	b.WriteString("<w:r>" + props)
	flush := func(s string) {
		if s != "" {
			b.WriteString("<" + tag + ` xml:space="preserve">` + Escape(s) + "</" + tag + ">")
		}
	}
	last := 0
	for i, r := range text {
		var el string
		switch r {
		case '\t':
			el = "<w:tab/>"
		case '\n':
			el = "<w:br/>"
		case '‑':
			el = "<w:noBreakHyphen/>"
		default:
			continue
		}
		flush(text[last:i])
		b.WriteString(el)
		last = i + len(string(r))
	}
	flush(text[last:])
	b.WriteString("</w:r>")
	return b.String()
}

// Revision is one tracked change as Walk's visitor and Revisions() report it.
type Revision struct {
	ID       int    `json:"id"`
	Kind     string `json:"kind"` // insertion, deletion, substitution, paragraph_insertion, paragraph_deletion
	Author   string `json:"author"`
	Date     string `json:"date,omitempty"`
	Inserted string `json:"inserted,omitempty"`
	Deleted  string `json:"deleted,omitempty"`
	Start    int    `json:"start"` // byte offsets into Text(): the inserted text's span, or the deletion point
	End      int    `json:"end"`
	Before   string `json:"before"` // up to 40 characters of untouched text on each side
	After    string `json:"after"`
	IDs      []int  `json:"ids"` // every w:ins/w:del element it coalesces (accept or reject by any)
}

// splice is one replacement of a span of the part.
type splice struct {
	start, end int
	text       string
}

// walker holds one Walk's state.
type walker struct {
	d        *Document
	src      string
	ix       *index
	rv       *Revisions
	splices  []splice
	elements map[*revEl]OpKind // Accept/Reject by element, once
	paraOps  map[int]*paraOp
	comments []comment
}

type paraOp struct {
	insertAfter, insertBefore []string
	deleteIt                  bool
	props                     string // the asking run's, for an inserted paragraph's text
}

// Runs is what Walk would visit, in order, without writing anything: the caller's visitor can
// plan across runs (an edit spanning several) before the walk.
func (d *Document) Runs() ([]Run, error) {
	var out []Run
	src, _ := d.Part("word/document.xml")
	ix, err := scan(src)
	if err != nil {
		return nil, err
	}
	visit(ix, func(r Run) ([]Op, error) { out = append(out, r); return nil, nil })
	return out, nil
}

// visit calls v for each run of ix in order (an empty paragraph once, Index -1) and returns the
// non-empty answers with the run they are for (-1 for an empty paragraph).
func visit(ix *index, v Visitor) ([]visited, error) {
	var all []visited
	for pi := range ix.paras {
		p := &ix.paras[pi]
		var mark *Revision
		if p.markRev != nil {
			mark = &Revision{ID: p.markRev.id, Kind: map[string]string{"ins": "paragraph_insertion", "del": "paragraph_deletion"}[p.markRev.kind], Author: p.markRev.author, Date: p.markRev.date, IDs: []int{p.markRev.id}}
		}
		if p.nRuns == 0 {
			ops, err := v(Run{Para: pi, Index: -1, Start: p.textStart, Skip: "empty", Mark: mark, LastInPara: true})
			if err != nil {
				return nil, err
			}
			if len(ops) > 0 {
				all = append(all, visited{-1, pi, ops})
			}
			continue
		}
		for i := p.firstRun; i < p.firstRun+p.nRuns; i++ {
			r := &ix.runs[i]
			pub := Run{Para: pi, Index: i - p.firstRun, Text: r.text, Deleted: r.delText, Start: r.docStart, Props: r.props, Skip: r.skip, Mark: mark, LastInPara: i == p.firstRun+p.nRuns-1}
			if r.rev != nil {
				pub.Revision = &Revision{ID: r.rev.id, Kind: map[string]string{"ins": "insertion", "del": "deletion"}[r.rev.kind], Author: r.rev.author, Date: r.rev.date, IDs: []int{r.rev.id}}
			}
			ops, err := v(pub)
			if err != nil {
				return nil, err
			}
			if len(ops) > 0 {
				all = append(all, visited{i, pi, ops})
			}
		}
	}
	return all, nil
}

type visited struct {
	run  int // -1: an empty paragraph
	para int
	ops  []Op
}

// Walk scans the document, calls v for each run in order (an empty paragraph once, as a run with
// Skip "empty"), then applies every op last run first (so no op shifts another's offsets) and
// switches Track Changes on in settings.xml. The visitor's errors, and an op the run cannot take
// (an edit of a skipped run, Accept of a plain run, a span out of range, two ops on one span), are
// returned and nothing is changed.
func (d *Document) Walk(v Visitor, rv *Revisions) error {
	if d.raw == nil {
		return ErrReadOnly
	}
	src, _ := d.Part("word/document.xml")
	ix, err := scan(src)
	if err != nil {
		return err
	}
	rv.next = max(rv.next, ix.maxID+1, 1)
	w := &walker{d: d, src: src, ix: ix, rv: rv, elements: map[*revEl]OpKind{}, paraOps: map[int]*paraOp{}}
	all, err := visit(ix, v)
	if err != nil {
		return err
	}
	for _, x := range all {
		if err := w.runOps(x.run, x.para, x.ops); err != nil {
			return fmt.Errorf("ooxml: paragraph %d, run %d: %w", x.para, x.run-ix.paras[x.para].firstRun, err)
		}
	}
	w.paragraphOps()
	w.elementOps()
	if err := w.apply(); err != nil {
		return err
	}
	if err := w.markOps(); err != nil {
		return err
	}
	if len(w.comments) > 0 {
		d.addComments(w.comments, rv)
	}
	d.trackRevisions()
	return nil
}

// runOps turns one run's ops into splices (ri -1: an empty paragraph's visit).
func (w *walker) runOps(ri, pi int, ops []Op) error {
	var r *run
	if ri >= 0 {
		r = &w.ix.runs[ri]
	}
	var edits, comments []Op
	var before, after []string
	for _, op := range ops {
		switch op.Kind {
		case Keep:
		case Replace, Delete, Comment:
			if r == nil || r.skip != "" {
				return fmt.Errorf("the run cannot be edited (%s)", cmpStr(r == nil, "empty", skipOf(r)))
			}
			if op.Start < 0 || op.End > len(r.text) || op.Start > op.End || op.Kind != Comment && op.Start == op.End && op.Text == "" {
				return fmt.Errorf("span [%d, %d) is not within the run's %d bytes", op.Start, op.End, len(r.text))
			}
			if op.Kind == Comment {
				comments = append(comments, op)
			} else {
				edits = append(edits, op)
			}
		case InsertBefore, InsertAfter:
			if r == nil || r.skip != "" && r.skip != "deleted" {
				return fmt.Errorf("the run cannot be edited (%s)", cmpStr(r == nil, "empty", skipOf(r)))
			}
			if op.Text == "" {
				return errors.New("an insertion of nothing")
			}
			if op.Kind == InsertBefore {
				before = append(before, op.Text)
			} else {
				after = append(after, op.Text)
			}
		case Accept, Reject:
			if r == nil || r.rev == nil {
				return errors.New("the run is not inside a tracked change")
			}
			if k, ok := w.elements[r.rev]; ok && k != op.Kind {
				return errors.New("the tracked change is both accepted and rejected")
			}
			w.elements[r.rev] = op.Kind
		case AcceptMark, RejectMark:
			m := w.ix.paras[pi].markRev
			if m == nil {
				return errors.New("the paragraph mark is not a tracked change")
			}
			k := map[OpKind]OpKind{AcceptMark: Accept, RejectMark: Reject}[op.Kind]
			if prev, ok := w.elements[m]; ok && prev != k {
				return errors.New("the paragraph mark's change is both accepted and rejected")
			}
			w.elements[m] = k
		case InsertParagraphAfter, InsertParagraphBefore, DeleteParagraph:
			po := w.paraOps[pi]
			if po == nil {
				po = &paraOp{}
				if r != nil {
					po.props = r.props
				}
				w.paraOps[pi] = po
			}
			switch {
			case op.Kind == DeleteParagraph:
				po.deleteIt = true
			case op.Text == "":
				return errors.New("an insertion of nothing")
			case op.Kind == InsertParagraphAfter:
				po.insertAfter = append(po.insertAfter, op.Text)
			default:
				po.insertBefore = append(po.insertBefore, op.Text)
			}
		default:
			return fmt.Errorf("unknown op %d", op.Kind)
		}
	}
	if len(edits) == 0 && len(before) == 0 && len(after) == 0 && len(comments) == 0 {
		return nil
	}
	sort.SliceStable(edits, func(a, b int) bool { return edits[a].Start < edits[b].Start })
	for k := 1; k < len(edits); k++ {
		if edits[k].Start < edits[k-1].End {
			return fmt.Errorf("spans [%d, %d) and [%d, %d) overlap", edits[k-1].Start, edits[k-1].End, edits[k].Start, edits[k].End)
		}
	}
	// Rewrite the run: kept slices as plain runs, each edit as <w:del> + <w:ins>, insertions at the
	// ends. Inside another author's <w:ins> or <w:del>, BigLaw's changes close and reopen it, so no
	// revision nests in another. Comments wrap the whole rewritten run.
	var b strings.Builder
	closeOuter, openOuter := "", ""
	if r.rev != nil {
		closeOuter, openOuter = "</w:"+r.rev.kind+">", r.rev.open
	}
	tracked := func(s string) { b.WriteString(closeOuter + s + openOuter) }
	for _, t := range before {
		tracked(w.rv.ins(t, r.props))
	}
	for _, c := range comments {
		b.WriteString(`<w:commentRangeStart w:id="` + strconv.Itoa(w.comment(c.Text)) + `"/>`)
	}
	if r.skip == "deleted" {
		b.WriteString(w.src[r.xmlStart:r.xmlEnd]) // reproduced as it stands
	} else {
		last := 0
		for _, e := range edits {
			b.WriteString(runXML(r.text[last:e.Start], r.props, false))
			var s string
			if e.End > e.Start {
				s += w.rv.del(r.text[e.Start:e.End], r.props)
			}
			if e.Kind == Replace && e.Text != "" {
				s += w.rv.ins(e.Text, r.props)
			}
			tracked(s)
			last = e.End
		}
		b.WriteString(runXML(r.text[last:], r.props, false))
	}
	for k := range comments {
		id := strconv.Itoa(w.comments[len(w.comments)-len(comments)+k].id)
		b.WriteString(`<w:commentRangeEnd w:id="` + id + `"/><w:r>` + r.props + `<w:commentReference w:id="` + id + `"/></w:r>`)
	}
	for _, t := range after {
		tracked(w.rv.ins(t, r.props))
	}
	out, start, end := b.String(), r.xmlStart, r.xmlEnd
	if r.rev != nil {
		out = strings.ReplaceAll(out, openOuter+closeOuter, "")           // nothing of the other author's element between two of ours
		if r.rev.openEnd == start && strings.HasPrefix(out, closeOuter) { // the element's first run: no empty element before ours
			out, start = strings.TrimPrefix(out, closeOuter), r.rev.xmlStart
		}
		if r.rev.closeStart == end && strings.HasSuffix(out, openOuter) { // its last run: none after
			out, end = strings.TrimSuffix(out, openOuter), r.rev.xmlEnd
		}
	}
	w.splices = append(w.splices, splice{start, end, out})
	return nil
}

func skipOf(r *run) string {
	if r == nil {
		return "empty"
	}
	return r.skip
}

func cmpStr(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// paragraphOps turns the paragraph ops into splices: a deleted paragraph has every run
// tracked-deleted and its mark marked; an inserted paragraph follows its anchor with the anchor's
// properties (numbering, indentation; never its section break) and a marked mark.
func (w *walker) paragraphOps() {
	for _, pi := range slices.Sorted(maps.Keys(w.paraOps)) {
		po, p := w.paraOps[pi], w.ix.paras[pi]
		if po.deleteIt {
			var b strings.Builder
			b.WriteString(w.src[p.xmlStart:p.openEnd] + markedPPr(p.pPr, "<w:del "+w.rv.attrs()+"/>"))
			var open *revEl // another author's element the runs being written sit in
			for i := p.firstRun; i < p.firstRun+p.nRuns; i++ {
				r := w.ix.runs[i]
				if r.rev != open {
					if open != nil {
						b.WriteString("</w:" + open.kind + ">")
					}
					if open = r.rev; open != nil {
						b.WriteString(open.open)
					}
				}
				switch {
				case r.rev != nil && r.rev.kind == "del":
					b.WriteString(w.src[r.xmlStart:r.xmlEnd]) // already deleted: as it stands
				case r.skip == "" || r.skip == "hidden":
					b.WriteString("<w:del " + w.rv.attrs() + ">" + runXML(r.text, r.props, true) + "</w:del>")
				default: // reproduced as it is, its text marked deleted
					b.WriteString("<w:del " + w.rv.attrs() + ">" + strings.NewReplacer("<w:t>", "<w:delText>", "<w:t ", "<w:delText ", "</w:t>", "</w:delText>").Replace(w.src[r.xmlStart:r.xmlEnd]) + "</w:del>")
				}
			}
			if open != nil {
				b.WriteString("</w:" + open.kind + ">")
			}
			b.WriteString("</w:p>")
			w.splices = append(w.splices, splice{p.xmlStart, p.xmlEnd, b.String()})
		}
		for _, ins := range []struct {
			texts []string
			at    int
		}{{po.insertBefore, p.xmlStart}, {po.insertAfter, p.xmlEnd}} {
			if len(ins.texts) == 0 {
				continue
			}
			var b strings.Builder
			for _, text := range ins.texts {
				b.WriteString("<w:p>" + markedPPr(p.pPr, "<w:ins "+w.rv.attrs()+"/>") + w.rv.ins(text, po.props) + "</w:p>")
			}
			w.splices = append(w.splices, splice{ins.at, ins.at, b.String()})
		}
	}
}

// markedPPr is pPr with mark inside its run properties (added when absent), existing marks, a
// section break and a properties revision dropped.
func markedPPr(pPr, mark string) string {
	pPr = dropElement(dropElement(pPr, "w:sectPr"), "w:pPrChange")
	if pPr == "" {
		return "<w:pPr><w:rPr>" + mark + "</w:rPr></w:pPr>"
	}
	i := strings.Index(pPr, "<w:rPr")
	if i < 0 {
		return strings.TrimSuffix(pPr, "</w:pPr>") + "<w:rPr>" + mark + "</w:rPr></w:pPr>"
	}
	j := strings.Index(pPr[i:], ">")
	if j < 0 {
		return pPr
	}
	if strings.HasSuffix(pPr[i:i+j+1], "/>") {
		return pPr[:i] + "<w:rPr>" + mark + "</w:rPr>" + pPr[i+j+1:]
	}
	rest := pPr[i+j+1:]
	for _, tag := range []string{"<w:ins ", "<w:del "} {
		for k := strings.Index(rest, tag); k >= 0; k = strings.Index(rest, tag) {
			e := strings.Index(rest[k:], "/>")
			if e < 0 {
				break
			}
			rest = rest[:k] + rest[k+e+2:]
		}
	}
	return pPr[:i+j+1] + mark + rest
}

// dropElement removes every <tag …>…</tag> or <tag …/> from s (tag never nests in itself).
func dropElement(s, tag string) string {
	return dropRe[tag].ReplaceAllString(s, "")
}

var dropRe = map[string]*regexp.Regexp{"w:sectPr": elementRe("w:sectPr"), "w:pPrChange": elementRe("w:pPrChange")}

func elementRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<` + tag + `(?:\s[^>]*)?/>|<` + tag + `(?:\s[^>]*)?>.*?</` + tag + `>`)
}

// elementOps turns the accepted and rejected tracked changes into splices: accepting an insertion
// or rejecting a deletion unwraps the element (deleted text becomes text); accepting a deletion or
// rejecting an insertion removes it. A paragraph mark's change removes its marker; accepting a
// deleted mark or rejecting an inserted one also joins the paragraph with the next.
func (w *walker) elementOps() {
	for e, k := range w.elements {
		if e.mark {
			continue
		}
		if keep := e.kind == "ins" && k == Accept || e.kind == "del" && k == Reject; !keep {
			w.splices = append(w.splices, splice{e.xmlStart, e.xmlEnd, ""})
			continue
		}
		inner := w.src[e.openEnd:e.closeStart]
		if e.kind == "del" {
			inner = strings.NewReplacer("<w:delText>", "<w:t>", "<w:delText ", "<w:t ", "</w:delText>", "</w:t>").Replace(inner)
		}
		w.splices = append(w.splices, splice{e.xmlStart, e.xmlEnd, inner})
	}
}

// markOps is the second pass, over the document as the first left it: a paragraph mark's change
// removes its marker; accepting a deleted mark or rejecting an inserted one joins the paragraph
// with the next (the next one's properties, as Word does), or, for a last paragraph with nothing
// left in it, removes it.
func (w *walker) markOps() error {
	decided := map[int]OpKind{}
	for e, k := range w.elements {
		if e.mark {
			decided[e.id] = k
		}
	}
	if len(decided) == 0 {
		return nil
	}
	src, _ := w.d.Part("word/document.xml")
	ix, err := scan(src)
	if err != nil {
		return err
	}
	w.src, w.ix, w.splices = src, ix, nil
	for pi := range ix.paras {
		p, m := ix.paras[pi], ix.paras[pi].markRev
		if m == nil {
			continue
		}
		k, ok := decided[m.id]
		if !ok {
			continue
		}
		join := m.kind == "del" && k == Accept || m.kind == "ins" && k == Reject
		next := pi + 1
		switch {
		case !join:
			w.splices = append(w.splices, splice{m.xmlStart, m.xmlEnd, ""})
		case next < len(ix.paras) && ix.paras[next].cell == p.cell && strings.TrimSpace(src[p.xmlEnd:ix.paras[next].xmlStart]) == "":
			n := ix.paras[next]
			w.splices = append(w.splices, splice{p.xmlStart, n.xmlEnd, src[p.xmlStart:p.openEnd] + n.pPr + src[p.pPrEnd:p.xmlEnd-len("</w:p>")] + src[n.pPrEnd:n.xmlEnd]})
		case p.nRuns == 0 && pi > 0:
			w.splices = append(w.splices, splice{p.xmlStart, p.xmlEnd, ""})
		default:
			w.splices = append(w.splices, splice{m.xmlStart, m.xmlEnd, ""})
		}
	}
	return w.apply()
}

// apply replaces the spliced spans in one pass over the part (never a copy per splice: 150
// edits on a 30 MB part would copy 4.5 GB), refusing any two that overlap.
func (w *walker) apply() error {
	if len(w.splices) == 0 {
		return nil
	}
	sort.SliceStable(w.splices, func(a, b int) bool {
		return w.splices[a].start < w.splices[b].start || w.splices[a].start == w.splices[b].start && w.splices[a].end < w.splices[b].end
	})
	var out strings.Builder
	out.Grow(len(w.src) + len(w.src)/8)
	at := 0
	for _, s := range w.splices {
		if s.start < at {
			return errors.New("ooxml: two changes overlap in the document")
		}
		out.WriteString(w.src[at:s.start])
		out.WriteString(s.text)
		at = s.end
	}
	out.WriteString(w.src[at:])
	w.d.setPart("word/document.xml", out.String())
	return nil
}

// trackRevisions switches Track Changes on in word/settings.xml (added if absent, left alone if
// present), so the recipient's further edits are tracked too.
func (d *Document) trackRevisions() {
	s, ok := d.Part("word/settings.xml")
	if !ok {
		d.setPart("word/settings.xml", xmlHeader+`<w:settings xmlns:w="`+wml+`"><w:trackRevisions/></w:settings>`)
		d.relate("settings.xml", "http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings", "application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml")
		return
	}
	if strings.Contains(s, "trackRevisions") {
		return
	}
	i := strings.Index(s, "<w:settings")
	j := 0
	if i >= 0 {
		j = strings.Index(s[i:], ">")
	}
	if i < 0 || j < 0 {
		return
	}
	if strings.HasSuffix(s[i:i+j+1], "/>") {
		d.setPart("word/settings.xml", s[:i]+strings.TrimSuffix(s[i:i+j+1], "/>")+"><w:trackRevisions/></w:settings>"+s[i+j+1:])
		return
	}
	d.setPart("word/settings.xml", s[:i+j+1]+"<w:trackRevisions/>"+s[i+j+1:])
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// relate adds a part under word/ to the document's relationships and the content types, once.
func (d *Document) relate(target, relType, contentType string) {
	rels, ok := d.Part("word/_rels/document.xml.rels")
	if !ok {
		rels = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`
	}
	if !strings.Contains(rels, `Target="`+target+`"`) {
		n := 0
		for _, f := range strings.Split(rels, `Id="rId`)[1:] {
			if m, err := strconv.Atoi(strings.SplitN(f, `"`, 2)[0]); err == nil {
				n = max(n, m)
			}
		}
		if i := strings.LastIndex(rels, "</Relationships>"); i >= 0 {
			rels = rels[:i] + `<Relationship Id="rId` + strconv.Itoa(n+1) + `" Type="` + relType + `" Target="` + target + `"/>` + rels[i:]
		}
		d.setPart("word/_rels/document.xml.rels", rels)
	}
	ct, ok := d.Part("[Content_Types].xml")
	if !ok {
		ct = xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`
	}
	if !strings.Contains(ct, `PartName="/word/`+target+`"`) {
		if i := strings.LastIndex(ct, "</Types>"); i >= 0 {
			d.setPart("[Content_Types].xml", ct[:i]+`<Override PartName="/word/`+target+`" ContentType="`+contentType+`"/>`+ct[i:])
		}
	}
}
