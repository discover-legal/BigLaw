// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package storetest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// Slots bounds how many tests hold a test database at once across every test
// process (go test -p runs one per package): a test takes a slot in DSN and
// gives it back when it ends. With Pool connections per store, the database
// sees about Slots × (Pool+1) connections at most, whatever -p and -parallel
// are (CI's Postgres allows 100). Subtests share their top-level test's slot,
// so a test whose parallel subtests each call DSN cannot wait on itself.
const Slots = 8

var (
	slotMu sync.Mutex
	held   = map[string]*heldSlot{} // top-level test name → its slot
)

type heldSlot struct {
	release func()
	refs    int
}

// hold takes a slot for t's top-level test (waiting for one, see Slots) and
// gives it back when the last test sharing it ends.
func hold(t testing.TB, base string) {
	t.Helper()
	root, _, _ := strings.Cut(t.Name(), "/")
	slotMu.Lock()
	s := held[root]
	if s == nil {
		slotMu.Unlock()
		sum := sha256.Sum256([]byte(base))
		release, err := takeSlot("biglaw-storetest-"+hex.EncodeToString(sum[:6]), Slots)
		if err != nil {
			t.Fatalf("storetest: take a database slot: %v", err)
			return
		}
		slotMu.Lock()
		if s = held[root]; s != nil { // a sibling subtest took one meanwhile: share it
			release()
		} else {
			s = &heldSlot{release: release}
			held[root] = s
		}
	}
	s.refs++
	slotMu.Unlock()
	t.Cleanup(func() {
		slotMu.Lock()
		defer slotMu.Unlock()
		if s.refs--; s.refs == 0 {
			delete(held, root)
			s.release()
		}
	})
}
