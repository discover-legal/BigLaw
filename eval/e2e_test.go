// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package eval

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/api"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

// slow is test plumbing around model.Fake: every call takes a moment, so a browser watching the
// run sees its steps arrive one by one.
type slow struct{ *model.Fake }

func (s slow) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(400 * time.Millisecond):
	}
	return s.Fake.Chat(ctx, req)
}

// TestE2EServe is not a check: with E2E_ADDR set it serves the real API, engine and the built UI
// (ui/dist) over a scripted model on that address, for the browser end-to-end run (README § Test
// it), until interrupted. A matter M-1 holds two documents; one memo task's model calls are
// scripted.
func TestE2EServe(t *testing.T) {
	addr := os.Getenv("E2E_ADDR")
	if addr == "" {
		t.Skip("E2E_ADDR is not set")
	}
	st, err := store.Open(context.Background(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := docs.New(st)
	for name, text := range map[string]string{
		"complaint.md": "1. Parties\nGreystone Packaging Co. sued Talbot Fresh Foods, Inc. on October 9, 2026.\n\n2. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n",
		"docket.md":    "Docket\nTalbot was served on 10/21/2026 via its registered agent. The answer period is 21 days.\n",
	} {
		doc, err := d.IngestText(context.Background(), name, text)
		if err != nil {
			t.Fatal(err)
		}
		d.File(context.Background(), doc.ID, "M-1")
	}
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "What does Greystone claim?", "class": "Conduct", "practice": "litigation"},
		{"id": "I2", "question": "When was Talbot served?", "class": "Requirement", "practice": "litigation"}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
	ex := func(q, issue string) model.ToolCall {
		return call("extract_facts", m{"question": q, "documentId": nil, "sectionId": nil, "through": nil, "offset": nil, "query": q, "issueId": issue})
	}
	fd := func(claim, doc, quote string, values ...m) m {
		return m{"issueId": nil, "claim": claim, "quotes": []m{{"documentId": doc, "quote": quote, "locator": nil}}, "values": append([]m{}, values...), "entities": []m{}, "relations": []m{}, "confidence": 1}
	}
	sc := map[string][]model.Reply{
		"lead": {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{ex("damages claimed", "I1"), ex("service date", "I2")}},
			{ToolCalls: []model.ToolCall{call("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": nil}), call("draft_section", m{"issueId": "I2", "fids": []string{"F2"}, "revise": nil})}},
			{ToolCalls: []model.ToolCall{call("committee", m{})}}, {ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}},
		"extract_facts": {reply(m{"findings": []m{fd("Greystone claims damages of $644,910 for unpaid invoices.", "complaint.md", "Greystone claims damages of $644,910 for unpaid invoices",
			m{"entity": "Greystone", "metric": "damages", "value": "$644,910", "unit": nil, "date": nil, "class": "Money"})}, "gaps": []string{}}),
			reply(m{"findings": []m{fd("Talbot was served on 10/21/2026 through its registered agent.", "docket.md", "Talbot was served on 10/21/2026 via its registered agent")}, "gaps": []string{}})},
		"draft_section": {reply(m{"markdown": "Greystone claims damages of {Zephyr} for unpaid invoices [F1].", "summary": "Damages {Zephyr} [F1]."}),
			reply(m{"markdown": "Talbot was served on 10/21/2026 via its registered agent [F2], which starts its time to answer.", "summary": "Service [F2]."})},
	}
	for k, v := range critics {
		sc[k] = v
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e := engine.New(st, d, slow{readerFake(sc)}, model.Roles{Lead: "fake-lead"}, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, log)
	mux := api.Handler(d, e, os.DirFS("../ui/dist"), log)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go e.Start(ctx)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() { <-ctx.Done(); srv.Close() }()
	t.Logf("serving on %s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
