// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ocg

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Regression (independent verifier): persist marshalled the shared
// *OcgDocument outside the lock while RecordViolations/RecordOutcome mutated
// RuleStats — a data race that can fault with a concurrent map read/write.
// Run with -race. GetByClient snapshots are also read concurrently.
func TestPersistAndSnapshotsDoNotRace(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "ocg.json"), nil, "")
	s.docs["c1"] = &types.OcgDocument{ID: "d1", ClientID: "c1"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(2)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				s.RecordViolations("c1", []types.OcgSuggestion{{RuleID: fmt.Sprintf("r%d-%d", g, i)}})
				s.RecordOutcome("c1", fmt.Sprintf("r%d", i%5), "accepted")
			}
		}(g)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_, _ = json.Marshal(s.GetByClient("c1"))
			}
		}()
	}
	wg.Wait()
	if n := len(s.GetByClient("c1").RuleStats); n != 8*40+5 {
		t.Fatalf("rule stats = %d, want %d", n, 8*40+5)
	}
}
