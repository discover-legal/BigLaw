// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package eval

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// uuidRe matches the ids a run makes (documents, tasks): they differ per run, so a golden holds a
// placeholder numbered by first appearance.
var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TestProofRunsGolden pins each proof-run matter's delivered memo byte for byte (ids aside), under
// the oracle and the fabricator: the Limitations lines in their order and wording, the Computed
// figures block, the Evidence Note and the gates a lawyer is asked to decide. A refactor that is
// meant to change no behaviour must leave every file in testdata/golden as it is. GOLDEN_UPDATE=1
// rewrites them (review the diff: it is a change of what a user reads).
func TestProofRunsGolden(t *testing.T) {
	for _, mt := range loadMatters(t) {
		for _, run := range []struct {
			name    string
			scripts func(matter, map[string]string) map[string][]model.Reply
		}{{"oracle", oracle}, {"fabricator", fabricator}} {
			t.Run(mt.Slug+"/"+run.name, func(t *testing.T) {
				t.Parallel()
				v, _, gates := drive(t, mt, run.scripts)
				got := "status: " + v.Status + "\ngates: " + strings.Join(gates, " ") + "\n\n"
				if v.Output != nil {
					got += *v.Output
				}
				n := map[string]string{}
				got = uuidRe.ReplaceAllStringFunc(got, func(id string) string {
					if _, ok := n[id]; !ok {
						n[id] = "<id" + strconv.Itoa(len(n)+1) + ">"
					}
					return n[id]
				})
				path := filepath.Join("testdata", "golden", mt.Slug+"."+run.name+".md")
				if os.Getenv("GOLDEN_UPDATE") != "" {
					os.MkdirAll(filepath.Dir(path), 0o755)
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (GOLDEN_UPDATE=1 writes it)", err)
				}
				if got != string(want) {
					t.Errorf("%s changed (GOLDEN_UPDATE=1 rewrites it; review the diff):\n%s", path, firstDiff(string(want), got))
				}
			})
		}
	}
}

// firstDiff shows the first line where a and b differ, with its neighbours.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < max(len(al), len(bl)); i++ {
		if i >= len(al) || i >= len(bl) || al[i] != bl[i] {
			at := func(l []string) string {
				if i < len(l) {
					return l[i]
				}
				return "<end>"
			}
			return "line " + strconv.Itoa(i+1) + ":\n want " + at(al) + "\n  got " + at(bl)
		}
	}
	return ""
}
