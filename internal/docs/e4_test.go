// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

type tally struct{ facts, found, strings, stringsFound int }

func (a *tally) add(b tally) {
	a.facts, a.found, a.strings, a.stringsFound = a.facts+b.facts, a.found+b.found, a.strings+b.strings, a.stringsFound+b.stringsFound
}

func (a tally) String() string {
	return fmt.Sprintf("%d/%d facts = %.3f (every evidence string: %d/%d = %.3f)", a.found, a.facts, float64(a.found)/float64(a.facts),
		a.stringsFound, a.strings, float64(a.stringsFound)/float64(a.strings))
}

type e4Result struct{ question, title tally }

// e4Measure is gate E4's scoring (PLAN § PR 3): it ingests each of the five proof-run matters into
// s and, for each of the 68 planted facts, searches within that matter with the fact's answer-free
// question (the gate) and its title (an upper bound: titles state the answers). A fact counts when
// a chunk holding any of its evidence strings is in the top 8; each evidence string is also
// counted on its own. Per-matter recall and the misses are logged.
func e4Measure(t *testing.T, s *Service) e4Result {
	t.Helper()
	keys, _ := filepath.Glob("../../eval/tasks/*/*/answer_key.json")
	if len(keys) != 5 {
		t.Fatalf("%d proof-run matters, want 5", len(keys))
	}
	ws := regexp.MustCompile(`\s+`)
	norm := func(x string) string { return strings.ToLower(ws.ReplaceAllString(strings.TrimSpace(x), " ")) }
	var r e4Result
	for _, key := range keys {
		var ak struct {
			Facts []struct {
				ID, Title, Question string
				Evidence            []string
			}
		}
		raw, err := os.ReadFile(key)
		if err != nil || json.Unmarshal(raw, &ak) != nil {
			t.Fatalf("%s: %v", key, err)
		}
		docsDir := filepath.Join(filepath.Dir(key), "documents")
		files, _ := os.ReadDir(docsDir)
		var scope []string
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(docsDir, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			scope = append(scope, ingestText(t, s, f.Name(), string(b)).ID)
		}
		var q, ti tally
		for _, f := range ak.Facts {
			if f.Question == "" {
				t.Fatalf("%s: fact %s has no answer-free question", key, f.ID)
			}
			for _, run := range []struct {
				query string
				into  *tally
				miss  bool
			}{{f.Question, &q, true}, {f.Title, &ti, false}} {
				hits, err := s.Search(t.Context(), scope, run.query, 8, 0)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, ev := range f.Evidence {
					run.into.strings++
					if slices.ContainsFunc(hits, func(h Hit) bool { return strings.Contains(norm(h.Text), norm(ev)) }) {
						run.into.stringsFound++
						found = true
					}
				}
				if run.into.facts++; found {
					run.into.found++
				} else if run.miss {
					t.Logf("  miss %s: %s", f.ID, f.Question)
				}
			}
		}
		t.Logf("%s: recall@8 %d/%d by question, %d/%d by title", filepath.Base(filepath.Dir(key)), q.found, q.facts, ti.found, ti.facts)
		r.question.add(q)
		r.title.add(ti)
	}
	t.Logf("E4 recall@8 by answer-free question (the gate): %s", r.question)
	t.Logf("E4 recall@8 by fact title (upper bound: titles state the answer): %s", r.title)
	if r.question.facts != 68 {
		t.Errorf("%d planted facts, want 68", r.question.facts)
	}
	return r
}

// The E4 tape, eval/e4/embeddings.json: every text the measurement embeds (the proof-run chunks as
// the chunker cuts them, and the questions and titles), keyed by sha256 of the exact text, with the
// model that embedded it. CI replays it through model.Fake; the owner records it once.
const tapePath = "../../eval/e4/embeddings.json"

// selfTestModel names the deterministic hash embedder below. A tape it recorded proves only that
// the record/replay harness works: it is never a gate result.
const selfTestModel = "biglaw-selftest-hash"

type e4Tape struct {
	Model      string            `json:"model"`
	Dimensions int               `json:"dimensions"` // what the recording asked for (0: the model's own)
	Chunker    string            `json:"chunker"`
	Note       string            `json:"note,omitempty"`
	Vectors    map[string]string `json:"vectors"` // sha256(text) hex -> base64 of little-endian float32s
}

const chunkerID = "docs.Chunkify, ChunkTokens=400 (PR 3a)"

func textKey(text string) string { s := sha256.Sum256([]byte(text)); return hex.EncodeToString(s[:]) }

func encodeVector(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeVector(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) == 0 || len(b)%4 != 0 {
		return nil, fmt.Errorf("not base64 float32s")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// hashVector is the self-test embedder: signed feature hashing of lower-cased words into dims
// buckets, L2-normalised. Deterministic and offline; a bag of words, not a language model.
func hashVector(text string, dims int) []float32 {
	v := make([]float32, dims)
	v[0] = 1e-3 // never the zero vector
	for _, w := range regexp.MustCompile(`[\p{L}\p{N}]+`).FindAllString(strings.ToLower(text), -1) {
		h := fnv.New32a()
		h.Write([]byte(w))
		x := h.Sum32()
		v[x%uint32(dims)] += float32(1 - 2*int((x>>31)&1))
	}
	var n float64
	for _, f := range v {
		n += float64(f) * float64(f)
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / math.Sqrt(n))
	}
	return v
}

// recorder asks inner for vectors (with dims, if set) and keeps every text it embedded.
type recorder struct {
	inner model.Embedder
	dims  int
	mu    sync.Mutex
	tape  map[string]string
}

func (r *recorder) Embed(ctx context.Context, req model.EmbedRequest) (*model.Embeddings, error) {
	req.Dimensions = r.dims
	e, err := r.inner.Embed(ctx, req)
	if err == nil {
		r.mu.Lock()
		for i, text := range req.Inputs {
			r.tape[textKey(text)] = encodeVector(e.Vectors[i])
		}
		r.mu.Unlock()
	}
	return e, err
}

// replay is a model.Fake answering from a tape; texts it lacks are collected in missing.
func replay(tape e4Tape) (*model.Fake, func() []string) {
	var mu sync.Mutex
	var missing []string
	f := model.NewFake(nil)
	f.Vectors = func(text string) ([]float32, error) {
		if enc, ok := tape.Vectors[textKey(text)]; ok {
			return decodeVector(enc)
		}
		mu.Lock()
		missing = append(missing, text)
		mu.Unlock()
		return nil, fmt.Errorf("not on the E4 tape: %.60q", text)
	}
	return f, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(missing) }
}

func loadTape(t *testing.T) e4Tape {
	t.Helper()
	var tape e4Tape
	raw, err := os.ReadFile(tapePath)
	if err != nil || json.Unmarshal(raw, &tape) != nil || tape.Model == "" || len(tape.Vectors) == 0 {
		t.Fatalf("%s is missing or not an E4 tape (%v): record it with make e4-record", tapePath, err)
	}
	return tape
}

// checkReplay fails the test if the measurement embedded a text the tape lacks (the chunker or a
// fixture changed since recording: re-record) or left a document lexical only.
func checkReplay(t *testing.T, s *Service, missing func() []string) {
	t.Helper()
	if m := missing(); len(m) > 0 {
		t.Errorf("%d texts are not on %s (the chunker or the fixtures changed): re-record with make e4-record; first: %.80q", len(m), tapePath, m[0])
	}
	var lexical int
	if err := s.db.QueryRow(`SELECT count(*) FROM documents WHERE embed_model IS DISTINCT FROM $1`, s.embedModel).Scan(&lexical); err != nil || lexical != 0 {
		t.Errorf("%d documents were not embedded (%v)", lexical, err)
	}
}

// TestE4TapeReplays is the harness self-test: the committed tape holds every text the E4
// measurement embeds, replays through model.Fake into hybrid search with no document left lexical,
// and its vectors survive the base64 round trip. With the self-test tape its vectors must equal
// the hash embedder's, which proves the recorder wrote what it was given.
func TestE4TapeReplays(t *testing.T) {
	t.Parallel()
	tape := loadTape(t)
	if tape.Model == selfTestModel {
		for k, enc := range tape.Vectors {
			if v, err := decodeVector(enc); err != nil || len(v) != tape.Dimensions {
				t.Fatalf("vector %s: %v, %d dimensions, want %d", k, err, len(v), tape.Dimensions)
			}
		}
	}
	f, missing := replay(tape)
	s := newService(t)
	s.UseEmbedder(f, tape.Model)
	r := e4Measure(t, s)
	checkReplay(t, s, missing)
	t.Logf("harness self-test with %s (%d vectors): %s; not a gate result unless the tape is a real model's", tape.Model, len(tape.Vectors), r.question)
	if tape.Model == selfTestModel {
		n := 0
		for _, c := range f.EmbedCalls() {
			for _, text := range c.Inputs {
				if want := encodeVector(hashVector(text, tape.Dimensions)); tape.Vectors[textKey(text)] != want {
					t.Fatalf("the tape's vector for %.60q is not the hash embedder's", text)
				}
				n++
			}
		}
		if n == 0 {
			t.Fatal("no text was embedded")
		}
	}
}

// TestHybridRecallProofRuns is gate E4 (PLAN § PR 3b): answer-free recall@8 of hybrid search must
// be ≥ 0.95 (65/68). By default it replays eval/e4/embeddings.json; E4_RECORD=1 records that tape
// with EMBED_MODEL (and EMBED_DIMENSIONS) at BIGLAW_MODEL_URL/BIGLAW_MODEL_KEY (make e4-record), and
// E4_LIVE=1 measures live without writing it (make e4-live). A tape recorded by the self-test
// embedder is no gate: the test then skips, saying so.
func TestHybridRecallProofRuns(t *testing.T) {
	t.Parallel()
	s := newService(t)
	record, live := os.Getenv("E4_RECORD") == "1", os.Getenv("E4_LIVE") == "1"
	var rec *recorder
	var missing func() []string
	modelID := ""
	switch {
	case record || live:
		modelID = os.Getenv("EMBED_MODEL")
		dims, err := strconv.Atoi(cmp.Or(os.Getenv("EMBED_DIMENSIONS"), "0"))
		if modelID == "" || err != nil || dims < 0 {
			t.Fatal("E4_RECORD/E4_LIVE need EMBED_MODEL (and EMBED_DIMENSIONS, a number, or unset)")
		}
		var inner model.Embedder
		if modelID == selfTestModel {
			dims = cmp.Or(dims, 64)
			f := model.NewFake(nil)
			f.Vectors = func(text string) ([]float32, error) { return hashVector(text, dims), nil }
			inner = f
		} else {
			c, err := model.New(model.Options{URL: os.Getenv("BIGLAW_MODEL_URL"), Key: os.Getenv("BIGLAW_MODEL_KEY"), Roles: model.Roles{Embed: modelID}})
			if err != nil {
				t.Fatal(err)
			}
			inner = c
		}
		rec = &recorder{inner: inner, dims: dims, tape: map[string]string{}}
		s.UseEmbedder(rec, modelID)
	default:
		tape := loadTape(t)
		var f *model.Fake
		f, missing = replay(tape)
		modelID = tape.Model
		s.UseEmbedder(f, tape.Model)
	}
	r := e4Measure(t, s)
	if missing != nil {
		checkReplay(t, s, missing)
	}
	if rec != nil && record {
		tape := e4Tape{Model: modelID, Dimensions: rec.dims, Chunker: chunkerID, Vectors: rec.tape}
		if modelID == selfTestModel {
			tape.Note = "Self-test tape: a bag-of-words hash embedder, not a language model. It proves the E4 record/replay harness only; " +
				"TestHybridRecallProofRuns skips on it. The owner replaces it with: make e4-record EMBED_MODEL=(a real embedding model) EMBED_DIMENSIONS=512."
		}
		out, _ := json.MarshalIndent(tape, "", " ")
		if err := os.WriteFile(tapePath, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d vectors of %s to %s", len(rec.tape), modelID, tapePath)
	}
	if modelID == selfTestModel {
		t.Skipf("E4 GATE NOT JUDGED: these are the self-test hash embedder's vectors (%s), not a real model's; their recall, %s, is no gate result. "+
			"Record the real tape once: make e4-record EMBED_MODEL=text-embedding-3-small EMBED_DIMENSIONS=512 BIGLAW_MODEL_URL=… BIGLAW_MODEL_KEY=… BIGLAW_TEST_DSN=…",
			selfTestModel, r.question)
	}
	if float64(r.question.found) < 0.95*float64(r.question.facts) {
		t.Errorf("E4 GATE FAILED with %s: recall@8 by answer-free question %s < 0.95 (65/68)", modelID, r.question)
	} else {
		t.Logf("E4 gate met with %s: %s", modelID, r.question)
	}
}
