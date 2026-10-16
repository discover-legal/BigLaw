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

// A symlinked store path keeps its link: the target is replaced (0600), so
// the real file never holds stale data.
func TestWriteFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "vol", "state.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(dir, "mid.json")
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink("vol/state.json", mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mid, link); err != nil { // chain: link → mid → real
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link replaced by a plain file")
	}
	got, _ := os.ReadFile(real)
	fi, _ := os.Stat(real)
	if string(got) != "new" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("target %q perm %v", got, fi.Mode().Perm())
	}

	// Dangling link: the first write creates the target.
	dangling := filepath.Join(dir, "d.json")
	if err := os.Symlink(filepath.Join(dir, "vol", "fresh.json"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(dangling, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "vol", "fresh.json")); string(got) != "x" {
		t.Fatalf("dangling target = %q", got)
	}

	// A loop is an error, not a hang.
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	_ = os.Symlink(b, a)
	_ = os.Symlink(a, b)
	if err := WriteFile(a, []byte("x"), 0o600); err == nil {
		t.Fatal("symlink loop must fail")
	}
}
