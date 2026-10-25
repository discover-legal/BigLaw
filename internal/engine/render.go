// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// draftReq is a draft_section request as Go completed it, with the handle set the drafter got.
type draftReq struct {
	issueID string
	request map[string]any
	figs    []Fig
}

// draftRequest completes a draft_section call (paged drafting): the issue, its named findings
// (verified and not rejected only: G3), their figure handles and every worksheet's, the summaries
// of the sections already written, in plan order, and on a redraft the checks the current version
// fails (lint), so the drafter fixes exactly those.
func (e *Engine) draftRequest(ctx context.Context, taskID string, scope []string, issueID string, input json.RawMessage) (draftReq, error) {
	var in struct {
		FIDs   []string `json:"fids"`
		Revise *string
	}
	_ = json.Unmarshal(input, &in)
	issues, err := load[Issue](ctx, e, taskID, kIssue)
	secs, serr := load[Section](ctx, e, taskID, kSection)
	use, uerr := e.usable(ctx, taskID)
	if err = cmpErr(err, serr, uerr); err != nil {
		return draftReq{}, err
	}
	i := slices.IndexFunc(issues, func(is Issue) bool { return is.ID == issueID })
	if i < 0 {
		return draftReq{}, agent.Fail("draft_section: " + issueID + " is not a plan issue; draft one of the plan's issues (ledger op findings lists them)")
	}
	var fs []map[string]any
	var omitted []string
	names := map[string]string{} // documentId → its name, so the drafter can say which document states what
	name := func(id string) string {
		if _, ok := names[id]; !ok {
			if d, err := e.docs.Get(ctx, scope, id); err == nil {
				names[id] = d.Name
			} else {
				names[id] = ""
			}
		}
		return names[id]
	}
	for _, id := range in.FIDs {
		j := slices.IndexFunc(use, func(f Finding) bool { return f.FID == id })
		if j < 0 {
			omitted = append(omitted, id)
			continue
		}
		var qs []map[string]string
		for _, c := range use[j].Citations {
			qs = append(qs, map[string]string{"documentId": c.DocumentID, "document": name(c.DocumentID), "quote": c.Quote, "locator": c.Locator})
		}
		fs = append(fs, map[string]any{"fid": id, "claim": use[j].Claim, "quotes": qs})
	}
	if len(fs) == 0 {
		return draftReq{}, agent.Fail("draft_section: none of fids is a verified finding of this task; name verified findings (ledger op findings)")
	}
	d := draftReq{issueID: issueID}
	all, err := e.handleSet(ctx, taskID, use)
	if err != nil {
		return draftReq{}, err
	}
	hs := []map[string]string{}
	for _, h := range all { // the drafter sees its findings' handles and every worksheet's; Go substitutes the run's set
		d.figs = append(d.figs, Fig{h.Handle, h.FID, h.Value})
		if h.FID == "" || slices.Contains(in.FIDs, h.FID) {
			hs = append(hs, map[string]string{"handle": h.Handle, "fid": cmpStr(h.FID != "", h.FID, "worksheet "+h.Ref), "label": h.Label})
		}
	}
	var prior []map[string]string
	for _, is := range issues {
		if k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID }); k >= 0 && is.ID != issueID {
			prior = append(prior, map[string]string{"issueId": is.ID, "summary": secs[k].Summary})
		}
	}
	d.request = map[string]any{"issue": map[string]string{"id": issueID, "question": issues[i].Question}, "findings": fs, "handles": hs,
		"priorSections": prior, "revise": in.Revise}
	if as, err := e.analyses(ctx, taskID); err != nil {
		return draftReq{}, err
	} else if a, ok := as[issueID]; ok {
		d.request["analysis"] = a // the issue's legal analysis (analyze_issue): rule, application, conclusion
	}
	if len(omitted) > 0 {
		d.request["omitted"] = "not verified findings of this task, so not shown: " + strings.Join(omitted, ", ")
	}
	if k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == issueID }); k >= 0 {
		ws, err := e.worksheets(ctx, taskID)
		if err != nil {
			return draftReq{}, err
		}
		byFID := map[string]Finding{}
		for _, f := range use {
			byFID[f.FID] = f
		}
		if checks := lint(issueID, secs[k], byFID, ws); len(checks) > 0 {
			d.request["checks"] = checks
		}
	}
	return d, nil
}

// saveSection records a drafted section with the handle set its drafter was given; the draft's
// version moves on.
func (e *Engine) saveSection(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, d draftReq, answer json.RawMessage) (any, error) {
	taskID := c.TaskID
	var a struct{ Markdown, Summary string }
	_ = json.Unmarshal(answer, &a)
	secs, err := load[Section](ctx, e, taskID, kSection)
	if err != nil {
		return nil, err
	}
	s := Section{IssueID: d.issueID, Markdown: splitCites(a.Markdown), Summary: splitCites(a.Summary), Handles: d.figs, Version: version(secs) + 1}
	before := b.Spent().Calls
	if err := e.readSection(ctx, rt, c, b, &s); err != nil {
		return nil, err
	}
	if n := b.Spent().Calls - before; n > 0 && ctx.Value(coverDraft{}) == nil { // the reader's calls are not the lead's drafter's: P4's draft share leaves them out (policy.go); a coverage redraft's are its finish step's
		prior, err := load[int](ctx, e, taskID, kRead)
		if err != nil {
			return nil, err
		}
		for _, p := range prior {
			n += p
		}
		if err := e.write(ctx, taskID, kRead, "draft", n, true); err != nil {
			return nil, err
		}
	}
	if err := e.write(ctx, taskID, kSection, d.issueID, s, true); err != nil {
		return nil, err
	}
	return map[string]any{"issueId": d.issueID, "version": s.Version, "words": len(strings.Fields(a.Markdown))}, nil
}

// readSection has the figure reader read a drafted section's own figures (the ones the drafter
// wrote, not Go's handle values: G8 and the coverage pass read them) and, in the same calls, the
// findings it cites that were never read (one with no typed value is read when first cited), out of
// b; it sets the section's Read.
func (e *Engine) readSection(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, s *Section) error {
	body, regs := memoBody(*s)
	var cited []string
	fs, err := load[Finding](ctx, e, c.TaskID, kFinding)
	if err != nil {
		return err
	}
	for _, m := range fidRe.FindAllStringSubmatch(s.Markdown, -1) {
		if i := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == m[1] }); i >= 0 && fs[i].Unread && fs[i].Verified && !slices.Contains(cited, m[1]) {
			cited = append(cited, m[1])
		}
	}
	spans := memoSpans(body, regs)
	var it *reading
	var with []*reading
	if len(spans) > 0 {
		it = newReading(nil, []analysis.Passage{{ID: "m", Text: body, Spans: spans}}, []passageIn{{ID: "m", Kind: "memo", Text: body}})
		it.memo = true
		with = append(with, it)
	}
	if len(with)+len(cited) > 0 {
		if _, err := e.readFindings(ctx, rt, c, b, "draft_section", cited, with...); err != nil {
			return err
		}
	}
	if it != nil {
		rs, _ := it.readings()
		s.Read = &SectionRead{Hash: textHash(body), Figs: ofPassage(rs, "m")}
		if whys := slices.Sorted(maps.Values(it.unread)); len(whys) > 0 {
			s.Read.Unread = fmt.Sprintf("%d of its %d figures were not read (%s)", len(it.unread), len(spans), strings.Join(slices.Compact(whys), "; "))
		}
	}
	return nil
}

// stale reports whether a section's reading is missing, of other text, or left figures unread for
// want of budget, or it cites a finding never read: finish reads it again first (bench7).
func (e *Engine) stale(s Section, fs []Finding) bool {
	body, regs := memoBody(s)
	if len(memoSpans(body, regs)) > 0 && (s.Read == nil || s.Read.Hash != textHash(body) || strings.Contains(s.Read.Unread, "the budget could not fund")) {
		return true
	}
	for _, m := range fidRe.FindAllStringSubmatch(s.Markdown, -1) {
		if i := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == m[1] }); i >= 0 && fs[i].Unread && fs[i].Verified {
			return true
		}
	}
	return false
}

// readSections reads again every section whose reading is stale (stale), out of b, in plan order.
func (e *Engine) readSections(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget) error {
	secs, err := load[Section](ctx, e, c.TaskID, kSection)
	if err != nil {
		return err
	}
	for _, s := range secs {
		fs, err := load[Finding](ctx, e, c.TaskID, kFinding)
		if err != nil {
			return err
		}
		if !e.stale(s, fs) {
			continue
		}
		if err := e.readSection(ctx, rt, c, b, &s); err != nil {
			return err
		}
		if err := e.write(ctx, c.TaskID, kSection, s.IssueID, s, true); err != nil {
			return err
		}
	}
	return nil
}

// citeGroup is a drafter's bracket of several finding ids or value refs ("[F304, F305]",
// "[F288.v2, F288.v1]"): the memo's citation is one [F-id] per bracket (lint, Sources, coverage).
var citeGroup, citeID = regexp.MustCompile(`\[\s*F\d+(?:\.v\d+)?(?:\s*[,;]\s*F\d+(?:\.v\d+)?)*\s*\]`), regexp.MustCompile(`F\d+`)

// splitCites rewrites each citation group as single citations, one per finding, in order: "[F3,
// F3.v1, F7]" is "[F3] [F7]". Read as a group, its ids were no citation at all and its digits were
// figures G8 flagged (bench: most of a run's lint lines were "the figure 304", from "[F304, F305]").
func splitCites(md string) string {
	return citeGroup.ReplaceAllStringFunc(md, func(g string) string {
		var out []string
		for _, id := range citeID.FindAllString(g, -1) {
			if c := "[" + id + "]"; !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		return strings.Join(out, " ")
	})
}

// deliverable is a run's rendered memo and what it disclosed.
type deliverable struct {
	Markdown    string
	Limitations []string
}

// render assembles the memo losslessly from its sections in plan order (summaries never), with
// handles substituted outside quotes, then Sources (transcript citations badged, G11),
// Limitations, the Evidence Note and the note to the reviewing lawyer; external authorities are
// flagged (G5). flagged lists lint problems a forced finish delivers with (marked inline).
func (e *Engine) render(ctx context.Context, t Task, limits []string, computed computedBlock, meta string, flagged []string) (deliverable, error) {
	issues, err := load[Issue](ctx, e, t.ID, kIssue)
	secs, serr := load[Section](ctx, e, t.ID, kSection)
	all, ferr := load[Finding](ctx, e, t.ID, kFinding)
	use, uerr := e.usable(ctx, t.ID)
	if err = cmpErr(err, serr, ferr, uerr); err != nil {
		return deliverable{}, err
	}
	var b strings.Builder
	title, _, _ := strings.Cut(strings.TrimSpace(t.Description), "\n")
	fmt.Fprintf(&b, "# %s\n", mdSafe(truncate(title, 160), true))
	cited, cut := []string{}, 0
	for _, is := range issues {
		k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID })
		if k < 0 {
			continue
		}
		body, n := dropUnusable(polishSection(is.Question, substituteOutsideQuotes(secs[k].Markdown, secs[k].Handles)), use)
		body = mdSafe(body, false)
		cut += n
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", mdSafe(is.Question, true), body)
		for _, m := range fidRe.FindAllStringSubmatch(body, -1) {
			if !slices.Contains(cited, m[1]) {
				cited = append(cited, m[1])
			}
		}
	}
	names := map[string]string{}
	var corpus strings.Builder
	for _, id := range t.DocumentIDs {
		if d, err := e.docs.Get(ctx, t.DocumentIDs, id); err == nil {
			names[id] = d.Name
			corpus.WriteString(d.Name + "\n" + d.Text + "\n")
		}
	}
	body, external := flagExternalAuthorities(dedupeDocBlocks(b.String()), corpus.String())
	for _, f := range flagged {
		limits = append(limits, "Delivered with an unresolved check: "+f)
	}
	if cut > 0 {
		limits = append(limits, fmt.Sprintf("%d %s citing a finding that is not usable (rejected at its gate or quarantined) %s removed from the memo.", cut,
			plural(cut, "sentence", "sentences"), plural(cut, "was", "were")))
	}
	b.Reset()
	b.WriteString(body)
	b.WriteString("\n\n## Sources\n\n")
	transcriptOnly, lowOnly := 0, 0
	for _, fid := range cited {
		i := slices.IndexFunc(use, func(f Finding) bool { return f.FID == fid })
		if i < 0 {
			continue
		}
		f := use[i]
		var cites []string
		for _, c := range f.Citations {
			where := strings.TrimSuffix(cmpStr(names[c.DocumentID] != "", names[c.DocumentID], c.DocumentID)+", "+c.Locator, ", ")
			badge := fmt.Sprintf("%s, %.2f", c.Method, c.Confidence)
			if strings.HasSuffix(c.Method, "@transcript") {
				badge += "; machine transcription (model) of an image: check against the original, /documents/" + c.DocumentID + "/file"
			}
			cites = append(cites, fmt.Sprintf("“%s” (%s; %s)", mdSafe(c.Quote, true), mdSafe(where, true), badge))
		}
		fmt.Fprintf(&b, "- [%s] %s Source: %s\n", fid, mdSafe(f.Claim, true), strings.Join(cites, "; "))
	}
	for _, f := range use {
		transcriptOnly += btoi(allCitations(f, func(c Citation) bool { return strings.HasSuffix(c.Method, "@transcript") }))
		lowOnly += btoi(allCitations(f, func(c Citation) bool { return c.Low }))
	}
	if s := candidates(SelectPacks(t.Jurisdiction, practiceOf(issues))); s != "" {
		limits = append(limits, s)
	}
	if len(external) > 0 {
		limits = append(limits, fmt.Sprintf("%d external %s cited above %s not verified (no citator): see Authorities Requiring Verification.", len(external),
			plural(len(external), "authority", "authorities"), plural(len(external), "is", "are")))
	}
	limits = append(limits, "External authorities (cases, statutes) are flagged, not verified: BigLaw has no live citator.")
	b.WriteString("\n## Limitations\n\n")
	for _, l := range limits {
		b.WriteString("- " + mdSafe(l, true) + "\n")
	}
	b.WriteString(computed.markdown()) // the figures computed over typed inputs, with their sources (lab-r4 r10)
	verified := 0
	for _, f := range all {
		verified += btoi(f.Verified)
	}
	q := len(all) - verified
	fmt.Fprintf(&b, "\n## Evidence Note\n\n%d %s verified against the matter's documents and %d %s quarantined because their quotes did not verify; quarantined findings were kept out of this memo.",
		verified, plural(verified, "finding was", "findings were"), q, plural(q, "was", "were"))
	if r := verified - len(use); r > 0 {
		fmt.Fprintf(&b, " %d verified %s rejected by the lawyer at a gate %s kept out of it too.", r, plural(r, "finding", "findings"), plural(r, "is", "are"))
	}
	if transcriptOnly > 0 || lowOnly > 0 {
		fmt.Fprintf(&b, " %d %s only on machine transcriptions of images, %d of them on low-confidence pages (each gated for a lawyer).", transcriptOnly,
			plural(transcriptOnly, "finding rests", "findings rest"), lowOnly)
	}
	if t.GroundingAlert != "" {
		b.WriteString(" Grounding alert: " + mdSafe(t.GroundingAlert, true) + ".")
	}
	if meta = strings.TrimSpace(meta); meta != "" {
		b.WriteString("\n\n## Note to the reviewing lawyer\n\n" + mdSafe(meta, false))
	}
	return deliverable{Markdown: b.String() + "\n", Limitations: limits}, nil
}

// dropUnusable removes the sentences that cite a finding that is not usable (rejected at its
// gate, or quarantined): lint refuses such a draft, so only a forced finish renders one.
func dropUnusable(md string, use []Finding) (string, int) {
	bad := func(s string) bool {
		return slices.ContainsFunc(fidRe.FindAllStringSubmatch(s, -1), func(m []string) bool {
			return !slices.ContainsFunc(use, func(f Finding) bool { return f.FID == m[1] })
		})
	}
	lines, n := strings.Split(md, "\n"), 0
	for i, ln := range lines {
		if !bad(ln) {
			continue
		}
		parts := splitSentences(ln)
		for j := 1; j < len(parts); j++ { // "… claim. [F2]": a piece of only citations ends the sentence before it
			if strings.Trim(fidRe.ReplaceAllString(parts[j], ""), " .,;:") == "" {
				parts[j-1] += " " + parts[j]
				parts = slices.Delete(parts, j, j+1)
				j--
			}
		}
		kept := slices.DeleteFunc(slices.Clone(parts), bad)
		n += len(parts) - len(kept)
		lines[i] = strings.Join(kept, " ")
	}
	return strings.Join(lines, "\n"), n
}

var (
	mdHeading = regexp.MustCompile(`^( {0,3})#{1,2}(\s|$)`)
	mdFence   = regexp.MustCompile("^( {0,3})(```|~~~)")
)

// mdSafe neutralises text that comes from the matter's documents or from a model (quotes,
// claims, document names, questions, drafted sections, notes) where the deliverable's Markdown
// prints it, so that text cannot open a section, a code fence or raw HTML: "<" is always
// backslash-escaped (Markdown shows it as "<"). inline is a one-line slot (a heading, a list
// item, a Sources line): white space collapses to single spaces, so nothing starts a line. A block
// keeps its lines (lists, tables, "###" subheadings), with "#"/"##" headings demoted to "###",
// below the memo's own sections, and fence openers escaped.
func mdSafe(s string, inline bool) string {
	s = strings.ReplaceAll(s, "<", `\<`)
	if inline {
		return norm(s)
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = mdFence.ReplaceAllString(mdHeading.ReplaceAllString(ln, "${1}###$2"), `${1}\$2`)
	}
	return strings.Join(lines, "\n")
}

// plural is one when n is 1, else many.
func plural(n int, one, many string) string { return cmpStr(n == 1, one, many) }

func allCitations(f Finding, ok func(Citation) bool) bool {
	return len(f.Citations) > 0 && !slices.ContainsFunc(f.Citations, func(c Citation) bool { return !ok(c) })
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
