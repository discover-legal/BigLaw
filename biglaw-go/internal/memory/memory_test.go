// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package memory

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// The TS store paged deletions at 5000 entries per search and originally
// truncated there (fixed in f9f5bad). The Go store filters the whole slice in
// one pass, so there is no page cap — this test locks that in with a count
// well above the old TS page size.
func TestDeleteByTaskIDDeletesAllEntries(t *testing.T) {
	s := &InterRoundStore{}
	const target = 6001
	for i := 0; i < target; i++ {
		s.entries = append(s.entries, types.MemoryEntry{ID: "t", TaskID: "task-1"})
	}
	s.entries = append(s.entries, types.MemoryEntry{ID: "k", TaskID: "task-2"})

	s.DeleteByTaskID("task-1")

	if len(s.entries) != 1 {
		t.Fatalf("expected 1 surviving entry, got %d", len(s.entries))
	}
	if s.entries[0].TaskID != "task-2" {
		t.Fatalf("wrong entry survived: %+v", s.entries[0])
	}
}
