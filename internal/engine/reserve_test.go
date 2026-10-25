// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// r7 L4: the figure reader's reserve is held from the quotes' judges only when a finding states a
// number for it to read; with none, the reader makes no call, and the judges may spend that call
// (on a last-calls grant, a paraphrase that needed it was quarantined unjudged).
func TestReaderReserveFreeWhenNoNumbers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Notice\nThe Seller shall give the Buyer written notice of any claim promptly.\nThe Buyer shall keep the Seller informed of the defence.\n")
	f.engine(map[string][]model.Reply{"verify_quote": {judged(true), judged(true)}}, Config{})
	task := f.submit(0)
	r := &run{e: f.e, t: task}
	rt, err := f.e.runtime(r)
	if err != nil {
		t.Fatal(err)
	}
	d := f.docs[0].ID
	draft := func(claim, quote string) draftFinding {
		return draftFinding{Claim: claim, Quotes: []analysis.Quote{{DocumentID: d, Quote: quote}}}
	}
	drafts := []draftFinding{draft("The Seller must notify the Buyer of claims.", "the Seller must notify the Buyer in writing of every claim without delay"),
		draft("The Buyer must keep the Seller informed.", "the Buyer must keep the Seller up to date on the defence")}
	b := agent.NewBudget(2, 1, 0)
	if _, _, err := f.e.admit(context.Background(), rt, agent.Call{TaskID: task.ID, DocumentIDs: task.DocumentIDs}, b, agent.Grant{Calls: 1}, "extract_facts", "I1", "fake", drafts, nil); err != nil {
		t.Fatal(err)
	}
	fs := ledgerFindings(t, f, task.ID)
	if len(fs) != 2 || !fs[0].Verified || !fs[1].Verified {
		t.Fatalf("with no number to read, a judge went unfunded: %+v", fs)
	}
}
