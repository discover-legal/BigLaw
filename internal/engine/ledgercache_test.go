// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// cacheRun is a submitted task with a run held by this engine (as drive holds it), so load caches.
func cacheRun(t *testing.T) (*fixture, *run) {
	t.Helper()
	f := newFixture(t)
	f.engine(nil, Config{})
	r := &run{e: f.e, t: f.submit(10)}
	f.e.mu.Lock()
	f.e.runs[r.t.ID] = r
	f.e.mu.Unlock()
	return f, r
}

func cachedFinding(fid, claim string) Finding {
	return Finding{Finding: analysis.Finding{FID: fid, IssueID: "I1", Claim: claim, Verified: true,
		Quotes: []analysis.Quote{{DocumentID: "d", Quote: claim}}, Values: []analysis.Value{{Entity: "E", Value: "$1"}}},
		Citations: []Citation{{DocumentID: "d", Quote: claim}}, Check: &SectionCheck{Answer: "yes"}}
}

// dbWrite writes a row around the run, as a gate decision or another process does.
func dbWrite(t *testing.T, r *run, kind, key string, v any) {
	t.Helper()
	if _, err := r.e.db.Exec(upsertSQL(true), r.t.ID, kind, key, mustJSON(v), 0); err != nil {
		t.Fatal(err)
	}
}

func loadedClaims(t *testing.T, e *Engine, id string) []string {
	t.Helper()
	fs, err := load[Finding](context.Background(), e, id, kFinding)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.FID+"="+f.Claim)
	}
	return out
}

// TestLoadCacheNeverStale: every write path is seen at the next load of a running task: the step
// buffer (new rows, and an update over a cached committed row), and rows written around the run (a
// gate decision, another process) both new and updated; a load returns what an uncached load does.
func TestLoadCacheNeverStale(t *testing.T) {
	t.Parallel()
	f, r := cacheRun(t)
	ctx, id := context.Background(), r.t.ID
	dbWrite(t, r, kFinding, "F1", cachedFinding("F1", "one"))
	if got := loadedClaims(t, f.e, id); !reflect.DeepEqual(got, []string{"F1=one"}) {
		t.Fatalf("first load %v", got)
	}
	dbWrite(t, r, kFinding, "F1", cachedFinding("F1", "one, updated")) // around the run: an update
	dbWrite(t, r, kFinding, "F2", cachedFinding("F2", "two"))          // and a new row
	if got := loadedClaims(t, f.e, id); !reflect.DeepEqual(got, []string{"F1=one, updated", "F2=two"}) {
		t.Fatalf("after writes around the run %v", got)
	}
	if err := f.e.write(ctx, id, kFinding, "F2", cachedFinding("F2", "two, buffered"), true); err != nil {
		t.Fatal(err)
	}
	if err := f.e.write(ctx, id, kFinding, "F3", cachedFinding("F3", "three"), false); err != nil {
		t.Fatal(err)
	}
	if got := loadedClaims(t, f.e, id); !reflect.DeepEqual(got, []string{"F1=one, updated", "F2=two, buffered", "F3=three"}) {
		t.Fatalf("after buffered writes %v", got)
	}
	r.mu.Lock()
	r.buf = nil // the step failed: its writes are gone, and so must they be from the reads
	r.mu.Unlock()
	if got := loadedClaims(t, f.e, id); !reflect.DeepEqual(got, []string{"F1=one, updated", "F2=two"}) {
		t.Fatalf("after the buffer dropped %v", got)
	}
	cached, err := load[Finding](ctx, f.e, id, kFinding)
	if err != nil {
		t.Fatal(err)
	}
	f.e.mu.Lock()
	delete(f.e.runs, id) // the run ended: its cache with it
	f.e.mu.Unlock()
	plain, err := load[Finding](ctx, f.e, id, kFinding)
	if err != nil || !reflect.DeepEqual(cached, plain) || string(mustJSON(cached)) != string(mustJSON(plain)) {
		t.Fatalf("cached load differs from a plain one: %v\n%s\n%s", err, mustJSON(cached), mustJSON(plain))
	}
}

// TestLoadCacheIsolatesCallers: a caller changing what load returned (appending to a finding's
// citations, editing a value or its check) changes neither the next load nor another caller's rows.
func TestLoadCacheIsolatesCallers(t *testing.T) {
	t.Parallel()
	f, r := cacheRun(t)
	ctx, id := context.Background(), r.t.ID
	dbWrite(t, r, kFinding, "F1", cachedFinding("F1", "one"))
	a, _ := load[Finding](ctx, f.e, id, kFinding)
	b, _ := load[Finding](ctx, f.e, id, kFinding)
	want := string(mustJSON(b))
	a[0].Citations = append(a[0].Citations[:0], Citation{Quote: "changed"})
	a[0].Values[0].Value = "$2"
	a[0].Check.Answer = "no"
	a[0].Quotes = append(a[0].Quotes, analysis.Quote{Quote: "extra"})
	c, _ := load[Finding](ctx, f.e, id, kFinding)
	if string(mustJSON(b)) != want || string(mustJSON(c)) != want {
		t.Fatalf("a caller's change reached other rows:\nwant %s\nb    %s\nc    %s", want, mustJSON(b), mustJSON(c))
	}
}

// TestLoadCacheConcurrent (run under -race): parallel loads, buffered writes and writes around the
// run; each reader mutates its rows, and sees every finding written before it started, each whole.
func TestLoadCacheConcurrent(t *testing.T) {
	t.Parallel()
	f, r := cacheRun(t)
	ctx, id := context.Background(), r.t.ID
	var mu sync.Mutex
	written := 0
	var wg sync.WaitGroup
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 15 {
				fid := fmt.Sprintf("F%d_%d", w, i)
				var err error
				if w == 0 {
					err = f.e.write(ctx, id, kFinding, fid, cachedFinding(fid, "claim "+fid), false)
				} else {
					_, err = r.e.db.Exec(upsertSQL(false), id, kFinding, fid, mustJSON(cachedFinding(fid, "claim "+fid)), 0)
				}
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				written++
				mu.Unlock()
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 15 {
				mu.Lock()
				before := written
				mu.Unlock()
				fs, err := load[Finding](ctx, f.e, id, kFinding)
				if err != nil || len(fs) < before {
					t.Errorf("load: %v, %d rows, %d written before", err, len(fs), before)
					return
				}
				for i := range fs {
					if fs[i].Claim != "claim "+fs[i].FID || len(fs[i].Citations) != 1 || fs[i].Citations[0].Quote != fs[i].Claim {
						t.Errorf("row %+v", fs[i])
					}
					fs[i].Citations[0].Quote, fs[i].Values = "mutated", append(fs[i].Values, analysis.Value{})
				}
			}
		}()
	}
	wg.Wait()
	if got := loadedClaims(t, f.e, id); len(got) != 30 {
		t.Fatalf("%d findings", len(got))
	}
}

// TestDeepCopy: nil and empty stay apart (they marshal differently), and nothing is shared.
func TestDeepCopy(t *testing.T) {
	t.Parallel()
	type inner struct{ S []int }
	type outer struct {
		Nil, Empty []string
		M          map[string]any
		P          *inner
		A          [2]inner
		Raw        json.RawMessage
		L          []inner
	}
	in := outer{L: []inner{{S: []int{3}}}, Empty: []string{}, M: map[string]any{"k": []any{"x"}}, P: &inner{S: []int{1}}, A: [2]inner{{S: []int{2}}}, Raw: json.RawMessage(`{}`)}
	out := deepCopy(reflect.ValueOf(in)).Interface().(outer)
	if !reflect.DeepEqual(in, out) || string(mustJSON(in)) != string(mustJSON(out)) || out.Nil != nil || out.Empty == nil {
		t.Fatalf("copy %+v", out)
	}
	out.M["k"].([]any)[0], out.P.S[0], out.A[0].S[0], out.Raw[0], out.L[0].S[0] = "y", 9, 9, '[', 9
	if in.M["k"].([]any)[0] != "x" || in.P.S[0] != 1 || in.A[0].S[0] != 2 || in.Raw[0] != '{' || in.L[0].S[0] != 3 {
		t.Fatalf("shared: %+v", in)
	}
	type hidden struct{ S []int }
	type exposed struct{ hidden }
	defer func() {
		if recover() == nil {
			t.Fatal("an unexported field holding references was copied as shared, not refused")
		}
	}()
	hasRefs(reflect.TypeOf(exposed{}))
}
