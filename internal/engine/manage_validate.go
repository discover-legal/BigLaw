// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Manage's validation (README § Manage): what an admin may save, file by file and as the whole set a
// task would run on, and each changed schema's compatibility with what Go reads and writes.

// The bounds of what an admin may save.
const (
	maxPromptBytes    = 64 << 10
	maxSkillBody      = 32 << 10
	maxSkillFile      = 64 << 10
	maxSkillFiles     = 16
	maxSubagentTurns  = 32
	maxToolTurns      = 10000
	maxSkillDescBytes = 200
)

var skillFileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,59}\.(md|json|csv)$`)

// validate checks the set snap would make, for a save of item id: id's files one by one, then the
// whole set the way a task would load it (newSpecs, then the runtime's registry with the lead's
// tool list), and every changed schema's compatibility with the built-in's (compat).
func (e *Engine) validate(snap Snapshot, id string) error {
	if e.remote == nil {
		e.remote = newRemoteClient(RemotePolicy{})
	}
	var ps []Problem
	bad := func(file, format string, a ...any) { ps = append(ps, Problem{file, fmt.Sprintf(format, a...)}) }
	kind, name, _ := strings.Cut(id, ".")
	files := snap[id].Files
	want := itemFiles(builtin, id)
	if kind != "skill" && len(want) > 0 && len(files) > 0 && !slices.Equal(slices.Sorted(maps.Keys(files)), want) {
		bad("", "the files must be exactly the item's own: %s", strings.Join(want, ", "))
	}
	if len(want) == 0 && len(files) > 0 { // an item an admin creates: its files where the engine loads them
		need := map[string][]string{"agent": {agentsDir + name + ".json", agentsDir + name + ".md"}, "tool": {toolsDir + name + ".json"}, "server": {serversDir + name + ".json"}, "rule": {hooksDir + name + ".json"}}[kind]
		if need != nil && !slices.Equal(slices.Sorted(maps.Keys(files)), need) {
			bad("", "a new %s is exactly these files: %s", kind, strings.Join(need, ", "))
		}
		if kind != "skill" && kind != "server" && !agent.ToolNameRe.MatchString(name) {
			bad("", "a name is 2–64 lower-case letters, digits and _, starting with a letter")
		}
		if slices.Contains(reservedNames, name) {
			bad("", "%q is reserved (the lead, the Test call's caller, meta and the engine's passes): choose another name", name)
		}
		if kind == "server" && !secretName.MatchString(name) {
			bad("", "a server's name is 2–40 lower-case letters, digits, _ and -, starting with a letter")
		}
	}
	if _, ok := files[skillsDir+name+"/SKILL.md"]; kind == "skill" && len(files) > 0 && !ok {
		bad("", "a skill needs its SKILL.md (front matter and instructions)")
	}
	if kind == "skill" && len(files) > maxSkillFiles+1 {
		bad("", "a skill has at most %d reference files", maxSkillFiles)
	}
	for _, f := range slices.Sorted(maps.Keys(files)) {
		text := files[f]
		if itemOf(f) != id {
			bad(f, "not one of %s's files", id)
			continue
		}
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			bad(f, "not UTF-8 text")
			continue
		}
		switch {
		case kind == "skill":
			validateSkillFile(f, name, text, bad)
		case strings.HasPrefix(f, toolsDir):
			e.validateCustomTool(f, text, bad)
		case strings.HasPrefix(f, serversDir):
			e.validateServer(f, text, bad)
		case strings.HasPrefix(f, hooksDir):
			e.validateHookRule(f, text, bad)
		case kind == "pack":
			validatePack(f, text, bad)
		case strings.HasSuffix(f, ".md"):
			if strings.TrimSpace(text) == "" || len(text) > maxPromptBytes {
				bad(f, "a prompt is 1 byte to %d KiB of text", maxPromptBytes>>10)
			}
		default:
			validateSpec(f, text, bad)
			if b, ok := builtin[f]; ok {
				for _, p := range compatSpec(b, []byte(text)) {
					bad(f, "%s", p)
				}
			}
		}
	}
	if len(ps) > 0 {
		return &InvalidError{ps}
	}
	sp, err := newSpecs(snap)
	if err != nil {
		return brokenSet(snap, id, err)
	}
	if len(sp.skills) > maxSkills {
		bad("", "at most %d skills: the brief lists every one", maxSkills)
	}
	if n := len(sp.skillIndex()); n > maxSkillIndex {
		bad("", "the skills' names and descriptions take %d bytes of the brief, over %d: shorten a description or delete a skill", n, maxSkillIndex)
	}
	for _, n := range slices.Sorted(maps.Keys(sp.tools)) {
		if m := sp.tools[n].MCP; m != nil {
			if _, ok := sp.servers[m.Server]; !ok {
				bad(toolsDir+n+".json", "mcp.server: %q is not a saved MCP server (a tool needs its server)", m.Server)
			}
		}
	}
	for _, n := range slices.Sorted(maps.Keys(sp.specs)) { // a subagent never ends the run or acts as the lead
		if s := sp.specs[n]; s.Kind == "subagent" {
			for _, a := range s.Allowed {
				_, built := builtin[agentsDir+n+".json"]
				if t := sp.specs[a]; t.Final || len(t.Solo) > 0 && (!built || !needs(a, subagentOK)) {
					bad(agentsDir+n+".json", "allowed: %q is the lead's own (it ends the run, or has calls that must stand alone in a lead turn); a subagent cannot be given it", a)
				}
			}
		}
	}
	validateHookRefs(sp, bad)
	callable := skillCallable(sp)
	for _, k := range sp.skills {
		for _, t := range k.Tools {
			if !slices.Contains(callable, t) {
				bad(skillsDir+k.Name+"/SKILL.md", "tools: %q is not a tool use_skill's callers may call (a skill grants no tool; it can only point at %s)", t, strings.Join(callable, ", "))
			}
		}
	}
	r := &run{e: e, specs: sp, t: Task{WorkflowType: "memo"}}
	if rt, err := e.runtime(r); err != nil {
		bad("", "%s", strings.TrimPrefix(err.Error(), "agent: "))
	} else if _, err := rt.Reg.Defs(leadVisible(sp)); err != nil {
		bad("", "%s", strings.TrimPrefix(err.Error(), "agent: "))
	}
	if len(ps) > 0 {
		return &InvalidError{ps}
	}
	return nil
}

func validateSpec(f, text string, bad func(string, string, ...any)) {
	var s agent.Spec
	if err := strictDecode(text, &s); err != nil {
		bad(f, "not one well-formed spec object (fields: name, kind, class, maxTurns, role, allowed, description, in, out, solo, final): %v", err)
		return
	}
	var was agent.Spec
	if json.Unmarshal(builtin[f], &was) != nil { // a subagent an admin creates
		was = agent.Spec{Name: s.Name, Kind: "subagent"}
	}
	switch {
	case s.Name != fileStem(f):
		bad(f, "name: must stay %q (Go and the other specs call it by name)", fileStem(f))
	case s.Kind != was.Kind:
		bad(f, "kind: must stay %q (Go implements it as one)", was.Kind)
	case s.Final != was.Final:
		bad(f, "final: must stay %v (only finish may end the run)", was.Final)
	case !slices.Equal(s.Solo, was.Solo):
		bad(f, "solo: must stay %v (the ops that must stand alone in a lead turn are Go's: ledger's plan, finish)", was.Solo)
	case s.Kind == "subagent" && (s.MaxTurns < 1 || s.MaxTurns > maxSubagentTurns):
		bad(f, "maxTurns: a subagent's is 1 to %d", maxSubagentTurns)
	case s.Kind != "subagent" && (s.MaxTurns < 0 || s.MaxTurns > maxToolTurns):
		bad(f, "maxTurns: a Go tool's is 0 to %d", maxToolTurns)
	case s.Kind != "subagent" && (s.Role != "" || len(s.Allowed) > 0):
		bad(f, "role and allowed are a subagent's only")
	case s.Kind == "subagent" && s.Class == "free" && was.Class != "free":
		bad(f, "class: free is the figure reader's and judge's only (Go meters them from their own shares); a subagent the lead or a fan-out calls takes a class with a share (evidence, draft, committee, cover or plan)")
	}
	validateDescription(f, s.Description, bad)
	for _, sc := range []struct {
		n   string
		raw json.RawMessage
	}{{"in", s.In}, {"out", s.Out}} {
		c, err := agent.Compile(sc.raw)
		if err != nil {
			bad(f, "%s: %v", sc.n, err)
		} else if why := c.Strict(sc.n); why != "" {
			bad(f, "%s: not strict-shaped: %s", sc.n, why)
		}
	}
}

// strictDecode decodes text as exactly one JSON object holding only v's fields.
func strictDecode(text string, v any) error {
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil || d.More() {
		return cmp.Or(err, errors.New("more after the object"))
	}
	return nil
}

// fileStem is a .json file's name without its directory and extension.
func fileStem(f string) string { return strings.TrimSuffix(path.Base(f), ".json") }

// validateDescription bounds any spec's description (a model reads it in every prompt that offers
// the tool): at most maxDescBytes, no control or format characters but line breaks.
func validateDescription(f, d string, bad func(string, string, ...any)) {
	switch {
	case len(d) > maxDescBytes:
		bad(f, "description: %d bytes, over %d KiB (every prompt that offers it carries it)", len(d), maxDescBytes>>10)
	case strings.ContainsFunc(d, func(r rune) bool { return r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) }):
		bad(f, "description: no control or format characters (bidi overrides, zero-width marks) but line breaks")
	}
}

func validatePack(f, text string, bad func(string, string, ...any)) {
	fields, _, body, err := frontMatter(text)
	if err != nil {
		bad(f, "%v", err)
		return
	}
	knownKeys(f, fields, bad, "code", "status", "reviewedBy", "checklist")
	code := strings.TrimSuffix(path.Base(f), ".md")
	switch {
	case fields["code"] != code:
		bad(f, "code: must stay %q (the file's)", code)
	case fields["status"] != "candidate" && fields["status"] != "reviewed":
		bad(f, "status: candidate or reviewed")
	case fields["status"] == "reviewed" && fields["reviewedBy"] == "":
		bad(f, "status reviewed needs reviewedBy: the lawyer who reviewed it")
	case body == "":
		bad(f, "the body (after the front matter) is empty")
	case len(text) > maxPromptBytes:
		bad(f, "over %d KiB", maxPromptBytes>>10)
	}
}

// knownKeys reports each front matter key that is not one of known.
func knownKeys(f string, fields map[string]string, bad func(string, string, ...any), known ...string) {
	for k := range fields {
		if !slices.Contains(known, k) {
			bad(f, "front matter: unknown key %q (%s)", k, strings.Join(known, ", "))
		}
	}
}

func validateSkillFile(f, name, text string, bad func(string, string, ...any)) {
	base := path.Base(f)
	if path.Dir(f) != skillsDir+name {
		bad(f, "a skill's files sit in %s%s/, one level", skillsDir, name)
		return
	}
	if base != "SKILL.md" {
		switch {
		case !skillFileName.MatchString(base):
			bad(f, "a reference file is named in lower-case letters, digits, . _ - and ends .md, .json or .csv")
		case len(text) > maxSkillFile:
			bad(f, "a reference file is at most %d KiB", maxSkillFile>>10)
		case strings.HasSuffix(base, ".json") && !json.Valid([]byte(text)):
			bad(f, "not well-formed JSON")
		}
		return
	}
	fields, lists, body, err := frontMatter(text)
	if err != nil {
		bad(f, "%v", err)
		return
	}
	knownKeys(f, fields, bad, "name", "description", "status", "reviewedBy", "tools")
	desc := fields["description"]
	switch {
	case fields["name"] != name:
		bad(f, "name: must be %q (the skill's directory)", name)
	case desc == "" || len(desc) > maxSkillDescBytes:
		bad(f, "description: one line of 1 to %d bytes (the brief lists it)", maxSkillDescBytes)
	case fields["status"] != "candidate" && fields["status"] != "reviewed":
		bad(f, "status: candidate or reviewed")
	case fields["status"] == "reviewed" && fields["reviewedBy"] == "":
		bad(f, "status reviewed needs reviewedBy: the lawyer who reviewed it")
	case body == "" || len(body) > maxSkillBody:
		bad(f, "the body (the instructions) is 1 byte to %d KiB", maxSkillBody>>10)
	case len(lists["tools"]) > agent.MaxVisible:
		bad(f, "tools: at most %d", agent.MaxVisible)
	}
	if fields["tools"] != "" {
		bad(f, "tools: a list, one \"- name\" line each")
	}
}

// compatSpec is why a saved spec's schemas break what Go reads or writes (README § Manage,
// compatibility): every property the built-in schema has keeps its place and type (Go decodes or
// writes it by that shape: removing, renaming or retyping it is refused, nested ones too); a schema
// Go writes (a Go tool's output, the input of a tool Go calls) gains no property; the built-in
// description's examples and Go's own literal inputs still validate. Narrowing an enum or a bound
// of a value Go writes is not caught beyond those (a limit README states).
func compatSpec(old, cur []byte) []string {
	var was, now agent.Spec
	if json.Unmarshal(old, &was) != nil || json.Unmarshal(cur, &now) != nil {
		return nil
	}
	var out []string
	for _, sc := range []struct {
		n             string
		a, b          json.RawMessage
		goWrites, out bool
	}{{"in", was.In, now.In, needs(was.Name, goWrites), false}, {"out", was.Out, now.Out, was.Kind != "subagent", true}} {
		a, errA := agent.Compile(sc.a)
		b, errB := agent.Compile(sc.b)
		if errA != nil || errB != nil {
			continue // the schema's own check reports it
		}
		out = append(out, compatSchema(sc.n, a, b, sc.goWrites)...)
	}
	in, err := agent.Compile(now.In)
	if err != nil {
		return out
	}
	for _, m := range agent.ExampleRe.FindAllStringSubmatch(was.Description, -1) {
		if err := in.Validate(json.RawMessage(m[1])); err != nil {
			out = append(out, fmt.Sprintf("in: the built-in example %s no longer validates (%v)", m[1], err))
		}
	}
	if i := slices.IndexFunc(goContract, func(c goRow) bool { return c.name == was.Name && c.sent != "" }); i >= 0 {
		x := goContract[i].sent
		if err := in.Validate(json.RawMessage(x)); err != nil {
			out = append(out, fmt.Sprintf("in: Go calls %s with %s, which no longer validates (%v)", was.Name, x, err))
		}
	}
	return out
}

func compatSchema(at string, a, b *agent.Schema, goWrites bool) []string {
	var out []string
	if !slices.Equal(slices.Sorted(slices.Values(a.Type)), slices.Sorted(slices.Values(b.Type))) {
		return []string{fmt.Sprintf("%s: Go reads or writes this field as %s; it cannot be retyped (now %s)", at, typeList(a), typeList(b))}
	}
	if !slices.Equal(enumSet(a.Enum), enumSet(b.Enum)) { // Go branches on these values (a critic's verdict, a gap's severity, an op)
		out = append(out, fmt.Sprintf("%s: Go branches on this field's values (%s); its enum cannot change (now %s): a renamed, recased, added or removed value would skip or misroute Go's handling of it",
			at, strings.Join(enumSet(a.Enum), ", "), cmpStr(b.Enum == nil, "no enum", strings.Join(enumSet(b.Enum), ", "))))
	}
	for _, n := range slices.Sorted(maps.Keys(a.Properties)) {
		p, ok := b.Properties[n]
		if !ok {
			out = append(out, fmt.Sprintf("%s.%s: Go reads or writes this field; it cannot be removed or renamed", at, n))
			continue
		}
		out = append(out, compatSchema(at+"."+n, a.Properties[n], p, goWrites)...)
	}
	if goWrites {
		for _, n := range slices.Sorted(maps.Keys(b.Properties)) {
			if _, ok := a.Properties[n]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: Go writes this object and has no value for a new field (a strict schema requires every property)", at, n))
			}
		}
	}
	switch {
	case a.Items != nil && b.Items != nil:
		out = append(out, compatSchema(at+"[]", a.Items, b.Items, goWrites)...)
	case a.Items != nil:
		out = append(out, fmt.Sprintf("%s: Go reads or writes its items; they cannot be removed", at))
	}
	return out
}

// enumSet is an enum's values as a sorted set of their JSON forms (nil: no enum).
func enumSet(e []any) []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, v := range e {
		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func typeList(s *agent.Schema) string {
	if len(s.Type) == 0 {
		return "an enum"
	}
	return strings.Join(s.Type, " or ")
}
