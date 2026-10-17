// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Identity-keyed conflict holds: a hold belongs to the person, not to one
// document; clearing a hold re-runs the check; the service serialises the
// check → admit sequence; held prospects cannot push facts into the CRM.

package intake

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/crm"
)

func personReq(ref, name, doc string, parties ...string) SubmissionRequest {
	r := SubmissionRequest{
		ExternalID: doc,
		Client:     ClientRef{ExternalID: ref, Name: name, Email: ref + "@example.com"},
		Title:      "Draft " + doc,
		Content:    "Draft text of " + doc,
	}
	for _, p := range parties {
		r.AdverseParties = append(r.AdverseParties, PartyRef{Name: p})
	}
	return r
}

func mustSubmit(t *testing.T, svc *Service, req SubmissionRequest) string {
	t.Helper()
	sub, err := svc.Submit(sysCtx(), req)
	if err != nil {
		t.Fatalf("Submit %s: %v", req.ExternalID, err)
	}
	return sub.Status
}

// Defect 1: Pat's first document is held; a second document that does not
// name the conflicting client is held too (open hold on the identity), and
// after a decline every later document is re-held as previously declined —
// never auto-admitted.
func TestHoldIsKeyedToTheIdentity(t *testing.T) {
	svc, _, cs, repo := newTestIntake(t)
	_, _ = cs.Create("Rex Existing", "C-1", nil, "")
	ctx := sysCtx()

	doc1, err := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "doc1", "Rex Existing"))
	if err != nil || doc1.Status != StatusConflictHold {
		t.Fatalf("doc1: %v %+v", err, doc1)
	}
	doc2, err := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "doc2"))
	if err != nil || doc2.Status != StatusConflictHold {
		t.Fatalf("second document slipped past the identity's hold: %v %+v", err, doc2)
	}
	if rec := DecodeConflict(*doc2); len(rec.OpenHolds) != 1 || rec.OpenHolds[0] != doc1.ID || len(rec.Hits) != 1 {
		t.Fatalf("doc2 should carry the open hold and the identity-wide hit: %+v", rec)
	}
	if len(cs.List()) != 1 || docCount(t, repo) != 0 {
		t.Fatal("held identity reached the roster or the knowledge store")
	}

	// Decline both; the identity is now marked.
	for _, id := range []string{doc1.ID, doc2.ID} {
		if _, err := svc.ResolveConflict(ctx, id, false, "acting for Rex", "partner-1"); err != nil {
			t.Fatalf("decline %s: %v", id, err)
		}
	}
	doc3, err := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "doc3"))
	if err != nil || doc3.Status != StatusConflictHold {
		t.Fatalf("previously declined identity not re-held: %v %+v", err, doc3)
	}
	if rec := DecodeConflict(*doc3); len(rec.PreviouslyDeclined) != 2 {
		t.Fatalf("previouslyDeclined flag missing: %+v", rec)
	}
	// Re-submitting a declined document re-holds it too (no bypass by
	// editing out the party).
	again, err := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "doc1"))
	if err != nil || again.Status != StatusConflictHold {
		t.Fatalf("declined document landed on re-submit: %v %+v", err, again)
	}

	// A partner may still admit the person — but only explicitly, and the
	// next document is held again.
	if _, err := svc.ResolveConflict(ctx, doc3.ID, true, "screened; waiver on file", "partner-1"); err != nil {
		t.Fatalf("acknowledge doc3: %v", err)
	}
	doc4, err := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "doc4"))
	if err != nil || doc4.Status != StatusConflictHold {
		t.Fatalf("declined identity auto-admitted: %v %+v", err, doc4)
	}
}

// Defect 2: clearing a hold re-runs the check. Dan is held; Eve (Dan's
// spouse) is admitted meanwhile naming Dan; acknowledging Dan's original
// hit must surface the Eve–Dan conflict instead of landing him.
func TestResolveConflictRerunsTheCheck(t *testing.T) {
	svc, _, cs, _ := newTestIntake(t)
	_, _ = cs.Create("Robert Smith", "C-1", nil, "")
	ctx := sysCtx()

	dan, err := svc.Submit(ctx, personReq("auth0|dan", "Dan Doe", "dan-1", "Robert Smith"))
	if err != nil || dan.Status != StatusConflictHold {
		t.Fatalf("dan: %v %+v", err, dan)
	}
	if st := mustSubmit(t, svc, personReq("auth0|eve", "Eve Doe", "eve-1", "Dan Doe")); st != StatusReceived {
		t.Fatalf("eve should land (Dan is not a client yet): %s", st)
	}
	got, err := svc.ResolveConflict(ctx, dan.ID, true, "Robert Smith is a former client", "partner-1")
	if !errors.Is(err, ErrReheld) || got == nil || got.Status != StatusConflictHold {
		t.Fatalf("acknowledging stale hits landed Dan: %v %+v", err, got)
	}
	rec := DecodeConflict(*got)
	foundEve := false
	for _, h := range rec.Hits {
		if h.ConflictingClientName == "Eve Doe" {
			foundEve = true
		}
	}
	if !foundEve || len(rec.unacknowledged()) != 1 {
		t.Fatalf("new Eve hit not surfaced: %+v", rec)
	}
	// Acknowledging what is now shown lands him.
	landed, err := svc.ResolveConflict(ctx, dan.ID, true, "both spouses consent in writing", "partner-1")
	if err != nil || landed.Status != StatusReceived {
		t.Fatalf("second acknowledgement: %v %+v", err, landed)
	}
}

// Defect 2 (race): two simultaneous first contacts that conflict with each
// other cannot both pass the check — the service serialises check → admit.
func TestConcurrentMutualConflictOnlyOneLands(t *testing.T) {
	for i := 0; i < 20; i++ {
		svc, _, _, _ := newTestIntake(t)
		var wg sync.WaitGroup
		statuses := make([]string, 2)
		for j, r := range []SubmissionRequest{
			personReq("auth0|a", "Alice Able", "a-1", "Bob Baker"),
			personReq("auth0|b", "Bob Baker", "b-1", "Alice Able"),
		} {
			wg.Add(1)
			go func(j int, r SubmissionRequest) {
				defer wg.Done()
				sub, err := svc.Submit(sysCtx(), r)
				if err != nil {
					t.Errorf("submit: %v", err)
					return
				}
				statuses[j] = sub.Status
			}(j, r)
		}
		wg.Wait()
		held := 0
		for _, s := range statuses {
			if s == StatusConflictHold {
				held++
			}
		}
		if held != 1 {
			t.Fatalf("run %d: want exactly one held, got %v", i, statuses)
		}
	}
}

// Defect 8: a double-click at first contact is idempotent.
func TestConcurrentFirstContactIsIdempotent(t *testing.T) {
	svc, crmSvc, _, _ := newTestIntake(t)
	var wg sync.WaitGroup
	ids := make([]string, 6)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub, err := svc.Submit(sysCtx(), personReq("auth0|dbl", "Dee Double", "dbl-1"))
			if err != nil {
				t.Errorf("submit %d: %v", i, err)
				return
			}
			ids[i] = sub.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("double-click duplicated the submission: %v", ids)
		}
	}
	profiles, _ := crmSvc.List(sysCtx())
	if len(profiles) != 1 {
		t.Fatalf("double-click created %d profiles", len(profiles))
	}
}

// Defect 4: the check runs on every name the identity supplies (not just
// the one frozen at first contact), and a weak clean check is recorded.
func TestEveryNameIsCheckedAndWeakChecksRecorded(t *testing.T) {
	svc, _, cs, _ := newTestIntake(t)
	_, _ = cs.Create("Megacorp", "C-1", []string{"Alice Jones"}, "")
	ctx := sysCtx()

	first := personReq("auth0|alice", "alice@x.com", "a-1")
	sub, err := svc.Submit(ctx, first)
	if err != nil || sub.Status != StatusReceived {
		t.Fatalf("first: %v %+v", err, sub)
	}
	rec := DecodeConflict(*sub)
	if fmt.Sprint(rec.ConflictUnchecked) != "[name_is_email no_adverse_parties]" {
		t.Fatalf("weak check not recorded: %+v", rec.ConflictUnchecked)
	}
	held, err := svc.Submit(ctx, personReq("auth0|alice", "Alice Jones", "a-2"))
	if err != nil || held.Status != StatusConflictHold {
		t.Fatalf("later real name not checked: %v %+v", err, held)
	}
}

// Defect 3: a held (or never-admitted) prospect cannot push facts into the
// CRM; an admitted client's adverse_party facts run through the check.
func TestProposeFactsGates(t *testing.T) {
	svc, crmSvc, cs, _ := newTestIntake(t)
	_, _ = cs.Create("Rex Existing", "C-1", nil, "")
	ctx := sysCtx()
	facts := []crm.FactInput{{Category: "note", Value: "my story"}}

	if _, err := svc.ProposeFacts(ctx, "auth0|ghost", facts); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("unknown identity: %v", err)
	}
	mustSubmit(t, svc, personReq("auth0|pat", "Pat Prospect", "p-1", "Rex Existing"))
	if _, err := svc.ProposeFacts(ctx, "auth0|pat", facts); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("held prospect proposed facts: %v", err)
	}
	if pending, _ := crmSvc.PendingQueue(ctx, crm.RoleLawyer); len(pending) != 0 {
		t.Fatalf("held prospect's facts reached the CRM: %+v", pending)
	}

	// An admitted client with an open hold on a later document: 409 too.
	mustSubmit(t, svc, personReq("auth0|dana", "Dana Client", "d-1"))
	mustSubmit(t, svc, personReq("auth0|dana", "Dana Client", "d-2", "Rex Existing"))
	if _, err := svc.ProposeFacts(ctx, "auth0|dana", facts); !errors.Is(err, ErrConflictHold) {
		t.Fatalf("client with an open hold proposed facts: %v", err)
	}

	// An admitted client naming an existing client as adverse party: the
	// fact is recorded (lawyer-gated), the profile is flagged and the next
	// submission is held.
	mustSubmit(t, svc, personReq("auth0|carl", "Carl Client", "c-1"))
	if _, err := svc.ProposeFacts(ctx, "auth0|carl", []crm.FactInput{{Category: "adverse_party", Value: "Rex Existing"}}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	p, _, _ := crmSvc.GetByExternalRef(ctx, "auth0|carl")
	if !p.ConflictFlag {
		t.Fatal("adverse_party fact hitting a client did not flag the profile")
	}
	if st := mustSubmit(t, svc, personReq("auth0|carl", "Carl Client", "c-2")); st != StatusConflictHold {
		t.Fatalf("adverse_party fact ignored by the next check: %s", st)
	}
}

// Defect 6: held submissions and conflict detail are partner/assignee-only;
// claiming and acting respect assignment.
func TestVisibilityAndAssignment(t *testing.T) {
	svc, _, cs, _ := newTestIntake(t)
	_, _ = cs.Create("Rex Existing", "C-1", nil, "")
	ctx := sysCtx()
	held, _ := svc.Submit(ctx, personReq("auth0|pat", "Pat Prospect", "p-1", "Rex Existing"))
	clean, _ := svc.Submit(ctx, personReq("auth0|dana", "Dana Client", "d-1"))
	lee, kim := Viewer{ProfileID: "lee"}, Viewer{ProfileID: "kim"}

	q, _ := svc.Queue(ctx, lee)
	if len(q) != 1 || q[0].ID != clean.ID {
		t.Fatalf("lawyer queue shows held submissions: %+v", q)
	}
	if _, err := svc.Claim(ctx, held.ID, lee); !errors.Is(err, ErrNotFound) {
		t.Fatalf("lawyer claimed a held submission: %v", err)
	}
	if _, err := svc.Update(ctx, clean.ID, StatusReady, "", lee); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unclaimed update allowed: %v", err)
	}
	if _, err := svc.Claim(ctx, clean.ID, lee); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := svc.Claim(ctx, clean.ID, kim); !errors.Is(err, ErrForbidden) {
		t.Fatalf("claim stolen: %v", err)
	}
	if _, err := svc.Update(ctx, clean.ID, StatusReady, "", kim); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-assignee update allowed: %v", err)
	}
	if _, err := svc.GetForViewer(ctx, clean.ID, kim); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-assignee task allowed: %v", err)
	}
	if _, err := svc.Update(ctx, clean.ID, StatusReady, "", lee); err != nil {
		t.Fatalf("assignee update: %v", err)
	}
	if _, err := svc.Claim(ctx, held.ID, Viewer{ProfileID: "partner-1", Partner: true}); err != nil {
		t.Fatalf("partner claim of a held submission: %v", err)
	}
	claimed, _, _ := svc.Get(ctx, clean.ID)
	if !lee.SeesDetail(*claimed) || kim.SeesDetail(*claimed) {
		t.Fatal("conflict detail must go to the assignee only (and partners)")
	}
}

// Defect 7: the portal reads one submission only for its own client.
func TestGetForClientScopes(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	sub, _ := svc.Submit(sysCtx(), personReq("auth0|dana", "Dana Client", "d-1"))
	if _, err := svc.GetForClient(sysCtx(), sub.ID, "auth0|dana"); err != nil {
		t.Fatalf("own submission: %v", err)
	}
	if _, err := svc.GetForClient(sysCtx(), sub.ID, "auth0|eve"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other client's submission readable: %v", err)
	}
	if _, err := svc.GetForClient(sysCtx(), sub.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unscoped read allowed: %v", err)
	}
}

// Party caps are refusals, never silent truncation of the check.
func TestPartyCaps(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	req := personReq("auth0|many", "Mona Many", "m-1")
	for i := 0; i < maxAdverseParties+1; i++ {
		req.AdverseParties = append(req.AdverseParties, PartyRef{Name: fmt.Sprintf("Party Number %d", i)})
	}
	if _, err := svc.Submit(sysCtx(), req); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-cap submission accepted: %v", err)
	}
	for d := 0; d*maxAdverseParties <= maxIdentityParties; d++ {
		r := personReq("auth0|many", "Mona Many", fmt.Sprintf("m-%d", d))
		for i := 0; i < maxAdverseParties; i++ {
			r.AdverseParties = append(r.AdverseParties, PartyRef{Name: fmt.Sprintf("Party %d-%d", d, i)})
		}
		_, err := svc.Submit(sysCtx(), r)
		if (d+1)*maxAdverseParties > maxIdentityParties {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("identity cap not enforced at doc %d: %v", d, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("doc %d: %v", d, err)
		}
	}
}
