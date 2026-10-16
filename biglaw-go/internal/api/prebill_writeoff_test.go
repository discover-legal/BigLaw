// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/billing"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// A pre-bill drafted before one of its entries was written off cannot be
// approved: it would invoice non-billable time at its old amount.
func TestPreBillWithWrittenOffEntryCannotBeApproved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	ts := timekeeping.NewTimeStore()
	if err := ts.Init(filepath.Join(dir, "time.json")); err != nil {
		t.Fatal(err)
	}
	rate := 300.0
	open := func(task string) types.TimeEntry {
		e := ts.Open(types.TimeEntry{ProfileID: "p", TaskID: task, Event: types.TimeEventTaskRun, BillingRate: &rate,
			StartedAt: time.Now().Add(-time.Hour), MatterNumber: "M-1"})
		return *ts.Close(e.ID)
	}
	bad, good := open("t-failed"), open("t-ok")

	billingOnce.Do(func() {})
	prev := billingPreBills
	billingPreBills = billing.NewPreBillStore(filepath.Join(dir, "prebills.json"))
	t.Cleanup(func() { billingPreBills, billingOnce = prev, sync.Once{} })
	dirty := billingPreBills.Create("M-1", "C-1", "p", []types.TimeEntry{bad, good})
	clean := billingPreBills.Create("M-1", "C-1", "p", []types.TimeEntry{good})
	ts.WriteOff(func(e types.TimeEntry) string {
		if e.ID == bad.ID {
			return "task failed (historical write-off)"
		}
		return ""
	})

	s := &Server{cfg: &config.Config{}, time: ts}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserKey, &types.SessionUser{ProfileID: "partner-1", Role: types.RolePartner})
		c.Next()
	})
	r.PATCH("/pre-bills/:id", s.handlePatchPreBill)

	for _, id := range []string{dirty.ID, clean.ID} {
		if w := sendJSON(t, r, http.MethodPatch, "/pre-bills/"+id, map[string]string{"status": "reviewed"}); w.Code != http.StatusOK {
			t.Fatalf("review %s: %d %s", id, w.Code, w.Body)
		}
	}
	if w := sendJSON(t, r, http.MethodPatch, "/pre-bills/"+dirty.ID, map[string]string{"status": "approved"}); w.Code != http.StatusConflict {
		t.Fatalf("approve with written-off entry: %d %s", w.Code, w.Body)
	}
	if got := billingPreBills.GetByID(dirty.ID); got.Status != billing.PreBillReviewed {
		t.Fatalf("status changed: %s", got.Status)
	}
	if w := sendJSON(t, r, http.MethodPatch, "/pre-bills/"+clean.ID, map[string]string{"status": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve clean bill: %d %s", w.Code, w.Body)
	}
}
