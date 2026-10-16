// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// A lawyer sees their own time plus AI time on tasks they requested (never a
// profileId they pass); a partner may filter by any profileId; a principal
// with no profile sees nothing.
func TestListTimeEntriesVisibility(t *testing.T) {
	ts := timekeeping.NewTimeStore()
	if err := ts.Init(filepath.Join(t.TempDir(), "time.json")); err != nil {
		t.Fatal(err)
	}
	ts.Open(types.TimeEntry{ProfileID: "l1", TaskID: "t1"})
	ts.Open(types.TimeEntry{AgentID: "ai", RequestedByProfileID: "l1", TaskID: "t1"})
	ts.Open(types.TimeEntry{ProfileID: "l2", TaskID: "t2"})
	ts.Open(types.TimeEntry{AgentID: "ai", RequestedByProfileID: "l2", TaskID: "t2"})
	l := &Local{Time: ts}
	ctx := context.Background()
	count := func(u types.SessionUser, f timekeeping.TimeFilter) int {
		got, err := l.As(u).ListTimeEntries(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	lawyer := types.SessionUser{ProfileID: "l1", Role: "lawyer"}
	partner := types.SessionUser{ProfileID: "p1", Role: "partner"}
	for name, c := range map[string]struct {
		u    types.SessionUser
		f    timekeeping.TimeFilter
		want int
	}{
		"lawyer own + requested AI":  {lawyer, timekeeping.TimeFilter{}, 2},
		"lawyer cannot pick another": {lawyer, timekeeping.TimeFilter{ProfileID: "l2"}, 2},
		"lawyer no profile":          {types.SessionUser{Role: "lawyer"}, timekeeping.TimeFilter{}, 0},
		"partner all":                {partner, timekeeping.TimeFilter{}, 4},
		"partner by profile":         {partner, timekeeping.TimeFilter{ProfileID: "l2"}, 1},
	} {
		if got := count(c.u, c.f); got != c.want {
			t.Errorf("%s: %d entries, want %d", name, got, c.want)
		}
	}
}
