// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// edited is item id's built-in files with file's text passed through fn: a save's body.
func edited(id, file string, fn func(string) string) map[string]string {
	files := fileMap(builtin, id)
	files[file] = fn(files[file])
	return files
}

// specEdit is a JSON spec file of item id changed by fn on its decoded object.
func specEdit(t *testing.T, id, file string, fn func(m map[string]any)) map[string]string {
	t.Helper()
	return edited(id, file, func(s string) string {
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		fn(v)
		return pretty(v)
	})
}

// path walks a decoded schema's properties: at("out", "findings", "[]", "claim").
func at(v map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		switch k {
		case "[]":
			v = v["items"].(map[string]any)
		case "in", "out":
			v = v[k].(map[string]any)
		default:
			v = v["properties"].(map[string]any)[k].(map[string]any)
		}
	}
	return v
}

func problems(err error) string {
	var inv *InvalidError
	if !errors.As(err, &inv) {
		return "not invalid: " + errString(err)
	}
	var b []string
	for _, p := range inv.Problems {
		b = append(b, p.File+": "+p.Message)
	}
	return strings.Join(b, "\n")
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

const (
	leadMD   = engineDir + "specs/lead.md"
	usPack   = engineDir + "packs/jurisdiction/us.md"
	tskillMD = skillsDir + "tskill/SKILL.md" // a skill the tests write: an export may delete any starter
)

// skillMD is a skill's SKILL.md (tests' own, so no test depends on a starter that ships).
func skillMD(name, status, reviewer, body string, tools ...string) string {
	t := ""
	for _, x := range tools {
		t += "\n- " + x
	}
	return "---\nname: " + name + "\ndescription: A skill the tests write.\nstatus: " + status + "\nreviewedBy:" + strings.TrimRight(" "+reviewer, " ") + "\ntools:" + t + "\n---\n" + body + "\n"
}

// tskill is the tests' skill's files: its SKILL.md (tools numbers, draft_section) and a reference file.
func tskill(md string) map[string]string {
	if md == "" {
		md = skillMD("tskill", "candidate", "", "Compute every deadline with numbers.", "numbers", "draft_section")
	}
	return map[string]string{tskillMD: md, skillsDir + "tskill/examples.json": `{"op": "calc"}`}
}

// aSkill is the name of a skill that ships (whichever does); a test about built-in skills skips without one.
func aSkill(t *testing.T) string {
	t.Helper()
	if len(builtinSpecs.skills) == 0 {
		t.Skip("no built-in skill ships in this build")
	}
	return builtinSpecs.skills[0].Name
}

// TestManageValidationRefuses: every save is checked strictly before it is stored: well-formed
// JSON and front matter, the schema subset strict-shaped, real allowed tools, turn caps and classes
// in range, and the schemas still compatible with what Go reads and writes (each refusal names the
// field Go needs); an edit that keeps all of that is accepted.
func TestManageValidationRefuses(t *testing.T) {
	t.Parallel()
	e := &Engine{docs: &docs.Service{}}
	const ef, rf, vq, nb = agentsDir + "extract_facts.json", agentsDir + "reproduce.json", "internal/docs/specs/verify_quote.json", "internal/analysis/specs/numbers.json"
	cases := []struct {
		name, id string
		files    map[string]string
		want     string
	}{
		{"malformed JSON", "agent.reproduce", edited("agent.reproduce", rf, func(s string) string { return s[:len(s)-3] }), "not one well-formed spec object"},
		{"unknown field", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["temperature"] = 1 }), "unknown field"},
		{"renamed", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["name"] = "reproducer" }), `name: must stay "reproduce"`},
		{"kind", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["kind"] = "go" }), `kind: must stay "subagent"`},
		{"turns zero", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["maxTurns"] = 0 }), "maxTurns: a subagent's is 1 to 32"},
		{"turns high", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["maxTurns"] = 33 }), "maxTurns: a subagent's is 1 to 32"},
		{"class", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["class"] = "premium" }), "class is plan, evidence"},
		{"role", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["role"] = "lead" }), "specialist or critic role"},
		{"unknown tool", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["allowed"] = []string{"retrieve", "web_search"} }), `allowed tool "web_search" is not a registered Go tool`},
		{"subagent as tool", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["allowed"] = []string{"draft_section"} }), `allowed tool "draft_section" is not a registered Go tool`},
		{"schema keyword", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { at(m, "in", "questions")["pattern"] = "x" }), "in: not a schema in BigLaw's subset"},
		{"open object", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { delete(at(m, "out"), "additionalProperties") }), "out: not strict-shaped"},
		{"description rules", "agent.reproduce", specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["description"] = "Answers questions." }), "at least three sentences"},
		{"Go reads a removed field", "agent.extract_facts", specEdit(t, "agent.extract_facts", ef, func(m map[string]any) {
			o := at(m, "out", "findings", "[]")
			delete(o["properties"].(map[string]any), "claim")
			o["required"] = slices.DeleteFunc(o["required"].([]any), func(x any) bool { return x == "claim" })
		}), "out.findings[].claim: Go reads or writes this field; it cannot be removed or renamed"},
		{"Go reads a retyped field", "agent.extract_facts", specEdit(t, "agent.extract_facts", ef, func(m map[string]any) {
			at(m, "out", "findings", "[]", "claim")["type"] = "integer"
		}), "out.findings[].claim: Go reads or writes this field as string; it cannot be retyped"},
		{"Go writes no new field", "tool.verify_quote", specEdit(t, "tool.verify_quote", vq, func(m map[string]any) {
			in := at(m, "in")
			in["properties"].(map[string]any)["page"] = map[string]any{"type": "integer"}
			in["required"] = append(in["required"].([]any), "page")
		}), "in.page: Go writes this object and has no value for a new field"},
		{"a Go tool's output", "tool.numbers", specEdit(t, "tool.numbers", nb, func(m map[string]any) {
			out := at(m, "out")
			out["properties"].(map[string]any)["note"] = map[string]any{"type": "string"}
			out["required"] = append(out["required"].([]any), "note")
		}), "out.note: Go writes this object"},
		{"built-in example", "tool.numbers", specEdit(t, "tool.numbers", nb, func(m map[string]any) { at(m, "in", "op")["enum"] = []string{"calc", "table"} }), "the built-in example"},
		{"H1: a verdict Go branches on renamed", "agent.prosecute", specEdit(t, "agent.prosecute", agentsDir+"prosecute.json", func(m map[string]any) {
			at(m, "out", "verdicts", "[]", "verdict")["enum"] = []string{"supported", "overstated", "not_supported"}
		}), `out.verdicts[].verdict: Go branches on this field's values ("overstated", "supported", "unsupported"); its enum cannot change`},
		{"H1: a severity recased", "agent.audit_gaps", specEdit(t, "agent.audit_gaps", agentsDir+"audit_gaps.json", func(m map[string]any) {
			at(m, "out", "gaps", "[]", "severity")["enum"] = []string{"High", "Medium", "Low"}
		}), "out.gaps[].severity: Go branches on this field's values"},
		{"H1: an enum value added", "tool.numbers", specEdit(t, "tool.numbers", nb, func(m map[string]any) {
			at(m, "in", "op")["enum"] = []string{"calc", "handles", "table", "guess"}
		}), "in.op: Go branches on this field's values"},
		{"M1: a subagent given finish", "agent.analyze_issue", specEdit(t, "agent.analyze_issue", agentsDir+"analyze_issue.json", func(m map[string]any) {
			m["allowed"] = append(m["allowed"].([]any), "finish")
		}), `allowed: "finish" is the lead's own`},
		{"L4: a subagent moved to class free", "agent.extract_facts", specEdit(t, "agent.extract_facts", ef, func(m map[string]any) { m["class"] = "free" }),
			"class: free is the figure reader's and judge's only"},
		{"empty prompt", "prompt.lead", edited("prompt.lead", leadMD, func(string) string { return "  \n" }), "a prompt is 1 byte"},
		{"extra file", "prompt.lead", func() map[string]string {
			f := fileMap(builtin, "prompt.lead")
			f[engineDir+"specs/lead2.md"] = "x"
			return f
		}(), "the files must be exactly the item's own"},
		{"pack front matter", "pack.jurisdiction.us", edited("pack.jurisdiction.us", usPack, func(s string) string { return strings.Replace(s, "---\n", "", 1) }), "must open and close"},
		{"pack code", "pack.jurisdiction.us", edited("pack.jurisdiction.us", usPack, func(s string) string { return strings.Replace(s, "code: us", "code: uk", 1) }), `code: must stay "us"`},
		{"pack reviewed by nobody", "pack.jurisdiction.us", edited("pack.jurisdiction.us", usPack, func(s string) string { return strings.Replace(s, "status: candidate", "status: reviewed", 1) }), "status reviewed needs reviewedBy"},
		{"pack key", "pack.jurisdiction.us", edited("pack.jurisdiction.us", usPack, func(s string) string { return strings.Replace(s, "code: us", "code: us\nowner: me", 1) }), `unknown key "owner"`},
		{"skill disallowed tool", "skill.tskill", tskill(skillMD("tskill", "candidate", "", "Body.", "read_figures")), `tools: "read_figures" is not a tool use_skill's callers may call`},
		{"skill unknown tool", "skill.tskill", tskill(skillMD("tskill", "candidate", "", "Body.", "web_search")), `"web_search" is not a tool`},
		{"skill name", "skill.tskill", tskill(skillMD("other", "candidate", "", "Body.")), `name: must be "tskill"`},
		{"skill description", "skill.tskill", tskill(strings.Replace(skillMD("tskill", "candidate", "", "Body."), "description: A skill", "description: "+strings.Repeat("x", 200), 1)), "description: one line of 1 to 200 bytes"},
		{"skill file name", "skill.tskill", func() map[string]string {
			f := tskill("")
			f[skillsDir+"tskill/run.sh"] = "rm -rf /"
			return f
		}(), "a reference file is named"},
		{"skill JSON file", "skill.tskill", func() map[string]string {
			f := tskill("")
			f[skillsDir+"tskill/examples.json"] = "{"
			return f
		}(), "not well-formed JSON"},
		{"skill without SKILL.md", "skill.notes", map[string]string{skillsDir + "notes/a.md": "x"}, "a skill needs its SKILL.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := e.validate(Snapshot{c.id: {Version: 1, Files: c.files}}, c.id)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(problems(err), c.want) {
				t.Fatalf("want a refusal saying %q, got:\n%s", c.want, problems(err))
			}
		})
	}
	ok := map[string]map[string]string{
		"agent.reproduce": specEdit(t, "agent.reproduce", rf, func(m map[string]any) {
			m["maxTurns"], m["allowed"] = 6, []string{"retrieve"}
			at(m, "in", "questions")["maxItems"] = 20                                           // narrowing a bound the model writes
			at(m, "out", "answers", "[]", "question")["description"] = "the question, verbatim" // a description in the schema
		}),
		"agent.extract_facts": specEdit(t, "agent.extract_facts", ef, func(m map[string]any) { // ledger refuses its writes to a subagent itself
			m["allowed"] = append(m["allowed"].([]any), "ledger")
		}),
		"agent.read_figures":   specEdit(t, "agent.read_figures", agentsDir+"read_figures.json", func(m map[string]any) { m["maxTurns"] = 3 }), // free as built
		"prompt.lead":          edited("prompt.lead", leadMD, func(s string) string { return s + "\nBe brief." }),
		"pack.jurisdiction.us": edited("pack.jurisdiction.us", usPack, func(s string) string { return s + "\nOne more line." }),
		"skill.tskill":         tskill(skillMD("tskill", "candidate", "", "Body.", "numbers", "ledger")),
	}
	for id, files := range ok {
		if err := e.validate(Snapshot{id: {Version: 1, Files: files}}, id); err != nil {
			t.Errorf("%s: a valid edit was refused:\n%s", id, problems(err))
		}
	}
}

// TestPackReviewRule: an edit of a pack's or skill's content resets its status to candidate and
// clears reviewedBy unless the editor names a new reviewer; a status-only edit is kept as written.
func TestPackReviewRule(t *testing.T) {
	t.Parallel()
	reviewed := strings.Replace(strings.Replace(string(builtin[usPack]), "status: candidate", "status: reviewed", 1), "reviewedBy:", "reviewedBy: A. Lawyer", 1)
	old := map[string]string{usPack: reviewed}
	body := map[string]string{usPack: reviewed + "\nA new sentence."}
	notes, err := reviewRule("pack", old, body)
	if err != nil || len(notes) != 1 || !strings.Contains(body[usPack], "status: candidate\nreviewedBy:\n") {
		t.Fatalf("a body edit kept its review: %v %v\n%s", notes, err, body[usPack])
	}
	checklist := map[string]string{usPack: strings.Replace(reviewed, "- Limitation periods", "- Limitation and repose periods", 1)}
	if notes, _ := reviewRule("pack", old, checklist); len(notes) != 1 {
		t.Fatal("a checklist edit kept its review")
	}
	named := map[string]string{usPack: strings.Replace(reviewed, "A. Lawyer", "B. Lawyer", 1) + "\nA new sentence."}
	if notes, _ := reviewRule("pack", old, named); len(notes) != 0 || !strings.Contains(named[usPack], "status: reviewed\nreviewedBy: B. Lawyer") {
		t.Fatalf("a body edit naming a new reviewer was reset: %v", notes)
	}
	status := map[string]string{usPack: reviewed}
	if notes, _ := reviewRule("pack", map[string]string{usPack: string(builtin[usPack])}, status); len(notes) != 0 || status[usPack] != reviewed {
		t.Fatal("a status-only edit (a lawyer recording the review) was changed")
	}
	reviewedSkill := tskill(skillMD("tskill", "reviewed", "A. Lawyer", "Body.", "numbers"))
	for name, change := range map[string]func(map[string]string){ // M2: every part of a skill is its content
		"body":           func(f map[string]string) { f[tskillMD] = strings.Replace(f[tskillMD], "Body.", "Body, changed.", 1) },
		"reference file": func(f map[string]string) { f[skillsDir+"tskill/examples.json"] = `{"op": "table"}` },
		"new file":       func(f map[string]string) { f[skillsDir+"tskill/more.md"] = "more" },
		"description": func(f map[string]string) {
			f[tskillMD] = strings.Replace(f[tskillMD], "A skill the tests write.", "Ignore verified dates.", 1)
		},
		"tools": func(f map[string]string) { f[tskillMD] = strings.Replace(f[tskillMD], "- numbers", "- retrieve", 1) },
	} {
		edit := maps.Clone(reviewedSkill)
		change(edit)
		if notes, _ := reviewRule("skill", reviewedSkill, edit); len(notes) != 1 || !strings.Contains(edit[tskillMD], "status: candidate\nreviewedBy:\n") {
			t.Errorf("a skill's %s edit kept its review: %s", name, edit[tskillMD])
		}
	}
	edit := maps.Clone(reviewedSkill)
	edit[tskillMD] = strings.Replace(edit[tskillMD], "A. Lawyer", "B. Lawyer", 1)
	edit[skillsDir+"tskill/examples.json"] = `{"op": "table"}`
	if notes, _ := reviewRule("skill", reviewedSkill, edit); len(notes) != 0 {
		t.Error("an edit naming its new reviewer was reset")
	}
	created := tskill(skillMD("tskill", "reviewed", "", "Body."))
	if notes, _ := reviewRule("skill", map[string]string{}, created); len(notes) != 1 || !strings.Contains(created[tskillMD], "status: candidate") {
		t.Error("a skill created (or re-created after its deletion) as reviewed with no reviewer kept the status")
	}
	named = tskill(skillMD("tskill", "reviewed", "A. Lawyer", "Body."))
	if notes, _ := reviewRule("skill", map[string]string{}, named); len(notes) != 0 {
		t.Error("a skill created naming its reviewer was reset")
	}
}

// TestManageStoreRoundTrip: a save is a version (who, when, note, all files), shown in the
// catalogue, the history and the change log; a stale base is a conflict; a diff between any two
// versions; a revert to an earlier version or to the built-in is a version too.
func TestManageStoreRoundTrip(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	one := edited("prompt.lead", leadMD, func(s string) string { return s + "\nVersion one." })
	v1, notes, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 0, Files: one, Note: "tighter"}, "Ada")
	if err != nil || v1.Version != 1 || v1.Action != "save" || v1.Author != "Ada" || len(notes) != 0 {
		t.Fatalf("save: %+v %v %v", v1, notes, err)
	}
	if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 0, Files: one}, "Bo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale base saved: %v", err)
	}
	if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 1, Files: one}, "Bo"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("an unchanged save: %v", err)
	}
	two := edited("prompt.lead", leadMD, func(s string) string { return s + "\nVersion two." })
	if v, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 1, Files: two}, "Bo"); err != nil || v.Version != 2 || v.From != 1 {
		t.Fatalf("second save: %+v %v", v, err)
	}
	d, err := e.ItemDetail(ctx, "prompt.lead")
	if err != nil || d.Version != 2 || d.Latest != 2 || d.Content[leadMD] != two[leadMD] || d.Original[leadMD] != string(builtin[leadMD]) {
		t.Fatalf("detail: v%d latest %d %v", d.Version, d.Latest, err)
	}
	h, err := e.History(ctx, "prompt.lead")
	if err != nil || len(h) != 2 || h[0].Version != 2 || h[1].Files[leadMD] != one[leadMD] || h[1].Note != "tighter" {
		t.Fatalf("history: %+v %v", h, err)
	}
	ds, err := e.Diff(ctx, "prompt.lead", 1, 2)
	if err != nil || len(ds) != 1 || !strings.Contains(ds[0].Patch, "-Version one.\n\\ No newline at end of file\n+Version two.") {
		t.Fatalf("diff 1→2: %+v %v", ds, err)
	}
	if ds, _ := e.Diff(ctx, "prompt.lead", 0, -1); len(ds) != 1 || !strings.Contains(ds[0].Patch, "+Version two.") {
		t.Fatalf("diff built-in→current: %+v", ds)
	}
	if v, err := e.Revert(ctx, "prompt.lead", 1, 2, "back", "Ada"); err != nil || v.Version != 3 || v.Restored == nil || *v.Restored != 1 {
		t.Fatalf("revert to 1: %+v %v", v, err)
	}
	if d, _ := e.ItemDetail(ctx, "prompt.lead"); d.Content[leadMD] != one[leadMD] || d.Version != 3 {
		t.Fatal("the revert to version 1 is not what new tasks run on")
	}
	if v, err := e.Revert(ctx, "prompt.lead", 0, 3, "", "Ada"); err != nil || !v.Builtin {
		t.Fatalf("revert to the built-in: %+v %v", v, err)
	}
	if d, _ := e.ItemDetail(ctx, "prompt.lead"); d.Version != 0 || d.Latest != 4 || d.Content[leadMD] != string(builtin[leadMD]) {
		t.Fatalf("after the revert to the built-in: v%d latest %d", d.Version, d.Latest)
	}
	snap, _, _ := e.current(ctx, e.db)
	if len(snap) != 0 {
		t.Fatalf("the snapshot still holds %v", slices.Collect(maps.Keys(snap)))
	}
	log, err := e.Changes(ctx, 10)
	if err != nil || len(log) != 4 || log[0].Action != "revert" || log[0].From != 3 || log[0].Files != nil {
		t.Fatalf("change log: %+v %v", log, err)
	}
	if _, _, err := e.Save(ctx, "hook.draft_section", SaveIn{Files: map[string]string{}}, "Ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a Go check was saved as an item: %v", err)
	}
	if _, _, err := e.Save(ctx, "prompt.nope", SaveIn{Files: map[string]string{}}, "Ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown item: %v", err)
	}
	if _, _, err := e.Save(ctx, "tool.nope", SaveIn{Files: map[string]string{}}, "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "needs its files") {
		t.Fatalf("a new tool with no files: %v", err)
	}
	if _, err := e.Delete(ctx, "prompt.lead", 4, "", "Ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a prompt was deleted: %v", err)
	}
}

// TestSkillCreateDeleteRevert: an admin creates a skill (listed in the catalogue and the brief),
// deletes it (a tombstone, listed as deleted) and restores it by revert.
func TestSkillCreateDeleteRevert(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	md := "---\nname: chronology\ndescription: Build a dated chronology from verified findings.\nstatus: candidate\nreviewedBy:\ntools:\n- ledger\n---\nList each event with its date and finding id.\n"
	files := map[string]string{skillsDir + "chronology/SKILL.md": md, skillsDir + "chronology/columns.csv": "date,event,fid\n"}
	if _, _, err := e.Save(ctx, "skill.Bad_Name", SaveIn{Files: files}, "Ada"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad skill name: %v", err)
	}
	v, _, err := e.Save(ctx, "skill.chronology", SaveIn{Files: files}, "Ada")
	if err != nil || v.Action != "create" {
		t.Fatalf("create: %+v %v", v, err)
	}
	items, _ := e.Items(ctx)
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "skill.chronology" })
	if i < 0 || items[i].Builtin || items[i].Deleted || !slices.Equal(items[i].Uses, []string{"ledger"}) || !slices.Contains(items[i].UsedBy, "lead") {
		t.Fatalf("catalogue: %+v", items)
	}
	snap, _, _ := e.current(ctx, e.db)
	sp, _ := newSpecs(snap)
	brief, err := e.brief(withSpecs(ctx, sp), Task{DocumentIDs: f.ids()})
	if err != nil || !strings.Contains(string(brief), "chronology (candidate): Build a dated chronology") || strings.Contains(string(brief), "List each event") {
		t.Fatalf("the brief lists names and descriptions only: %s %v", brief, err)
	}
	if v, err := e.Delete(ctx, "skill.chronology", 1, "not needed", "Ada"); err != nil || !v.Deleted {
		t.Fatalf("delete: %+v %v", v, err)
	}
	items, _ = e.Items(ctx)
	if i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "skill.chronology" }); i < 0 || !items[i].Deleted {
		t.Fatal("a deleted skill is not listed as deleted")
	}
	if _, err := e.Delete(ctx, "skill.chronology", 2, "", "Ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	if _, err := e.Revert(ctx, "skill.chronology", 1, 2, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if d, err := e.ItemDetail(ctx, "skill.chronology"); err != nil || d.Deleted || d.Content[skillsDir+"chronology/columns.csv"] != "date,event,fid\n" {
		t.Fatalf("restored: %+v %v", d, err)
	}
	if v, err := e.Revert(ctx, "skill.chronology", 0, 3, "", "Ada"); err != nil || !v.Deleted {
		t.Fatalf("a created skill's revert to the built-in is its absence: %+v %v", v, err)
	}
	starter := aSkill(t)
	if v, err := e.Delete(ctx, "skill."+starter, 0, "", "Ada"); err != nil || !v.Deleted { // a built-in skill
		t.Fatalf("delete a built-in skill: %+v %v", v, err)
	}
	snap, _, _ = e.current(ctx, e.db)
	sp, _ = newSpecs(snap)
	if _, ok := sp.skill(starter); ok || len(itemFiles(sp.files, "skill."+starter)) != 0 {
		t.Fatal("a deleted built-in skill is still in the set")
	}
}

// TestSkillCatalogueBounds: the brief lists at most maxSkills skills in at most maxSkillIndex bytes.
func TestSkillCatalogueBounds(t *testing.T) {
	t.Parallel()
	e := &Engine{docs: &docs.Service{}}
	snap := Snapshot{}
	for i := range maxSkills {
		name := "skill-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		snap["skill."+name] = Override{1, map[string]string{skillsDir + name + "/SKILL.md": "---\nname: " + name + "\ndescription: " + strings.Repeat("d", 80) + "\nstatus: candidate\nreviewedBy:\n---\nBody.\n"}}
	}
	err := e.validate(snap, "skill.skill-aa")
	if !strings.Contains(problems(err), "at most 40 skills") || !strings.Contains(problems(err), "bytes of the brief, over 4096") {
		t.Fatalf("bounds: %s", problems(err))
	}
	if len(builtinSpecs.skillIndex()) > maxSkillIndex || len(builtinSpecs.skills) == 0 {
		t.Fatal("the built-in skills are over the brief's bound, or there are none")
	}
}

// TestUseSkillPagesAndRecords: use_skill returns a body or a reference file one page at a time
// (next continues exactly where the page ended), refuses unknown skills and files, spends no model
// call, and records the skill in the step's ledger writes.
func TestUseSkillPagesAndRecords(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("Step: compute it with numbers. ✓\n", 1200) // ~40 KB: several pages
	sp, err := newSpecs(Snapshot{"skill.tskill": {Version: 1, Files: tskill(skillMD("tskill", "candidate", "", long, "numbers"))}})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := sp.skill("tskill")
	e := &Engine{docs: &docs.Service{}, runs: map[string]*run{}}
	r := &run{e: e, specs: sp}
	e.runs["t"] = r // its writes go to the step buffer
	ctx := withSpecs(context.Background(), sp)
	tool := e.skillTool(sp)
	var got strings.Builder
	var next any
	pages := 0
	for {
		in, _ := json.Marshal(map[string]any{"name": "tskill", "file": nil, "offset": next})
		res, err := tool.Call(ctx, agent.Call{TaskID: "t", Input: in})
		if err != nil {
			t.Fatal(err)
		}
		sch, _ := agent.Compile(sp.Spec("use_skill").Out)
		if err := sch.ValidateOutput(res.Output); err != nil || len(res.Output) > agent.PageBytes || res.Usage.Calls != 0 {
			t.Fatalf("page %d: %v, %d bytes, %d calls", pages, err, len(res.Output), res.Usage.Calls)
		}
		var o struct {
			Text      string
			Truncated bool
			Next      *int
		}
		_ = json.Unmarshal(res.Output, &o)
		got.WriteString(o.Text)
		pages++
		if !o.Truncated {
			break
		}
		next = *o.Next
	}
	if got.String() != k.Body || pages < 3 {
		t.Fatalf("%d pages did not rebuild the body (%d of %d bytes)", pages, got.Len(), len(k.Body))
	}
	in, _ := json.Marshal(map[string]any{"name": "tskill", "file": "examples.json", "offset": nil})
	res, err := tool.Call(ctx, agent.Call{TaskID: "t", Input: in})
	if err != nil || !strings.Contains(string(res.Output), `"file":"examples.json"`) || !strings.Contains(string(res.Output), `\"op\": \"calc\"`) {
		t.Fatalf("reference file: %s %v", res.Output, err)
	}
	for _, bad := range []map[string]any{{"name": "nope", "file": nil, "offset": nil}, {"name": "tskill", "file": "../lead.md", "offset": nil},
		{"name": "tskill", "file": nil, "offset": 1 << 30}} {
		in, _ := json.Marshal(bad)
		var fail *agent.Failure
		if _, err := tool.Call(ctx, agent.Call{TaskID: "t", Input: in}); !errors.As(err, &fail) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == 0 || r.buf[0].kind != kSkill || r.buf[0].key != "tskill" || !strings.Contains(string(r.buf[0].data), `"version":1`) {
		t.Fatalf("not recorded in the step's writes: %+v", r.buf)
	}
}

// leadPrompts are the lead's system prompts of every call fake saw.
func leadPrompts(fake *model.Fake) []string {
	var out []string
	for _, c := range fake.Calls() {
		if c.Agent == "lead" && len(c.Messages) > 0 && c.Messages[0].Role == "system" {
			s, _ := c.Messages[0].Content.(string)
			out = append(out, s)
		}
	}
	return out
}

// TestSnapshotAtStartAndResume: a task runs on the overrides in effect when it was submitted; a
// save after that reaches the next task only, also across a crash and a resume by another process.
func TestSnapshotAtStartAndResume(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	k := &killer{Fake: newFake(f.happy())}
	e := f.reopen(k, nil)
	ctx := context.Background()
	save := func(base int, mark string) {
		t.Helper()
		if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: base, Files: edited("prompt.lead", leadMD, func(s string) string { return s + "\n" + mark })}, "Ada"); err != nil {
			t.Fatal(err)
		}
	}
	save(0, "MARK-ONE")
	task := f.submit(0)
	save(1, "MARK-TWO") // after submission: not this task's
	e.afterCommit = func(id string, n int) {
		if n == 4 {
			e.active(id).cancel() // the process dies after step 4 commits
		}
	}
	f.step(ctx)
	save(2, "MARK-THREE") // while it is stopped
	e2 := f.reopen(k, nil)
	f.stale(task.ID)
	f.step(ctx)
	if v := f.get(task.ID); stop(v) != "awaiting_gate/finished" {
		t.Fatalf("the resumed run: %s %s", stop(v), f.steps(task.ID))
	}
	ps := leadPrompts(k.Fake)
	if len(ps) < 5 {
		t.Fatalf("%d lead calls", len(ps))
	}
	for i, p := range ps {
		if !strings.Contains(p, "MARK-ONE") || strings.Contains(p, "MARK-TWO") || strings.Contains(p, "MARK-THREE") {
			t.Fatalf("lead call %d did not run on the task's snapshot: %q", i, p[len(p)-40:])
		}
	}
	var snap Snapshot
	var raw []byte
	if err := f.st.DB().QueryRow(`SELECT specs FROM tasks WHERE id = $1`, task.ID).Scan(&raw); err != nil || json.Unmarshal(raw, &snap) != nil || snap["prompt.lead"].Version != 1 {
		t.Fatalf("stored snapshot: %s %v", raw, err)
	}
	next, err := e2.Submit(ctx, Task{Description: "x", WorkflowType: "memo", DocumentIDs: f.ids(), ToolBandit: true})
	if err != nil || next.Specs["prompt.lead"].Version != 3 || !strings.Contains(next.Specs["prompt.lead"].Files[leadMD], "MARK-THREE") {
		t.Fatalf("the next task's snapshot: %+v %v", next.Specs, err)
	}
}

// TestOverrideChangesCacheKeys: the figure reader's and judge's cache keys hash their specs from the
// run's set, so an override of any of their files reads (and judges) again rather than reuse.
func TestOverrideChangesCacheKeys(t *testing.T) {
	t.Parallel()
	for _, files := range [][]string{readerSpecFiles, judgeSpecFiles} {
		base := builtinSpecs.hashPaths(files)
		for _, name := range files {
			if overrideOne(t, engineDir+name, "\n").hashPaths(files) == base {
				t.Errorf("an override of %s leaves the cache key", name)
			}
		}
	}
	sp := overrideOne(t, agentsDir+"read_figures.md", "\nRead carefully.")
	if specsOf(withSpecs(context.Background(), sp)).hashPaths(readerSpecFiles) == specsOf(context.Background()).hashPaths(readerSpecFiles) {
		t.Fatal("a run's context does not carry its set's hash")
	}
}

// TestSkillRunAndLimitations: the lead reads a skill before its plan (it spends nothing); the step
// is journaled and survives a crash and resume; the deliverable names the candidate skill it used.
func TestSkillRunAndLimitations(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sc := f.happy()
	sc["lead"] = append([]model.Reply{calls(tc("use_skill", m{"name": "tskill", "file": nil, "offset": nil}))}, sc["lead"]...)
	k := &killer{Fake: newFake(sc)}
	e := f.reopen(k, nil)
	save(t, e, "skill.tskill", 0, tskill(""))
	task := f.submit(0)
	e.afterCommit = func(id string, n int) {
		if n == 2 { // right after use_skill's step commits
			e.active(id).cancel()
		}
	}
	f.step(context.Background())
	if st := f.steps(task.ID); !strings.HasSuffix(st, "2:tool_call:use_skill:committed") {
		t.Fatalf("first process: %s", st)
	}
	f.reopen(k, nil)
	f.stale(task.ID)
	f.step(context.Background())
	v := f.get(task.ID)
	if stop(v) != "awaiting_gate/finished" || v.Output == nil || !strings.Contains(*v.Output, "candidate skills (tskill)") {
		t.Fatalf("%s\n%s", stop(v), deref(v.Output))
	}
	ss, _ := f.e.Steps(context.Background(), task.ID, 0)
	if ss[1].Tool != "use_skill" || ss[1].usage.Calls != 0 || !strings.Contains(string(ss[1].Output), "Compute every deadline") && !strings.Contains(string(ss[1].Output), "trigger date") {
		t.Fatalf("the skill's step: %+v", ss[1])
	}
	rows := 0
	for _, r := range f.ledger(task.ID) {
		rows += btoi(r.Kind == kSkill)
	}
	if rows != 1 {
		t.Fatalf("%d skill rows", rows)
	}
}

// TestExportPatchApplies: the export's patch, applied at the repository root (git apply), turns the
// built-in files into the overridden ones: an edited prompt, a created skill and a deleted one.
func TestExportPatchApplies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Files: edited("prompt.lead", leadMD, func(s string) string { return strings.Replace(s, "in order:", "in this order:", 1) })}, "Ada"); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: notes\ndescription: Keep notes.\nstatus: candidate\nreviewedBy:\n---\nKeep notes."
	if _, _, err := e.Save(ctx, "skill.notes", SaveIn{Files: map[string]string{skillsDir + "notes/SKILL.md": md}}, "Ada"); err != nil {
		t.Fatal(err)
	}
	starter := aSkill(t)
	if _, err := e.Delete(ctx, "skill."+starter, 0, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	now, built, err := e.Export(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := Patch(now, built)
	for _, want := range []string{"--- a/" + leadMD + "\n+++ b/" + leadMD, "-What usually works, in order:", "+What usually works, in this order:",
		"new file mode 100644\n--- /dev/null\n+++ b/" + skillsDir + "notes/SKILL.md", "\\ No newline at end of file",
		"deleted file mode 100644\n--- a/" + skillsDir + starter + "/SKILL.md\n+++ /dev/null"} {
		if !strings.Contains(p, want) {
			t.Errorf("the patch lacks %q:\n%s", want, p)
		}
	}
	if only, _, _ := e.Export(ctx, []string{"skill.notes"}); len(only) != 1 {
		t.Errorf("an export of one item holds %d files", len(only))
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: the patch's text was checked, not applied")
	}
	dir := t.TempDir()
	for path, data := range builtin {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, path), data, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "x.patch"), []byte(p), 0o644)
	cmd := exec.Command("git", "apply", "x.patch")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git apply: %v\n%s", err, out)
	}
	for path, want := range now {
		if got, _ := os.ReadFile(filepath.Join(dir, path)); string(got) != want {
			t.Errorf("%s after the patch differs", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, skillsDir, starter+"/SKILL.md")); !os.IsNotExist(err) {
		t.Error("the deleted skill's file is still there")
	}
}

// TestCatalogue: every built-in file is one item's; the catalogue's sections, where each item is
// used, recent usage from the step log, and the checks Go runs around a tool on that tool's detail.
func TestCatalogue(t *testing.T) {
	t.Parallel()
	for f := range builtin {
		if itemOf(f) == "" {
			t.Errorf("%s belongs to no item", f)
		}
	}
	f := newFixture(t)
	f.run(f.happy(), Config{})
	items, err := f.e.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range items {
		by[it.ID] = it
	}
	for id, sec := range map[string]string{"prompt.lead": "agents", "agent.read_figures": "agents", "pass.sweep": "agents", "tool.numbers": "tools",
		"tool.use_skill": "tools", "pack.jurisdiction.us": "packs", "skill." + aSkill(t): "skills"} {
		if by[id].Section != sec {
			t.Errorf("%s: section %q, want %s", id, by[id].Section, sec)
		}
	}
	if !slices.Contains(by["tool.retrieve"].UsedBy, "lead") || !slices.Contains(by["tool.retrieve"].UsedBy, "extract_facts") || !slices.Contains(by["agent.extract_facts"].UsedBy, "sweep") {
		t.Errorf("used by: %v %v", by["tool.retrieve"].UsedBy, by["agent.extract_facts"].UsedBy)
	}
	if u := by["prompt.lead"].Usage; u == nil || u.Tasks != 1 || u.Steps < 5 || u.Calls < 5 {
		t.Errorf("the lead's usage: %+v", u)
	}
	if u := by["agent.draft_section"].Usage; u == nil || u.Steps != 1 {
		t.Errorf("draft_section's usage: %+v", u)
	}
	for id, want := range map[string]bool{"agent.check_deviation": true, "tool.numbers": true, "pass.finish": true, "tool.retrieve": false, "prompt.lead": false} {
		if _, ok := by[id]; !ok {
			t.Fatalf("no item %s", id)
		}
		d, err := f.e.ItemDetail(context.Background(), id)
		if err != nil || (len(d.Checks) > 0) != want || want && !slices.Equal(d.Checks, goChecks(d.Name)) {
			t.Errorf("%s's checks: %v %v", id, d.Checks, err)
		}
	}
	if slices.ContainsFunc(items, func(it Item) bool { return strings.HasPrefix(it.ID, "hook.") }) {
		t.Error("a Go hook is still an item")
	}
}

// pretty is a JSON value indented as the spec files are (one space).
func pretty(v any) string {
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b) + "\n"
}

func save(t *testing.T, e *Engine, id string, base int, files map[string]string) {
	t.Helper()
	if _, _, err := e.Save(context.Background(), id, SaveIn{Base: base, Files: files}, "Ada"); err != nil {
		t.Fatalf("save %s: %s", id, problems(err))
	}
}

// TestEnumRenameRefusedEndToEnd (review H1): renaming the verdict Go turns into a HIGH critic finding
// is refused at save, so the committee's gate cannot be skipped by an edit.
func TestEnumRenameRefusedEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	files := specEdit(t, "agent.prosecute", agentsDir+"prosecute.json", func(m map[string]any) {
		at(m, "out", "verdicts", "[]", "verdict")["enum"] = []string{"supported", "overstated", "not_supported"}
	})
	if _, _, err := e.Save(context.Background(), "agent.prosecute", SaveIn{Files: files}, "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Go branches on") {
		t.Fatalf("the rename was saved: %v", err)
	}
	if items, _ := e.Items(context.Background()); slices.ContainsFunc(items, func(i Item) bool { return i.ID == "agent.prosecute" && i.Version > 0 }) {
		t.Fatal("a refused save left a version")
	}
}

// TestSavesAreSerial (review L1): two saves of different items that are each valid alone but break
// the set together cannot both land: they validate one after the other.
func TestSavesAreSerial(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	rf := agentsDir + "reproduce.json"
	withSkill := specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["allowed"] = []string{"retrieve", "numbers", "use_skill", "exact_calc"} })
	save(t, e, "agent.reproduce", 0, withSkill) // exact_calc is now a tool use_skill's callers may call
	without := specEdit(t, "agent.reproduce", rf, func(m map[string]any) { m["allowed"] = []string{"retrieve", "numbers", "use_skill"} })
	skill := tskill(skillMD("tskill", "candidate", "", "Body.", "exact_calc"))
	errs := make(chan error, 2)
	go func() {
		_, _, err := e.Save(ctx, "agent.reproduce", SaveIn{Base: 1, Files: without}, "Ada")
		errs <- err
	}()
	go func() { _, _, err := e.Save(ctx, "skill.tskill", SaveIn{Files: skill}, "Bo"); errs <- err }()
	if a, b := <-errs, <-errs; a == nil && b == nil {
		t.Fatal("both saves landed: the set they make does not load")
	}
	snap, _, _ := e.current(ctx, e.db)
	if err := e.validate(snap, "agent.reproduce"); err != nil {
		t.Fatalf("the stored set does not validate: %s", problems(err))
	}
}

// TestAgentHashCoversAllowedTools (review L2): the reader's cache key hashes every tool its allowed
// list names, by name, so a tool added there, or that tool's override, reads again; as built in it
// is the hash it always was.
func TestAgentHashCoversAllowedTools(t *testing.T) {
	t.Parallel()
	if builtinSpecs.agentHash("read_figures") != builtinSpecs.hashPaths(readerSpecFiles) || builtinSpecs.agentHash("judge_figures") != builtinSpecs.hashPaths(judgeSpecFiles) {
		t.Fatal("the built-in reader's or judge's hash changed: their cached readings would all be read again")
	}
	rfj := agentsDir + "read_figures.json"
	added := specEdit(t, "agent.read_figures", rfj, func(m map[string]any) { m["allowed"] = []string{"figure_context", "exact_calc", "retrieve"} })
	one, err := newSpecs(Snapshot{"agent.read_figures": {1, added}})
	if err != nil {
		t.Fatal(err)
	}
	retr := fileMap(builtin, "tool.retrieve")
	for k := range retr {
		retr[k] = strings.Replace(retr[k], "Reads the matter", "Reads, only, the matter", 1)
	}
	two, err := newSpecs(Snapshot{"agent.read_figures": {1, added}, "tool.retrieve": {1, retr}})
	if err != nil {
		t.Fatal(err)
	}
	if h0, h1, h2 := builtinSpecs.agentHash("read_figures"), one.agentHash("read_figures"), two.agentHash("read_figures"); h0 == h1 || h1 == h2 {
		t.Fatal("a tool added to the reader's list, or its override, leaves the cache key")
	}
}

// TestRecreatedSkillReviewedAgainstItsPast (#56 r2 follow-up): a skill deleted and created again
// under the same reviewer keeps its review only when its content is what it was before the
// deletion; changed content goes back to candidate.
func TestRecreatedSkillReviewedAgainstItsPast(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	save(t, e, "skill.tskill", 0, tskill(skillMD("tskill", "reviewed", "Ada Lovelace", "Compute every deadline with numbers.", "numbers")))
	at := func() string {
		d, err := e.ItemDetail(ctx, "skill.tskill")
		if err != nil {
			t.Fatal(err)
		}
		return d.Content[tskillMD]
	}
	if !strings.Contains(at(), "status: reviewed") {
		t.Fatalf("a new skill naming its reviewer: %s", at())
	}
	if _, err := e.Delete(ctx, "skill.tskill", 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	save(t, e, "skill.tskill", 2, tskill(skillMD("tskill", "reviewed", "Ada Lovelace", "Compute every deadline with numbers.", "numbers")))
	if !strings.Contains(at(), "status: reviewed") {
		t.Fatalf("re-created unchanged: %s", at())
	}
	if _, err := e.Delete(ctx, "skill.tskill", 3, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	_, notes, err := e.Save(ctx, "skill.tskill", SaveIn{Base: 4, Files: tskill(skillMD("tskill", "reviewed", "Ada Lovelace", "Skip the deadlines.", "numbers"))}, "Mallory")
	if err != nil || !strings.Contains(at(), "status: candidate") || len(notes) == 0 {
		t.Fatalf("re-created with changed content under the same reviewer: %s %v %v", at(), notes, err)
	}
}

// TestSoloFrozen (#56 r2 follow-up): solo is Go's like final: dropping ledger's solo plan op (which
// would let a subagent or a multi-call turn rewrite the plan) is refused.
func TestSoloFrozen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	if _, _, err := e.Save(context.Background(), "pass.ledger", SaveIn{Files: specEdit(t, "pass.ledger", engineDir+"specs/ledger.json", func(s map[string]any) { delete(s, "solo") })}, "Ada"); !strings.Contains(problems(err), "solo: must stay [plan]") {
		t.Fatalf("solo dropped: %s", problems(err))
	}
	if _, _, err := e.Save(context.Background(), "agent.helper", SaveIn{Files: func() map[string]string {
		fs := customAgent("helper", "retrieve")
		var s map[string]any
		json.Unmarshal([]byte(fs[agentsDir+"helper.json"]), &s)
		s["solo"] = []string{"*"}
		fs[agentsDir+"helper.json"] = pretty(s)
		return fs
	}()}, "Ada"); !strings.Contains(problems(err), "solo: must stay []") {
		t.Fatalf("a custom subagent with solo: %s", problems(err))
	}
}

// TestManageDiffNilSide: either side of a diff (and Patch's built) may be nil: a file only on the
// other side is a new or a deleted file, never a panic (PR #59 r1 M1).
func TestManageDiffNilSide(t *testing.T) {
	t.Parallel()
	const p = "internal/engine/specs/x.md"
	if got := Patch(map[string]string{p: "a\n"}, nil); !strings.Contains(got, "new file mode 100644") || !strings.Contains(got, "+a") {
		t.Fatalf("Patch(now, nil): %q", got)
	}
	if ds := diffFiles(nil, map[string]string{p: "a\n"}); len(ds) != 1 || !strings.Contains(ds[0].Patch, "--- /dev/null") {
		t.Fatalf("diffFiles(nil, b): %+v", ds)
	}
	if ds := diffFiles(map[string]string{p: "a\n"}, nil); len(ds) != 1 || !strings.Contains(ds[0].Patch, "+++ /dev/null") {
		t.Fatalf("diffFiles(a, nil): %+v", ds)
	}
	if ds := diffFiles(nil, nil); ds == nil || len(ds) != 0 {
		t.Fatalf("diffFiles(nil, nil): %#v", ds)
	}
}

// TestManageJSONNullFiles: a version row whose files are a jsonb null (not SQL NULL, which no BigLaw
// write makes) reads as no files, as it did before the version-file readers were shared (PR #59 r1
// L2); SQL NULL is the built-in.
func TestManageJSONNullFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES
		('prompt.lead', 1, 'save', 'null'::jsonb, 0, 't'), ('prompt.lead', 2, 'revert', NULL, 1, 't')`); err != nil {
		t.Fatal(err)
	}
	if files, err := e.filesAt(ctx, "prompt.lead", 1); err != nil || files != nil {
		t.Fatalf("jsonb null: %v %v", files, err)
	}
	if files, err := e.filesAt(ctx, "prompt.lead", 2); err != nil || files[leadFile] == "" {
		t.Fatalf("SQL NULL is the built-in: %v %v", files, err)
	}
	if _, err := e.filesAt(ctx, "prompt.lead", 3); err == nil {
		t.Fatal("no version 3, no error")
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('prompt.meta', 1, 'save', 'null'::jsonb, 0, 't')`); err != nil {
		t.Fatal(err)
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if files, err := lastContent(ctx, tx, "prompt.meta"); err != nil || files != nil {
		t.Fatalf("lastContent of a jsonb null: %v %v", files, err)
	}
	if files, err := lastContent(ctx, tx, "prompt.lead"); err != nil || files[leadFile] == "" {
		t.Fatalf("lastContent of SQL NULL is the built-in: %v %v", files, err)
	}
}

const leadFile = engineDir + "specs/lead.md"

// TestManageSaveNeverDeletes: a save with no files would delete its item; it is refused for the
// engine's workflow and what Go needs with Delete's own reasons, and for every other built-in (a
// built-in skill is deleted with Delete). Before, {"base":0} deleted the lead's prompt and every
// Manage read then panicked.
func TestManageSaveNeverDeletes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	for id, want := range map[string]string{
		"prompt.lead":          "prompt.lead is part of the engine's workflow: edit or revert it; it cannot be deleted",
		"pack.jurisdiction.us": "is part of the engine's workflow",
		"tool.retrieve":        "tool.retrieve cannot be deleted or disabled: it is one of the memo lead's tools",
		"agent.extract_facts":  "cannot be deleted or disabled",
		"skill." + aSkill(t):   "is built in: a save keeps its files",
	} {
		for _, in := range []SaveIn{{}, {Files: map[string]string{}}} {
			if _, _, err := e.Save(ctx, id, in, "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
				t.Errorf("save of %s with no files: %v", id, err)
			}
		}
	}
	if vs, err := e.Changes(ctx, 10); err != nil || len(vs) != 0 {
		t.Fatalf("a refused save was stored: %v %v", vs, err)
	}
}

// TestManageSurvivesAMissingPrompt: a prompt whose file a version deleted (rows saved before saves
// kept built-ins' files) is listed as deleted, its detail opens, and a revert restores it: no panic.
func TestManageSurvivesAMissingPrompt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('prompt.lead', 1, 'save', '{}'::jsonb, 0, 't')`); err != nil {
		t.Fatal(err)
	}
	items, err := e.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "prompt.lead" })
	if i < 0 || !items[i].Deleted || items[i].Summary != deletedSummary {
		t.Fatalf("the missing prompt's item: %+v", items[i])
	}
	if d, err := e.ItemDetail(ctx, "prompt.lead"); err != nil || len(d.Content) != 0 || d.Original[leadFile] == "" {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if _, err := e.Revert(ctx, "prompt.lead", 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if d, err := e.ItemDetail(ctx, "prompt.lead"); err != nil || d.Deleted || d.Content[leadFile] == "" {
		t.Fatalf("after the revert: %+v %v", d.Item, err)
	}
}

// TestManageSurvivesABrokenVersion: a stored version that no longer loads (edited in the database,
// or saved under an older parser) is listed as broken with why, its detail shows what is stored,
// every other item is still listed, and a revert repairs it.
func TestManageSurvivesABrokenVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	const rf = agentsDir + "reproduce.json"
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('agent.reproduce', 1, 'save', $1::jsonb, 0, 't')`,
		`{"`+rf+`": "{", "`+agentsDir+`reproduce.md": "x"}`); err != nil {
		t.Fatal(err)
	}
	items, err := e.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "agent.reproduce" })
	if i < 0 || items[i].Status != "broken" || !strings.Contains(items[i].Summary, "does not load") || len(items) != len(Builtins()) {
		t.Fatalf("the broken item (%d items): %+v", len(items), items[i])
	}
	if d, err := e.ItemDetail(ctx, "agent.reproduce"); err != nil || d.Content[rf] != "{" {
		t.Fatalf("detail: %+v %v", d.Content, err)
	}
	if _, err := e.ItemDetail(ctx, "prompt.lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Revert(ctx, "agent.reproduce", 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if d, err := e.ItemDetail(ctx, "agent.reproduce"); err != nil || d.Status == "broken" {
		t.Fatalf("after the revert: %+v %v", d.Item, err)
	}
}

// seedVersions stores rows as an earlier build (or a database edit) left them.
func seedVersions(t *testing.T, e *Engine, rows ...string) {
	t.Helper()
	for _, r := range rows {
		if _, err := e.db.ExecContext(context.Background(), `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES `+r); err != nil {
			t.Fatal(err)
		}
	}
}

// TestManageRevertNeverDeletes: a revert to a tombstone (a version the old Save bug wrote) of what
// may never be deleted is refused with Delete's reason, nothing stored, and History marks that
// version unrevertable; a built-in skill's tombstone still reverts (skills are deletable). PR #59 r2-1.
func TestManageRevertNeverDeletes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	seedVersions(t, e, `('prompt.lead', 1, 'save', '{}'::jsonb, 0, 't')`, `('prompt.lead', 2, 'revert', NULL, 1, 't')`,
		`('tool.retrieve', 1, 'save', '{}'::jsonb, 0, 't')`, `('tool.retrieve', 2, 'revert', NULL, 1, 't')`)
	for id, want := range map[string]string{"prompt.lead": "prompt.lead is part of the engine's workflow", "tool.retrieve": "tool.retrieve cannot be deleted or disabled"} {
		if _, err := e.Revert(ctx, id, 1, 2, "", "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("revert of %s to its tombstone: %v", id, err)
		}
		vs, err := e.History(ctx, id)
		if err != nil || len(vs) != 2 || vs[0].Unrevertable != "" || !strings.Contains(vs[1].Unrevertable, want) {
			t.Fatalf("%s's history: %+v %v", id, vs, err)
		}
	}
	if d, err := e.ItemDetail(ctx, "prompt.lead"); err != nil || d.Deleted || d.Latest != 2 {
		t.Fatalf("a refused revert changed the lead: %+v %v", d.Item, err)
	}
	k := "skill." + aSkill(t)
	if _, err := e.Delete(ctx, k, 0, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Revert(ctx, k, 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := e.History(ctx, k); vs[1].Unrevertable != "" {
		t.Fatalf("a skill's tombstone is revertable: %+v", vs[1])
	}
	if _, err := e.Revert(ctx, k, 1, 2, "", "Ada"); err != nil {
		t.Fatalf("revert of a built-in skill to its deletion: %v", err)
	}
}

// TestManageBrokenVersionNamed: while a stored version does not load, every other write is a 422
// naming it and saying to revert it (it was a 500 "internal error"), the catalogue says so, a task
// fails naming it, and its revert still works. PR #59 r2-2.
func TestManageBrokenVersionNamed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "The notice period is thirty days.")
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	lead := fileMap(builtin, "prompt.lead")
	lead[leadFile] += "\nBe brief.\n"
	if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Files: lead}, "Ada"); err != nil {
		t.Fatal(err)
	}
	seedVersions(t, e, `('agent.reproduce', 1, 'save', '{"`+agentsDir+`reproduce.json": "{", "`+agentsDir+`reproduce.md": "x"}'::jsonb, 0, 't')`)
	const want = "the latest version of agent.reproduce does not load"
	lead[leadFile] += "More.\n"
	for what, err := range map[string]error{
		"save":   func() error { _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 1, Files: lead}, "Ada"); return err }(),
		"revert": func() error { _, err := e.Revert(ctx, "prompt.lead", 0, 1, "", "Ada"); return err }(),
		"delete": func() error { _, err := e.Delete(ctx, "skill."+aSkill(t), 0, "", "Ada"); return err }(),
	} {
		code, _, msg := ErrorType(err)
		if code != 422 || !strings.Contains(msg, want) || !strings.Contains(msg, "revert it in Manage") {
			t.Errorf("%s while agent.reproduce is broken: %d %s", what, code, msg)
		}
	}
	items, err := e.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "agent.reproduce" }); !strings.Contains(items[i].Summary, "every new task fails") {
		t.Fatalf("the catalogue's line: %s", items[i].Summary)
	}
	task := f.submit(5)
	f.step(ctx)
	if v := f.get(task.ID); v.Status != "failed" || v.Error == nil || !strings.Contains(*v.Error, "invalid: the task's spec snapshot does not load: "+want) {
		t.Fatalf("the task on the broken set: %s %v", v.Status, *v.Error)
	}
	if _, err := e.Revert(ctx, "agent.reproduce", 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Save(ctx, "prompt.lead", SaveIn{Base: 1, Files: lead}, "Ada"); err != nil {
		t.Fatalf("a save after the revert: %v", err)
	}
}
