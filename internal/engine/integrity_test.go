// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

var judgeNo = text(m{"supported": false, "confidence": 0.9})

// TestUnverifiedFindingNotInLedger (G2): a finding whose quote is not in the document is stored
// quarantined (its citation method none), never as a verified finding.
func TestUnverifiedFindingNotInLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1"))},
		"extract_facts": {facts(fd("Greystone waived all claims.", d1, "Greystone hereby waives every claim against Talbot",
			val("Greystone", "waiver", "every claim", "")))},
		"verify_quote": {judgeNo, judgeNo},
	}), Config{})
	if len(v.Findings) != 1 || v.Findings[0].Status != "quarantined" || v.Findings[0].Citations[0].Method != "none" {
		t.Fatalf("findings %+v", v.Findings)
	}
	use, _ := f.e.usable(context.Background(), v.ID)
	if len(use) != 0 {
		t.Fatalf("an unverified finding is usable: %+v", use)
	}
}

// TestQuarantineExcludedFromDraft (G3): draft_section never sees a quarantined finding, even when
// the lead names it.
func TestQuarantineExcludedFromDraft(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1", "F2"))},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices"),
			fd("Talbot admitted liability in writing.", d1, "Talbot admits it owes every invoice"))},
		"verify_quote": {judgeNo},
	}), Config{})
	f.submit(0)
	f.step(context.Background())
	var req string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "draft_section" {
			req = c.Messages[1].Content.(string)
		}
	}
	if req == "" || !strings.Contains(req, "644,910") || strings.Contains(req, "admitted liability") || !strings.Contains(req, "not shown: F2") {
		t.Fatalf("draft request: %s", req)
	}
}

// TestFabricatingModelFailsTask (G4): a run whose findings mostly fail verification fails (strict),
// with the grounding alert, rather than deliver.
func TestFabricatingModelFailsTask(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	var fake []m
	for _, q := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"} {
		fake = append(fake, fd("Invented fact "+q+".", d1, "the parties agreed that "+q+" consents were obtained in full"))
	}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fake...)},
		"verify_quote":  {judgeNo, judgeNo},
	}), Config{})
	if stop(v) != "failed/failed" || v.GroundingAlert == nil || !strings.Contains(*v.GroundingAlert, "8 of 8") || v.Output != nil {
		t.Fatalf("%s alert %v error %v", stop(v), v.GroundingAlert, v.Error)
	}
	if strings.Contains(f.steps(v.ID), "draft_section") {
		t.Fatal("the run went on after the collapse")
	}
}

// Carried vectors (v1 orchestrator/grounding_collapse_test.go), changed for v2: strict fails the
// task; warn records the alert and goes on, with unverified findings still quarantined (G3 holds
// in both); at least 8 findings are needed; counting is over the whole task.
func TestGroundingCollapseFail(t *testing.T) {
	alert, fail := Collapse(9, 87, 0.5, "strict")
	if !fail || !strings.Contains(alert, "grounding collapse") || !strings.Contains(alert, "90%") {
		t.Fatalf("%q %v", alert, fail)
	}
}

func TestGroundingCollapseWarnKeepsGoing(t *testing.T) {
	if alert, fail := Collapse(5, 50, 0.5, "warn"); fail || !strings.Contains(alert, "warn mode") || !strings.Contains(alert, "quarantined") {
		t.Fatalf("%q %v", alert, fail)
	}
}

func TestGroundingCollapseNotTriggered(t *testing.T) {
	for _, c := range [][2]int{{96, 100}, {1, 7}, {5, 9}} {
		if alert, _ := Collapse(c[0], c[1], 0.5, "strict"); alert != "" {
			t.Errorf("%d of %d verified: %q", c[0], c[1], alert)
		}
	}
	if alert, _ := Collapse(4, 8, 0.5, "strict"); alert == "" {
		t.Error("4 of 8 unverified at 0.5 is a collapse")
	}
}

// TestModelFailureIsNotAFinding (G6): a subagent's model outage fails the run with the typed error;
// nothing is persisted from the failed call.
func TestModelFailureIsNotAFinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1"))},
		"extract_facts": {{Err: &model.Error{Kind: model.ErrUnavailable, Status: 502, Detail: "upstream said: secret-host.internal"}}},
	}), Config{})
	if stop(v) != "failed/failed" || v.Error == nil || !strings.HasPrefix(*v.Error, "model_unavailable") || strings.Contains(*v.Error, "secret-host") || len(v.Findings) != 0 {
		t.Fatalf("%s %v findings %d", stop(v), v.Error, len(v.Findings))
	}
}

// TestEmptyModelReplyIsError (G6): an empty answer is no_content, never "no findings".
func TestEmptyModelReplyIsError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1"))},
		"extract_facts": {{Text: "  "}},
	}), Config{})
	if stop(v) != "failed/failed" || v.Error == nil || !strings.HasPrefix(*v.Error, "no_content") {
		t.Fatalf("%s %v", stop(v), v.Error)
	}
}

// TestDeliverableGateBlocksCompletion (G7): a finished run waits at the deliverable gate; only a
// lawyer's approval completes it.
func TestDeliverableGateBlocksCompletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.happy(), Config{})
	if v.Status == "complete" || v.Status != "awaiting_gate" || v.PendingGates[0].Kind != "deliverable" {
		t.Fatalf("%s %+v", stop(v), v.PendingGates)
	}
	f.e.Start(cancelled()) // a runner pass changes nothing: the run waits for a lawyer
	if v = f.get(v.ID); v.Status != "awaiting_gate" {
		t.Fatalf("%s", v.Status)
	}
	if v, err := f.e.Decide(context.Background(), v.ID, v.PendingGates[0].GateID, true, ""); err != nil || v.Status != "complete" {
		t.Fatalf("%v %s", err, v.Status)
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestDeliverableLintRejectsUnknownFid (G5): an [F-id] that names no verified finding fails the lint.
func TestDeliverableLintRejectsUnknownFid(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.with(map[string][]model.Reply{
		"draft_section": {section("Greystone claims {Zephyr} [F1]. Talbot admitted it [F9].", "s")},
		"lead":          append(f.happy()["lead"], calls(finishCall("submit"))),
	}), Config{})
	if fin := f.outputs(v.ID, "finish"); len(fin) == 0 || !strings.Contains(fin[0], "[F9] is not a verified finding") || strings.Contains(strings.Join(fin, ""), `"accepted":true`) {
		t.Fatalf("finish %q", fin)
	}
}

// TestModelDigitsNotFromLedgerFlagged (G8, trap): "68.6%" typed where the verified value is 81.6%
// fails the lint; the handle that substitutes 81.6% passes.
func TestModelDigitsNotFromLedgerFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Allocations\nChao's account received 81.6% of the profitable allocations.\n")
	d1 := f.docs[0].ID
	ex := facts(fd("Chao received 81.6% of profitable allocations.", d1, "Chao's account received 81.6% of the profitable allocations", val("Chao", "profitable allocations", "81.6%", "Percentage")))
	lead := []model.Reply{calls(plan(issue("I1", "Who got the allocations?", "Conduct"))), calls(extract(d1, "allocations", "I1")), calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))}
	v := f.run(f.with(map[string][]model.Reply{"lead": lead, "extract_facts": {ex}, "draft_section": {section("Chao received 68.6% of the profitable allocations [F1].", "s")}}), Config{})
	fin := f.outputs(v.ID, "finish")
	if len(fin) != 1 || !strings.Contains(fin[0], "the figure 68.6 is not a handle") {
		t.Fatalf("finish %q", fin)
	}
	g := newFixture(t, "1. Allocations\nChao's account received 81.6% of the profitable allocations.\n")
	ex = facts(fd("Chao received 81.6% of profitable allocations.", g.docs[0].ID, "Chao's account received 81.6% of the profitable allocations", val("Chao", "profitable allocations", "81.6%", "Percentage")))
	lead = []model.Reply{calls(plan(issue("I1", "Who got the allocations?", "Conduct"))), calls(extract(g.docs[0].ID, "allocations", "I1")), calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))}
	w := g.run(g.with(map[string][]model.Reply{"lead": lead, "extract_facts": {ex}, "draft_section": {section("Chao received {Zephyr} of the profitable allocations [F1].", "s")}}), Config{})
	if !strings.Contains(deref(w.Output), "Chao received 81.6% of the profitable allocations [F1]") {
		t.Fatalf("handle path:\n%s", deref(w.Output))
	}
}

// TestExternalAuthorityFlagged (G5): an authority not in the record is marked inline, listed in an
// appendix and counted in Limitations; the matter's own caption is not.
func TestExternalAuthorityFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.with(map[string][]model.Reply{"draft_section": {section("Greystone claims {Zephyr} [F1], a claim weak under Morgan v. Groupon, Inc., 27 N.Y.3d 423 (2016).", "s")}}), Config{})
	out := deref(v.Output)
	if !strings.Contains(out, "Morgan v. Groupon, Inc., 27 N.Y.3d 423 (2016)"+authorityMarker) || !strings.Contains(out, "## Authorities Requiring Verification") ||
		!strings.Contains(out, "1 external authority cited above is not verified") {
		t.Fatalf("output:\n%s", out)
	}
}

// Carried vectors (v1 orchestrator/writer_integrity_test.go).
const okafCorpus = "okafor v. meridian cloudworks complaint. 28 u.s.c. § 2462 governs the limitations period."

func TestFlagExternalAuthorities(t *testing.T) {
	body := "The clause fails under *Morgan v. Groupon, Inc.*, 27 N.Y.3d 423 (2016) and NYLL §193. " +
		"See §7(d) of the Agreement and Okafor v. Meridian CloudWorks. Also 28 U.S.C. § 2462 controls. Again, Morgan v. Groupon applies."
	out, _ := flagExternalAuthorities(body, okafCorpus)
	for _, c := range []struct {
		s    string
		want bool
	}{{"Morgan v. Groupon, Inc.*, 27 N.Y.3d 423 (2016)" + authorityMarker, true}, {"NYLL §193" + authorityMarker, true},
		{"Okafor v. Meridian CloudWorks" + authorityMarker, false}, {"28 U.S.C. § 2462" + authorityMarker, false}, {"§7(d)" + authorityMarker, false}} {
		if strings.Contains(out, c.s) != c.want {
			t.Errorf("%q marked: %v, want %v\n%s", c.s, !c.want, c.want, out)
		}
	}
	if strings.Count(out, authorityMarker) != 2 || !strings.Contains(out, "## Authorities Requiring Verification") {
		t.Fatalf("markers %d:\n%s", strings.Count(out, authorityMarker), out)
	}
	clean := "The agreement's §7(d) and the Okafor v. Meridian CloudWorks record support the claim."
	if got, _ := flagExternalAuthorities(clean, okafCorpus); got != clean {
		t.Fatalf("clean body changed: %s", got)
	}
}

func TestAuthorityDedupeSpacingVariants(t *testing.T) {
	out, _ := flagExternalAuthorities("Under NYLL §652 and later NYLL § 652, plus NYLL §§ 652, and see NYLL § 74) here.", "irrelevant corpus")
	if got := strings.Count(out, "- NYLL §"); got != 2 || strings.Contains(out, "74)") {
		t.Fatalf("%d entries:\n%s", got, out)
	}
}

// markTranscript makes F1's citation a machine transcript (low confidence when low) right after
// the step that verified it: what docs.VerifyQuote reports for a quote on a transcribed page.
func markTranscript(f *fixture, low bool) {
	f.e.afterCommit = func(id string, n int) {
		if n != 4 {
			return
		}
		var data []byte
		f.st.DB().QueryRow(`SELECT data FROM ledger WHERE task_id = $1 AND kind = 'finding' AND key = 'F1'`, id).Scan(&data)
		var x Finding
		json.Unmarshal(data, &x)
		x.Citations[0].Method, x.Citations[0].Confidence, x.Citations[0].Low = "exact@transcript", 0.85, low
		f.st.DB().Exec(`UPDATE ledger SET data = $2 WHERE task_id = $1 AND kind = 'finding' AND key = 'F1'`, id, mustJSON(x))
	}
}

// TestLowAgreementFindingGated (G7/G11): a cited finding resting only on a low-confidence
// transcript page is gated individually; the run pauses at it.
func TestLowAgreementFindingGated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	markTranscript(f, true)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	if stop(v) != "awaiting_gate/awaiting_gate" || len(v.PendingGates) != 1 || v.PendingGates[0].FindingID != "F1" || v.PendingGates[0].Kind != "finding" {
		t.Fatalf("%s %+v", stop(v), v.PendingGates)
	}
	if out := deref(v.Output); !strings.Contains(out, "1 findings rest only on machine transcriptions of images, 1 of them on low-confidence pages") {
		t.Fatalf("evidence note:\n%s", out)
	}
}

// TestSubagentLedgerReadsSkipQuarantine (G3): a subagent reading the ledger never sees a
// quarantined finding.
func TestSubagentLedgerReadsSkipQuarantine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	read := ledgerOp("findings", "detail", "full")
	f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices"),
			fd("Talbot admitted liability.", d1, "Talbot admits it owes every invoice"))},
		"verify_quote":  {judgeNo},
		"draft_section": {calls(read), section("Greystone claims damages [F1].", "s")},
	}), Config{})
	var seen string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "draft_section" && len(c.Messages) > 3 {
			seen = c.Messages[len(c.Messages)-1].Content.(string)
		}
	}
	if !strings.Contains(seen, `"fid":"F1"`) || strings.Contains(seen, "F2") || strings.Contains(seen, "admitted") {
		t.Fatalf("the subagent's ledger read: %s", seen)
	}
}

// TestDeliverableBadgesTranscriptCitations and TestEvidenceNoteCountsTranscriptOnlyFindings (G11).
func TestDeliverableBadgesTranscriptCitations(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	markTranscript(f, false)
	task := f.submit(0)
	f.step(context.Background())
	out := deref(f.get(task.ID).Output)
	if !strings.Contains(out, "exact@transcript, 0.85; machine transcription (model) of an image: check against the original, /documents/"+f.docs[0].ID+"/file") {
		t.Fatalf("no badge:\n%s", out)
	}
}

func TestEvidenceNoteCountsTranscriptOnlyFindings(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	markTranscript(f, false)
	task := f.submit(0)
	f.step(context.Background())
	if out := deref(f.get(task.ID).Output); !strings.Contains(out, "1 findings rest only on machine transcriptions of images, 0 of them on low-confidence pages") {
		t.Fatalf("evidence note:\n%s", out)
	}
}

// TestRetrieveNeverLeavesMatter at the engine level (G1): a document outside the task's
// documentIds is never a search hit, and a quote from it never verifies.
func TestRetrieveNeverLeavesMatter(t *testing.T) {
	t.Parallel()
	f := newFixture(t, complaintText, docketText, "1. Damages\nSecret other matter: damages of $9,999,999 for the unpaid invoices.\n")
	other := f.docs[2]
	f.docs = f.docs[:2]
	search := tc("retrieve", m{"op": "search", "query": "damages unpaid invoices", "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil})
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(search), calls(extract(other.ID, "damages", "I1"))},
		"extract_facts": {facts(fd("Damages were $9,999,999.", other.ID, "Secret other matter: damages of $9,999,999"))},
	}), Config{})
	hits := f.outputs(v.ID, "retrieve")
	if len(hits) != 1 || strings.Contains(hits[0], other.ID) || strings.Contains(hits[0], "9,999,999") || !strings.Contains(hits[0], "644,910") {
		t.Fatalf("search: %q", hits)
	}
	if len(v.Findings) != 1 || v.Findings[0].Status != "quarantined" {
		t.Fatalf("a quote from outside the matter: %+v", v.Findings)
	}
}

// Carried (v1 orchestrator/quarantine_test.go), changed: v2 always quarantines (G3); v1's floor
// back-off and "caveat" policy, which let unverified findings into a memo, are dropped.
func TestQuarantineExcludesUnverified(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", f.docs[0].ID, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")),
			fd("Talbot admitted liability.", f.docs[0].ID, "Talbot admits it owes every invoice"))},
		"verify_quote": {judgeNo},
	}), Config{})
	use, _ := f.e.usable(context.Background(), v.ID)
	if len(v.Findings) != 2 || len(use) != 1 || strings.Contains(deref(v.Output), "admitted liability") {
		t.Fatalf("findings %d usable %d\n%s", len(v.Findings), len(use), deref(v.Output))
	}
}

func TestAppendEvidenceNote(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", f.docs[0].ID, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")),
			fd("Talbot admitted liability.", f.docs[0].ID, "Talbot admits it owes every invoice"))},
		"verify_quote": {judgeNo},
	}), Config{})
	if !strings.Contains(deref(v.Output), "## Evidence Note\n\n1 findings were verified against the matter's documents and 1 were quarantined") {
		t.Fatalf("%s", deref(v.Output))
	}
}
