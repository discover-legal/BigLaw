// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package blob

import (
	"bytes"
	"testing"
)

func TestDiskStoreRoundTrip(t *testing.T) {
	s, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskStore: %v", err)
	}
	key := "doc-1/att-1"
	data := []byte("%PDF-1.7 ...bytes...")
	if err := s.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("round-trip mismatch: %q", got)
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(key); err == nil {
		t.Error("expected error reading deleted blob")
	}
	// Delete of an absent key is a no-op.
	if err := s.Delete(key); err != nil {
		t.Errorf("Delete absent: %v", err)
	}
}

func TestDiskStoreRejectsTraversal(t *testing.T) {
	s, _ := NewDiskStore(t.TempDir())
	for _, key := range []string{"../escape", "../../etc/passwd", "a/../../b"} {
		if err := s.Put(key, []byte("x")); err == nil {
			t.Errorf("Put(%q) should be rejected (path traversal)", key)
		}
	}
}
