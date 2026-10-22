// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
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
// (verified and not rejected only: G3), their figure handles, and the summaries of the sections
// already written, in plan order.
func (e *Engine) draftRequest(ctx context.Context, taskID, issueID string, input json.RawMessage) (draftReq, error) {
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
	var an []analysis.Finding
	for _, f := range use {
		an = append(an, f.Finding)
	}
	for _, id := range in.FIDs {
		j := slices.IndexFunc(use, func(f Finding) bool { return f.FID == id })
		if j < 0 {
			omitted = append(omitted, id)
			continue
		}
		var qs []map[string]string
		for _, c := range use[j].Citations {
			qs = append(qs, map[string]string{"documentId": c.DocumentID, "quote": c.Quote, "locator": c.Locator})
		}
		fs = append(fs, map[string]any{"fid": id, "claim": use[j].Claim, "quotes": qs})
	}
	if len(fs) == 0 {
		return draftReq{}, agent.Fail("draft_section: none of fids is a verified finding of this task; name verified findings (ledger op findings)")
	}
	d := draftReq{issueID: issueID}
	var hs []map[string]string
	for _, h := range analysis.Handles(an, in.FIDs, 0) {
		d.figs = append(d.figs, Fig{h.Handle, h.FID, h.Value})
		hs = append(hs, map[string]string{"handle": h.Handle, "fid": h.FID, "label": h.Label})
	}
	var prior []map[string]string
	for _, is := range issues {
		if k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID }); k >= 0 && is.ID != issueID {
			prior = append(prior, map[string]string{"issueId": is.ID, "summary": secs[k].Summary})
		}
	}
	d.request = map[string]any{"issue": map[string]string{"id": issueID, "question": issues[i].Question}, "findings": fs, "handles": hs,
		"priorSections": prior, "revise": in.Revise}
	if len(omitted) > 0 {
		d.request["omitted"] = "not verified findings of this task, so not shown: " + strings.Join(omitted, ", ")
	}
	return d, nil
}

// saveSection records a drafted section with the handle set its drafter was given; the draft's
// version moves on.
func (e *Engine) saveSection(ctx context.Context, taskID string, d draftReq, answer json.RawMessage) (any, error) {
	var a struct{ Markdown, Summary string }
	_ = json.Unmarshal(answer, &a)
	secs, err := load[Section](ctx, e, taskID, kSection)
	if err != nil {
		return nil, err
	}
	s := Section{IssueID: d.issueID, Markdown: a.Markdown, Summary: a.Summary, Handles: d.figs, Version: version(secs) + 1}
	if err := e.write(ctx, taskID, kSection, d.issueID, s, true); err != nil {
		return nil, err
	}
	return map[string]any{"issueId": d.issueID, "version": s.Version, "words": len(strings.Fields(a.Markdown))}, nil
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
func (e *Engine) render(ctx context.Context, t Task, limits []string, meta string, flagged []string) (deliverable, error) {
	issues, err := load[Issue](ctx, e, t.ID, kIssue)
	secs, serr := load[Section](ctx, e, t.ID, kSection)
	all, ferr := load[Finding](ctx, e, t.ID, kFinding)
	use, uerr := e.usable(ctx, t.ID)
	if err = cmpErr(err, serr, ferr, uerr); err != nil {
		return deliverable{}, err
	}
	var b strings.Builder
	title, _, _ := strings.Cut(strings.TrimSpace(t.Description), "\n")
	fmt.Fprintf(&b, "# %s\n", truncate(title, 160))
	cited := []string{}
	for _, is := range issues {
		k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID })
		if k < 0 {
			continue
		}
		body := polishSection(is.Question, substituteOutsideQuotes(secs[k].Markdown, secs[k].Handles))
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", is.Question, body)
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
			cites = append(cites, fmt.Sprintf("“%s” (%s; %s)", c.Quote, where, badge))
		}
		fmt.Fprintf(&b, "- [%s] %s Source: %s\n", fid, f.Claim, strings.Join(cites, "; "))
	}
	for _, f := range use {
		transcriptOnly += btoi(allCitations(f, func(c Citation) bool { return strings.HasSuffix(c.Method, "@transcript") }))
		lowOnly += btoi(allCitations(f, func(c Citation) bool { return c.Low }))
	}
	if s := candidates(SelectPacks(t.Jurisdiction, practiceOf(issues))); s != "" {
		limits = append(limits, s)
	}
	if len(external) > 0 {
		limits = append(limits, fmt.Sprintf("%d external authorit%s cited above %s not verified (no citator): see Authorities Requiring Verification.", len(external), map[bool]string{true: "y", false: "ies"}[len(external) == 1], map[bool]string{true: "is", false: "are"}[len(external) == 1]))
	}
	limits = append(limits, "External authorities (cases, statutes) are flagged, not verified: BigLaw has no live citator.")
	b.WriteString("\n## Limitations\n\n- " + strings.Join(limits, "\n- ") + "\n")
	fmt.Fprintf(&b, "\n## Evidence Note\n\n%d findings were verified against the matter's documents and %d were quarantined because their quotes did not verify; quarantined findings were kept out of this memo.", len(use), len(all)-len(use))
	if transcriptOnly > 0 || lowOnly > 0 {
		fmt.Fprintf(&b, " %d findings rest only on machine transcriptions of images, %d of them on low-confidence pages (each gated for a lawyer).", transcriptOnly, lowOnly)
	}
	if t.GroundingAlert != "" {
		b.WriteString(" Grounding alert: " + t.GroundingAlert + ".")
	}
	if meta = strings.TrimSpace(meta); meta != "" {
		b.WriteString("\n\n## Note to the reviewing lawyer\n\n" + meta)
	}
	return deliverable{Markdown: b.String() + "\n", Limitations: limits}, nil
}

func allCitations(f Finding, ok func(Citation) bool) bool {
	return len(f.Citations) > 0 && !slices.ContainsFunc(f.Citations, func(c Citation) bool { return !ok(c) })
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
