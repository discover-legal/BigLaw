// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	if err := WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil { // pre-existing loose file
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(got) != `{"v":2}` || fi.Mode().Perm() != 0o600 {
		t.Fatalf("content %q perm %v", got, fi.Mode().Perm())
	}
	// Concurrent writers never corrupt the file or leave temp files behind.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = WriteFile(path, []byte(fmt.Sprintf(`{"v":%d}`, i)), 0o600)
		}(i)
	}
	wg.Wait()
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}
