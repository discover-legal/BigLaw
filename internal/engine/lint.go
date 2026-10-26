// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// figures lists the dates (as date keys) and numbers (spans as written) text states, as code reads
// them literally (analysis.Literal): dates first, their digits no numbers. Single digits ("Rule
// 6(a)(1)", "paragraph 3") count only when all is set.
func figures(text string, all bool) (ds []string, nums []analysis.Span) {
	ds, ss := analysis.Literal(text)
	for _, s := range ss {
		if all || !s.Single() {
			nums = append(nums, s)
		}
	}
	return ds, nums
}

// spanStated reports whether a ground span states n as written: one of n's readings is one of the
// ground span's, and a minus n writes is written there too (a memo's positive figure may state a
// ground "(3,000)"; the figure reader's labels decide what either means, lint's labelled check).
func spanStated(n analysis.Span, gn []analysis.Span) bool {
	for _, g := range gn {
		for _, x := range n.Readings() {
			for _, y := range g.Readings() {
				if x.Cmp(y) == 0 || !n.Neg() && new(big.Rat).Neg(x).Cmp(y) == 0 {
					return true
				}
			}
		}
	}
	return false
}

// decimalOf is a span's first reading as a decimal ("4217", "-3000"), how a lint line names it.
func decimalOf(n analysis.Span) string { return analysis.Decimal(n.Readings()[0]) }

// Untraced lists what text states that ground does not, literally: dates no ground date matches at
// their common precision (a ground date's year also states that year), and numbers no ground span
// states as written (spanStated). derived also accepts a sum or difference of two ground numbers,
// or a sum of three (a total or delta of stated figures, not a new one).
func Untraced(text, ground string, derived bool) []string {
	gd, gn := figures(ground, true)
	td, tn := figures(text, false)
	var out []string
	for _, k := range td {
		d, _ := analysis.ParseDate(k)
		if !slices.ContainsFunc(gd, func(g string) bool { x, _ := analysis.ParseDate(g); return x.Compatible(d) }) {
			out = append(out, k)
		}
	}
	vals := make([]float64, 0, len(gn))
	for _, g := range gn {
		if len(vals) < 60 {
			f, _ := g.Readings()[0].Float64()
			vals = append(vals, f)
		}
	}
	for _, n := range tn {
		if spanStated(n, gn) || yearOf(n, gd) {
			continue
		}
		f, _ := n.Readings()[0].Float64()
		if !derived || !derivable(f, vals) {
			out = append(out, decimalOf(n))
		}
	}
	return out
}

// dayIn reports whether n is the day of a date para writes without its year ("to November 25"):
// the month's name right before it, and a ground date of that month and day.
func dayIn(para string, n analysis.Span, ds []analysis.Date) bool {
	words := strings.Fields(para[:n.Start])
	if len(words) == 0 {
		return false
	}
	d, ok := analysis.ParseDate(words[len(words)-1] + " " + n.Text + ", 2000")
	return ok && d.Day != 0 && slices.ContainsFunc(ds, func(x analysis.Date) bool { return x.Month == d.Month && x.Day == d.Day })
}

// yearIn is yearOf over parsed dates.
func yearIn(n analysis.Span, ds []analysis.Date) bool {
	return slices.ContainsFunc(ds, func(d analysis.Date) bool { return n.Text == strconv.Itoa(d.Year) })
}

// yearOf reports whether n is the year of one of the dates ds ("in 2024" beside "March 3, 2024").
func yearOf(n analysis.Span, ds []string) bool {
	return slices.ContainsFunc(ds, func(k string) bool { d, _ := analysis.ParseDate(k); return n.Text == strconv.Itoa(d.Year) })
}

func derivable(want float64, vals []float64) bool {
	eq := func(a float64) bool { return a-want < 1e-6 && want-a < 1e-6 }
	for i := range vals {
		for j := i + 1; j < len(vals); j++ {
			if eq(vals[i]+vals[j]) || eq(vals[i]-vals[j]) || eq(vals[j]-vals[i]) {
				return true
			}
			for k := j + 1; k < len(vals); k++ {
				if eq(vals[i] + vals[j] + vals[k]) {
					return true
				}
			}
		}
	}
	return false
}

// covered reports whether ground states every date and number in value as written (at least one).
func covered(value, ground string) bool { return coveredIn(value, groundOf(ground)) }

// coveredIn is covered over a ground parsed once.
func coveredIn(value string, g traceGround) bool {
	vd, vn := figures(value, true)
	if len(vd)+len(vn) == 0 {
		return false
	}
	for _, d := range vd {
		x, _ := analysis.ParseDate(d)
		if !slices.ContainsFunc(g.dates, func(y analysis.Date) bool { return y.Compatible(x) }) {
			return false
		}
	}
	for _, n := range vn {
		if !spanStated(n, g.nums) && !yearIn(n, g.dates) {
			return false
		}
	}
	return true
}

var quotedRe = regexp.MustCompile(`“[^”]*”|"[^"\n]*"`)

// outsideQuotes applies f to the parts of text outside quotation marks: a quoted document passage
// is the source's words and is never rewritten (a {Codename} inside one stays as written).
func outsideQuotes(text string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range quotedRe.FindAllStringIndex(text, -1) {
		b.WriteString(f(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(text[last:]))
	return b.String()
}

func handlesOf(figs []Fig) []analysis.Handle {
	hs := make([]analysis.Handle, len(figs))
	for i, f := range figs {
		hs[i] = analysis.Handle{Handle: f.Handle, FID: f.FID, Value: f.Value}
	}
	return hs
}

// substituteOutsideQuotes replaces the drafter's handles with their exact values outside quotes,
// from the handle set the drafter was given; an unknown handle stays as written (the lint names it).
func substituteOutsideQuotes(text string, figs []Fig) string {
	return outsideQuotes(text, func(s string) string {
		out, _ := analysis.Substitute(s, handlesOf(figs)) // an unknown handle stays as written; the rest are substituted
		return out
	})
}

// lint is the deliverable's integrity check over one section (G5, G8): every [F-id] names a
// usable finding, every handle is known, and every figure traces to a cited finding's quotes or
// values, its handle, or a calc worksheet (its result or its inputs). A rule or section number is
// a locator, not a figure. Each untraced figure says how to fix it (hint).
func lint(issueID string, s Section, usable map[string]Finding, sheets []analysis.Worksheet) []string {
	out, _, _, _ := lintSection(issueID, s, usable, sheets, false)
	return out
}

// lintLimited is lint, the figures it limited (by why: limitedUnread, limitedScale) instead of
// tracing them, and the cited findings to gate for them (by why): finish discloses the figures in
// Limitations and gates each finding whose never-read quote is all a figure traces to, or that states
// a limited figure at the other scale (lab-r4 r5 H2, r6 M2, r7 M4: a figure no clear reading verifies
// is flagged, or limited and gated, never silently traced).
func lintLimited(issueID string, s Section, usable map[string]Finding, sheets []analysis.Worksheet) (out []string, limited map[string][]string, gate map[string]string, computed []computedFig) {
	return lintSection(issueID, s, usable, sheets, true)
}

// lintSection is lintLimited; attribute also finds what each figure rests on (the Computed figures
// block, and the gates its ⚠ inputs set), which lint (the redraft check) skips (lab-r4 r11 M).
func lintSection(issueID string, s Section, usable map[string]Finding, sheets []analysis.Worksheet, attribute bool) (out []string, limited map[string][]string, gate map[string]string, computed []computedFig) {
	limited, gate = map[string][]string{}, map[string]string{}
	cited := map[string]bool{}
	for _, m := range fidRe.FindAllStringSubmatch(s.Markdown, -1) {
		if _, ok := usable[m[1]]; !ok && !cited[m[1]] {
			out = append(out, issueID+": ["+m[1]+"] is not a verified finding of this task")
		}
		cited[m[1]] = true
	}
	unknown := false
	_ = outsideQuotes(s.Markdown, func(x string) string {
		if _, err := analysis.Substitute(x, handlesOf(s.Handles)); err != nil && !unknown {
			unknown = true
			out = append(out, issueID+": "+err.Error())
		}
		return x
	})
	for _, q := range quotedRe.FindAllString(s.Markdown, -1) { // bench: "administered by {Heron} in {Indigo}" in a verbatim quote
		if i := slices.IndexFunc(s.Handles, func(f Fig) bool { return strings.Contains(q, f.Handle) }); i >= 0 {
			out = append(out, issueID+": "+s.Handles[i].Handle+" is inside a quotation, where Go never substitutes it: quote the document's own words there, or move the handle outside the quotation marks")
			break
		}
	}
	fids := slices.Sorted(maps.Keys(cited)) // in a fixed order: what is listed first is the same every run
	var ground strings.Builder
	for _, fid := range fids {
		f := usable[fid]
		for _, c := range f.Citations {
			ground.WriteString(c.Quote + "\n")
		}
		for _, v := range f.Values {
			ground.WriteString(v.Value + "\n")
		}
	}
	rolled := rolledDates(sheets)
	src := citedSources(fids, usable) // the cited quotes' numbers as the figure reader read them, with where each is
	var quoted []analysis.Reading
	for _, c := range src {
		quoted = append(quoted, c.Reading)
	}
	cg := ground.String()                            // what the section cites
	sheets = tracedSheets(sheets, cg, cited, quoted) // a worksheet traces only from what the section cites, as it states it (lab-r4 r6 M3, r7 H1)
	byHandle := map[string]analysis.Worksheet{}
	for _, w := range sheets {
		byHandle["{"+w.WID+"}"] = w
	}
	// The grounds G8 traces against, each parsed once (lab-r4 r11 M): base is what the section cites
	// (its quotes and values, and the handles Go wrote for them); each traced worksheet adds its result
	// and steps and the handles Go wrote for it (a raw date before its roll only beside the rolled one,
	// below). G8 traces each paragraph with every worksheet, then without any: a figure that traces
	// only with them rests on a worksheet, whatever its operands are (r11 H: a ref's value is the
	// figure reader's reading of it, its scale and kind), and is listed with its inputs.
	var bt strings.Builder
	bt.WriteString(cg)
	for _, f := range s.Handles {
		if cited[f.FID] {
			bt.WriteString(f.Value + "\n")
		}
	}
	base := groundOf(bt.String())
	extra := make([]string, len(sheets))
	for i, w := range sheets {
		var b strings.Builder
		for _, f := range s.Handles {
			if x, sheet := byHandle[f.Handle]; sheet && f.FID == "" && x.WID == w.WID && !rawDate(f.Value, rolled) {
				b.WriteString(f.Value + "\n")
			}
		}
		if _, raw := rolled[w.Result]; !raw || w.Unit != "date" {
			b.WriteString(w.Result + "\n")
		}
		for _, st := range w.Steps {
			if _, raw := rolled[strings.Fields(st.Value + " x")[0]]; st.Step != "raw date" && !raw {
				b.WriteString(st.Value + "\n")
			}
		}
		extra[i] = b.String()
	}
	gAll := base.plus(strings.Join(extra, ""))
	only := map[int]traceGround{} // base and one worksheet's, built when a paragraph needs it
	body, regs := memoBody(s)
	const all, none = -2, -1                                        // which worksheets a trace keeps: all, none, or the one at that index
	regsWith := func(keep func(analysis.Worksheet) bool) []region { // what Go wrote for a handle traces by construction: a cited finding's value, or a kept traced worksheet's result
		out := slices.Clone(regs)
		for i, r := range out {
			w, sheet := byHandle[r.fig.Handle]
			out[i].ok = r.fig.FID == "" && sheet && keep(w) && !rawDate(r.fig.Value, rolled) || cited[r.fig.FID]
		}
		return out
	}
	read := map[int]analysis.Reading{} // the figure reader's readings of the section's own figures, when it read this text
	if s.Read != nil && s.Read.Hash == textHash(body) {
		for _, r := range s.Read.Figs {
			read[r.Start] = r
		}
	}
	var known []analysis.Reading // what the cited findings' quotes and values were read as
	var unread []analysis.Span   // and their quotes' numbers the figure reader never read (not read at all: not unclear, not another kind)
	unreadBy := map[string][]analysis.Span{}
	for _, fid := range fids {
		f := usable[fid]
		known = append(known, f.Figures...)
		for _, r := range f.Figures {
			if r.Kind == analysis.KindUnclear && strings.HasPrefix(r.Why, "not read") {
				unreadBy[fid] = append(unreadBy[fid], analysis.Span{Text: r.Text})
			}
		}
		if len(f.Figures) == 0 { // never read: its quotes' numbers the reader is sent (never a locator's: quotePassage)
			for _, c := range f.Citations {
				unreadBy[fid] = append(unreadBy[fid], quotePassage("q", c.Quote).Spans...)
			}
		}
		unread = append(unread, unreadBy[fid]...)
		for _, v := range f.Values {
			if v.Fig != nil {
				known = append(known, *v.Fig)
			}
		}
	}
	cs := computeSet{src: src, ground: groundOf(cg), dates: map[string]quoteDates{}, sheets: sheets, usable: usable, fids: fids}
	trace := func(para string, td []string, tn []analysis.Span, pd []string, at0, which int) []untraced { // G8 over one paragraph (its locators masked), with the worksheets which keeps
		keep := func(w analysis.Worksheet) bool { return which == all || which >= 0 && w.WID == sheets[which].WID }
		g, ws := base, []analysis.Worksheet(nil)
		switch {
		case which == all:
			g, ws = gAll, sheets
		case which >= 0:
			if _, ok := only[which]; !ok {
				only[which] = base.plus(extra[which])
			}
			g, ws = only[which], sheets[which:which+1]
		}
		raws := ""
		for raw, w := range rolled { // a paragraph that states a rolled deadline may name its raw date
			if slices.Contains(pd, w.Result) && keep(w) {
				raws += raw + "\n"
			}
		}
		return g8In(para, td, tn, g.plus(raws), at0, regsWith(keep), read, known, unread, ws, cited, rolled)
	}
	keys := func(us []untraced) map[string]bool {
		m := map[string]bool{}
		for _, u := range us {
			m[u.key()] = true
		}
		return m
	}
	seen := map[string]bool{}
	var memoUnread, memoUnreadBy, memoWhy []string // the section's figures the reader never read, the findings stating them as written, and why
	hit := map[string][]untraced{}                 // the memo figures each worksheet is listed for (r10 H1: G8 itself decides, whatever form the figure is written in)
	memoAs := map[string][]untraced{}              // a scaled memo figure a worksheet computes, listed with it (r11 L5)
	handled := map[string]bool{}                   // the worksheets the section states by their handles
	off := 0
	for _, para := range strings.Split(body, "\n\n") {
		at0 := off
		off += len(para) + 2
		pd, _ := figures(para, true)
		masked := analysis.MaskLocators(para)
		td, tn := figures(masked, true)
		tr := func(which int) []untraced { return trace(masked, td, tn, pd, at0, which) }
		traced := tr(all)
		if attribute && len(sheets) > 0 {
			with := keys(traced)
			var missing []untraced // traced with the worksheets, not without them
			for _, n := range tr(none) {
				if !with[n.key()] {
					missing = append(missing, n)
				}
			}
			found := map[string]bool{}
			for i := 0; i < len(sheets) && len(found) < len(missing); i++ { // one pass: the first worksheet each traces through alone
				got := keys(tr(i))
				for _, n := range missing {
					if !found[n.key()] && !got[n.key()] {
						found[n.key()] = true
						hit[sheets[i].WID] = append(hit[sheets[i].WID], n)
					}
				}
			}
			for _, n := range missing { // traced only through several together: all of them
				for _, w := range sheets {
					if !found[n.key()] {
						hit[w.WID] = append(hit[w.WID], n)
					}
				}
			}
		}
		for _, n := range traced {
			if seen[n.figure] {
				continue
			}
			seen[n.figure] = true
			switch n.limited {
			case limitedMemoUnread: // one check-first item for the section's unread figures (below), its findings gated
				var by []string // the cited findings whose own verified quotes or values state it as written
				for _, fid := range fids {
					f := usable[fid]
					text, _ := verifiedText(f)
					for _, v := range f.Values {
						text += v.Value + "\n"
					}
					_, gn := figures(text, true)
					if _, ss := analysis.Literal(n.figure); len(ss) > 0 && spanStated(ss[0], gn) {
						by = append(by, fid)
					}
				}
				if len(by) == 0 { // none does (a cited handle or worksheet alone): flagged, as any unread figure (HIGH-1)
					out = append(out, issueID+": the figure "+n.figure+" is not traced (G8): the figure reader did not read it clearly ("+n.unread+"), so it is flagged, never traced; "+hint(n.figure, s.Handles, usable, cited))
					continue
				}
				memoUnread = append(memoUnread, n.figure)
				if why := strings.TrimPrefix(n.unread, "not read: "); !slices.Contains(memoWhy, why) {
					memoWhy = append(memoWhy, why)
				}
				for _, fid := range by {
					if gate[fid] == "" {
						gate[fid] = limitedMemoUnread
					}
					if !slices.Contains(memoUnreadBy, fid) {
						memoUnreadBy = append(memoUnreadBy, fid)
					}
				}
				continue
			case limitedUnit, limitedOther: // a figure resting on a reader's label: the Computed figures block's first tier
				computed = append(computed, cs.labelled(n))
				if n.limited == limitedUnit {
					for _, fid := range cs.holding(n.known) {
						if gate[fid] == "" {
							gate[fid] = limitedUnit
						}
					}
				}
				continue
			case "":
			default:
				i := -1
				if n.limited == limitedScale && attribute { // a worksheet's result written at a scale: listed with the worksheet, once (r11 L5)
					i = slices.IndexFunc(sheets, func(w analysis.Worksheet) bool {
						x := sheetAmount(w)
						return x != nil && x.Cmp(n.read.Rat()) == 0 && sheetKind(w, n.read)
					})
				}
				if i >= 0 {
					memoAs[sheets[i].WID] = append(memoAs[sheets[i].WID], n)
					if hit[sheets[i].WID] == nil {
						hit[sheets[i].WID] = []untraced{}
					}
				} else {
					limited[n.limited] = append(limited[n.limited], n.figure)
				}
				if n.limited == limitedUnread { // the cited findings whose unread quotes state it: gated for a lawyer (r6 M2)
					for fid, ns := range unreadBy {
						if _, ss := analysis.Literal(n.figure); len(ss) > 0 && spanStated(ss[0], ns) && gate[fid] == "" {
							gate[fid] = limitedUnread
						}
					}
				}
				if n.limited == limitedScale { // the cited findings that state it at the other scale: gated too (r7 M4, L2)
					for _, fid := range fids {
						f := usable[fid]
						figs := slices.Clone(f.Figures)
						for _, v := range f.Values {
							if v.Fig != nil {
								figs = append(figs, *v.Fig)
							}
						}
						if slices.ContainsFunc(figs, func(k analysis.Reading) bool { return scaledBeside(n.read, k) }) && gate[fid] == "" {
							gate[fid] = limitedScale
						}
					}
				}
				continue
			}
			if w, raw := rolled[n.figure]; raw {
				out = append(out, issueID+": the date "+n.figure+" is a deadline's raw date, before Rule 6(a)'s roll past a weekend or holiday (G8): the deadline is {"+w.WID+"} ("+
					sheetValue(w.Result, w.Unit)+"): write that handle, and state the raw date only in the sentence that gives the rolled deadline")
				continue
			}
			out = append(out, issueID+": the figure "+n.figure+" is not traced (G8): "+cmpStr(n.unread != "", "the figure reader did not read it clearly ("+n.unread+"), so it is flagged, never traced; ", "")+
				cmpStr(n.hint != "", n.hint, hint(n.figure, s.Handles, usable, cited)))
		}
	}
	if len(memoUnread) > 0 {
		limited[limitedMemoUnread] = memoUnread
		computed = append(computed, computedFig{Key: "memo-unread:" + issueID, Check: true,
			Text: fmt.Sprintf("%s in the memo %s not read by the figure reader (%s): %s only as written, digit for digit, to %s, whose findings are gated; check %s against the cited quotes",
				strings.Join(memoUnread, ", "), plural(len(memoUnread), "was", "were"), strings.Join(memoWhy, "; "), plural(len(memoUnread), "it traces", "they trace"),
				"["+strings.Join(memoUnreadBy, "] [")+"]", plural(len(memoUnread), "it", "each"))})
	}
	if !attribute {
		return out, limited, gate, nil
	}
	for _, r := range regs { // a worksheet's handle: Go wrote its result, so the section states it (r10 L1: listed with the rest)
		if w, sheet := byHandle[r.fig.Handle]; sheet {
			handled[w.WID] = true
			if hit[w.WID] == nil {
				hit[w.WID] = []untraced{}
			}
		}
	}
	for _, w := range sheets {
		if memo, ok := hit[w.WID]; ok {
			c := cs.sheet(w, memo, memoAs[w.WID], handled[w.WID])
			for fid, why := range c.Gate { // a ⚠ input: its findings gated, as a memo figure's would be (r11 H, L1)
				if gate[fid] == "" {
					gate[fid] = why
				}
			}
			computed = append(computed, c)
		}
	}
	return out, limited, gate, computed
}

// untraced is one figure G8 could not trace: as a lint line names it, how to fix it ("" for hint's),
// and why the figure reader did not read it clearly ("" when it did).
type untraced struct {
	figure, hint, unread string
	limited              string           // why it is limited, not flagged: limitedUnread, limitedScale, limitedUnit or limitedOther ("" flagged)
	read                 analysis.Reading // a limited figure's reading (its cited findings are gated by it)
	known                analysis.Reading // the cited reading a limitedUnit or limitedOther figure rests on
	at                   int              // where a number is in the section's body (-1 for a date: its key is figure)
}

// key is which memo figure an untraced entry is, whatever G8 named it.
func (u untraced) key() string {
	if u.at < 0 {
		return "d:" + u.figure
	}
	return "n:" + strconv.Itoa(u.at)
}

// scaledBeside reports whether a memo figure r and a cited reading k are one amount of one kind and
// currency, one of them read at a scale (from its words) and the other written out digit for digit:
// the same figure only by the figure reader's reading of the scale's words, which a lawyer confirms
// (limitedScale, both directions alike: r7 M4).
func scaledBeside(r, k analysis.Reading) bool {
	_, soft := analysis.SameFigure(r, k)
	return soft && r.Clear() && k.Clear() && (r.Scale == "") != (k.Scale == "")
}

// Why a figure G8 does not trace clearly is limited (disclosed in Limitations, for the reviewing
// lawyer at the deliverable's gate) rather than flagged: its cited quote was never read (it traces
// only as written), or the drafter wrote a scale where the cited figure states every digit ("$5M"
// for "$5,000,000"), which only the reader's scale of the drafter's words makes the same figure.
const (
	limitedUnread = "unread"
	limitedScale  = "scale"
	// limitedUnit is a memo period that is a cited one only by the reader's unit: the two readings'
	// unit words differ as written (the memo's "days", the record's "business days" or "of"; lab-r4
	// r10 M1): disclosed, its findings gated.
	limitedUnit = "unit"
	// limitedOther is a memo number the reader read as an identifier, traced as written, whose digits a
	// cited reading of a quantity (money, a count, a percentage, a number, a period) states (r10 L5):
	// traced, but listed for the lawyer to check first.
	limitedOther = "other"
	// limitedMemoUnread is a memo figure the figure reader never read (its call unfunded or failed:
	// bench7), which the cited findings' quotes or values state as written: listed check-first and
	// its findings gated, never traced by a reading (fail closed: a figure none states as written
	// stays a blocking lint line).
	limitedMemoUnread = "memo-unread"
)

// g8In traces one paragraph (its locators masked; it starts at at0 in the section's body; td, tn:
// figures(para, true), and g its ground, each parsed once: lab-r4 r11 M, lintLimited traces each
// paragraph against several grounds): its dates literally against ground's; each number Go wrote
// for a handle whose value is ground (a cited finding's, a worksheet's) by construction; each
// number the drafter wrote by the figure reader's reading of it. A number it read clearly as money,
// a count or a percentage traces only to a cited finding's quote span or value it read clearly as
// the same kind, currency, scale and amount, or (at scale 1) to a worksheet's result: never to
// digits read as something else, read unclearly or not read (lab-r4 r4: "$2,000,000" never traces
// to a share count; r5 M1). A number it read clearly as a date's part or an identifier traces only
// as written, digit for digit, in ground. A number it did not read clearly, or did not read at all
// (the reader unfunded, failed or unsure), is flagged: G8 fails closed, it never traces a figure it
// cannot read (r5 H2: "$9 billion" beside a "$9,000"). A clear figure whose cited quote the reader
// never read (unread: its admission's call unfunded or failed) is limited when that quote states it
// as written, and so is a figure the drafter wrote at a scale beside a cited one read at scale 1
// (or a worksheet's result) with the same amount: kept, and disclosed in Limitations; a cited
// figure read at a scale never traces one written at another (r5 H1: a "$500" read "in thousands"
// is flagged beside a memo's "$500,000").
func g8In(para string, td []string, tn []analysis.Span, g traceGround, at0 int, regs []region, read map[int]analysis.Reading, known []analysis.Reading, unread []analysis.Span, sheets []analysis.Worksheet, cited map[string]bool, rolled map[string]analysis.Worksheet) []untraced {
	gd, gn := g.dates, g.nums
	var out []untraced
	for _, k := range td {
		d, _ := analysis.ParseDate(k)
		if !slices.ContainsFunc(gd, func(x analysis.Date) bool { return x.Compatible(d) }) {
			out = append(out, untraced{figure: k, at: -1})
		}
	}
	for _, n := range tn {
		at := at0 + n.Start
		if i := slices.IndexFunc(regs, func(r region) bool { return r.start <= at && at < r.end }); i >= 0 {
			if regs[i].ok {
				continue
			}
			if !spanStated(n, gn) { // an uncited finding's handle: its value as written in what is cited
				out = append(out, untraced{figure: decimalOf(n), at: at})
			}
			continue
		}
		r, ok := read[at]
		switch {
		case ok && neverRead(r.Why) && spanStated(n, g.cited): // the reader's call never ran or failed (unfunded, failed; bench7: every figure of an unfunded memo, years too, blocked finish), and a cited finding states it as written (never a worksheet alone: its gate is the worksheet's)
			out = append(out, untraced{figure: n.Text, limited: limitedMemoUnread, unread: r.Why, at: at}) // as written only: listed check-first, its findings gated
		case !ok || r.Unclear || r.Kind == analysis.KindUnclear:
			why := "it was not read"
			if ok && r.Why != "" {
				why = r.Why
			}
			out = append(out, untraced{figure: decimalOf(n), unread: why, at: at})
		case r.Clear():
			if slices.ContainsFunc(known, func(k analysis.Reading) bool { same, _ := analysis.SameFigure(r, k); return same }) ||
				r.Scale == "" && slices.ContainsFunc(sheets, func(w analysis.Worksheet) bool {
					x := sheetAmount(w)
					return x != nil && x.Cmp(r.Rat()) == 0 && sheetKind(w, r)
				}) {
				continue
			}
			if spanStated(n, unread) && !slices.ContainsFunc(known, func(k analysis.Reading) bool { return k.Text == n.Text && !k.Unclear }) {
				out = append(out, untraced{figure: n.Text, limited: limitedUnread, at: at}) // only as written: limited, disclosed
				continue
			}
			if slices.ContainsFunc(known, func(k analysis.Reading) bool { return scaledBeside(r, k) }) || // the drafter's "$5M" for a cited "$5,000,000", or its "$5,000,000" for a cited "$5M" (r7 M4)
				r.Scale != "" && slices.ContainsFunc(sheets, func(w analysis.Worksheet) bool {
					x := sheetAmount(w)
					return x != nil && x.Cmp(r.Rat()) == 0 && sheetKind(w, r)
				}) {
				out = append(out, untraced{figure: written(para, analysis.Reading{Start: n.Start, End: n.End, ScaleFrom: r.ScaleFrom}), limited: limitedScale, read: r, at: at})
				continue
			}
			h := ""
			if k := slices.IndexFunc(known, func(k analysis.Reading) bool { return k.Amount == r.Amount && !k.Unclear }); k >= 0 {
				h = "the cited findings state " + r.Amount + " only as " + article(known[k].Kind) + cmpStr(known[k].Currency != "", " in "+known[k].Currency, "") +
					cmpStr(known[k].Scale != r.Scale, " at another scale", "") + " (the figure reader's reading), not as the " + describe(r) + " this sentence states: state what they state, or remove it"
			}
			out = append(out, untraced{figure: r.Amount, hint: h, read: r, at: at})
		case r.Kind == analysis.KindDuration: // a period: only a cited period in its unit, or the days between two cited dates (lab-r4 r9 H2: never a section number's digits)
			switch ok, k := durationStated(r, known, g.spans, sheets); {
			case !ok:
				out = append(out, untraced{figure: decimalOf(n) + " " + r.Unit, hint: "the cited findings state no period of " + decimalOf(n) + " " + r.Unit +
					" (a duration in that unit, or the days between two dates they state): state the period they state, compute it with numbers op calc fn date_diff, or remove it", read: r, at: at})
			case k != nil: // the cited period's words differ from the memo's: only the reader's unit label makes them one
				out = append(out, untraced{figure: decimalOf(n) + " " + r.Unit, limited: limitedUnit, read: r, known: *k, at: at})
			}
		case r.Kind == analysis.KindDate && (yearIn(n, gd) || dayIn(para, n, gd)):
		case !slices.ContainsFunc(gn, func(g analysis.Span) bool { return unsigned(g.Text) == unsigned(n.Text) }): // an identifier or a date's part: as written
			out = append(out, untraced{figure: n.Text, at: at})
		case ok && r.Kind == analysis.KindOther: // as written; listed when a cited quantity states the same digits (r10 L5)
			if i := slices.IndexFunc(known, func(k analysis.Reading) bool {
				return !k.Unclear && k.Kind != analysis.KindOther && k.Kind != analysis.KindDate && k.Kind != analysis.KindUnclear && k.Rat() != nil && r.Rat() != nil && new(big.Rat).Abs(k.Rat()).Cmp(new(big.Rat).Abs(r.Rat())) == 0
			}); i >= 0 {
				out = append(out, untraced{figure: n.Text, limited: limitedOther, read: r, known: known[i], at: at})
			}
		}
	}
	return out
}

// durationStated reports whether a memo's period r is the cited record's: a cited duration of that
// amount in that unit with the words beside it that state it, a traced worksheet's period of it
// (date_diff, business_days), or, in days, the days between two dates ground states. A cited duration
// whose words differ from the memo's as written (case, punctuation and a plural aside) is returned as
// k: the memo's period is the record's only by the reader's unit label (lab-r4 r10 M1).
func durationStated(r analysis.Reading, known []analysis.Reading, spans []int64, sheets []analysis.Worksheet) (ok bool, k *analysis.Reading) {
	x := r.Rat()
	if x == nil {
		return false, nil
	}
	x.Abs(x)
	same := func(y *big.Rat) bool { return y != nil && new(big.Rat).Abs(y).Cmp(x) == 0 }
	var other *analysis.Reading
	for i, c := range known {
		if c.Unclear || c.Kind != analysis.KindDuration || c.Unit != r.Unit || c.UnitFrom == "" || !same(c.Rat()) {
			continue
		}
		if unitWords(c.UnitFrom) == unitWords(r.UnitFrom) {
			return true, nil
		}
		other = &known[i]
	}
	if slices.ContainsFunc(sheets, func(w analysis.Worksheet) bool {
		y, ok := new(big.Rat).SetString(w.Result)
		return ok && w.Unit == r.Unit && same(y)
	}) || r.Unit == "days" && x.IsInt() && slices.Contains(spans, x.Num().Int64()) {
		return true, nil
	}
	return other != nil, other
}

// unitWords are a unit's words as two texts may write them alike: lower case, letters only, each
// word's plural "s" dropped ("Days", "day", "days" alike; "business days" another).
func unitWords(s string) string {
	ws := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	for i, w := range ws {
		if len(w) > 1 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			ws[i] = w[:len(w)-1]
		}
	}
	return strings.Join(ws, " ")
}

// unsigned is a span's text without a minus written before it ("-7 days" states the memo's "7 days").
func unsigned(s string) string { return strings.TrimLeft(s, "-−–—") }
