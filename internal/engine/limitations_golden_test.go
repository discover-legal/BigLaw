// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// TestCandidateLinesGolden pins the candidate pack and skill Limitations lines' wording, and their
// place in a delivered memo's Limitations (after the run's own lines, packs before skills, before
// the citator line). GOLDEN_UPDATE=1 rewrites testdata/limitations.golden.
func TestCandidateLinesGolden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ got, want string }{
		{candidates([]Pack{{Code: "us"}, {Code: "us-ny", Status: "reviewed"}, {Code: "litigation", Status: "candidate"}}),
			"This deliverable used candidate practice or jurisdiction packs (us, litigation) that no named lawyer has reviewed yet."},
		{candidates([]Pack{{Code: "us", Status: "reviewed"}}), ""},
		{candidates(nil), ""},
		{skillCandidates([]SkillUse{{Name: "a"}, {Name: "b", Status: "reviewed"}, {Name: "c", Status: "candidate"}}),
			"This deliverable used candidate skills (a, c) that no named lawyer has reviewed yet."},
		{skillCandidates([]SkillUse{{Name: "a", Status: "reviewed"}}), ""},
		{skillCandidates(nil), ""},
	} {
		if c.got != c.want {
			t.Errorf("got %q\nwant %q", c.got, c.want)
		}
	}

	f := newFixture(t)
	sc := f.happy()
	sc["lead"] = append([]model.Reply{calls(tc("use_skill", m{"name": "tskill", "file": nil, "offset": nil}))}, sc["lead"]...)
	e, _ := f.engine(sc, Config{})
	save(t, e, "skill.tskill", 0, tskill(""))
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	out := deref(v.Output)
	i := strings.Index(out, "\n## Limitations\n")
	if i < 0 {
		t.Fatalf("%s: no Limitations\n%s", stop(v), out)
	}
	lim := out[i:]
	if j := strings.Index(lim[1:], "\n## "); j >= 0 {
		lim = lim[:j+1]
	}
	ids := map[string]string{}
	got := "stop: " + stop(v) + "\n" + goldenID.ReplaceAllStringFunc(lim, func(id string) string {
		if _, ok := ids[id]; !ok {
			ids[id] = "<id" + strconv.Itoa(len(ids)+1) + ">"
		}
		return ids[id]
	})
	path := filepath.Join("testdata", "limitations.golden")
	if os.Getenv("GOLDEN_UPDATE") != "" {
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
		t.Errorf("%s changed (GOLDEN_UPDATE=1 rewrites it; review the diff):\nwant %s\n got %s", path, want, got)
	}
}
