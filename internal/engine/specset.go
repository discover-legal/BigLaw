// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
)

// The spec set (README § Manage): every agent, tool, pack and skill a run uses is a file built into
// the binary (go:embed), and an admin may override any of them with a versioned copy in Postgres
// (manage.go). A task runs on the set in effect when it was submitted (its snapshot, tasks.specs):
// a later save reaches the next task only, and a resumed run reads the same snapshot.

//go:embed skills
var skillFS embed.FS

// builtin is every editable file built into the binary, by its repo path (what an export patches).
var builtin = func() map[string][]byte {
	out := map[string][]byte{}
	add := func(fsys fs.FS, root, prefix string) {
		_ = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				data, rerr := fs.ReadFile(fsys, p)
				if rerr != nil {
					panic("engine: " + rerr.Error())
				}
				out[prefix+p] = data
			}
			return err
		})
	}
	add(specFS, "specs", engineDir)
	add(packFS, "packs", engineDir)
	add(skillFS, "skills", engineDir)
	add(analysis.SpecFS(), "specs", "internal/analysis/")
	add(docs.ToolSpecFS(), "specs", "internal/docs/")
	return out
}()

const (
	engineDir = "internal/engine/"
	agentsDir = engineDir + "specs/agents/"
	skillsDir = engineDir + "skills/"
)

// Snapshot is the overrides in effect for a task, by item id (tasks.specs): each its version and
// all of its files by repo path. No files is a deleted skill (a tombstone).
type Snapshot map[string]Override

type Override struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

// Specs is a spec set: the built-in files with one snapshot's overrides over them, parsed.
type Specs struct {
	files    map[string][]byte
	versions map[string]int // item id → its override's version (absent: the built-in)
	specs    map[string]agent.Spec
	paths    map[string]string // a spec's file, by its name
	packs    []Pack
	skills   []Skill
	tools    map[string]CustomTool // custom tools by name (custom.go)
	servers  map[string]Server     // MCP servers by name
}

// builtinSpecs is the set with no overrides: what a test, or a task submitted before 0008, runs on.
var builtinSpecs = func() *Specs {
	s, err := newSpecs(nil)
	if err != nil {
		panic("engine: the built-in specs: " + err.Error())
	}
	return s
}()

// newSpecs builds the set of snap: each item's built-in files replaced by its override's.
func newSpecs(snap Snapshot) (*Specs, error) {
	s := &Specs{files: maps.Clone(builtin), versions: map[string]int{}, specs: map[string]agent.Spec{}, paths: map[string]string{}, tools: map[string]CustomTool{}, servers: map[string]Server{}}
	for _, id := range slices.Sorted(maps.Keys(snap)) {
		o := snap[id]
		for _, f := range itemFiles(builtin, id) {
			delete(s.files, f)
		}
		for f, data := range o.Files {
			if itemOf(f) != id {
				return nil, fmt.Errorf("%s: file %s is not one of its files", id, f)
			}
			s.files[f] = []byte(data)
		}
		s.versions[id] = o.Version
	}
	for _, f := range slices.Sorted(maps.Keys(s.files)) {
		data := s.files[f]
		switch {
		case strings.HasPrefix(f, toolsDir):
			var t CustomTool
			if err := json.Unmarshal(data, &t); err != nil {
				return nil, fmt.Errorf("%s: %v", f, err)
			}
			s.tools[t.Name], s.specs[t.Name] = t, customSpec(t)
		case strings.HasPrefix(f, serversDir):
			var v Server
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, fmt.Errorf("%s: %v", f, err)
			}
			s.servers[v.Name] = v
		case strings.HasSuffix(f, ".json") && strings.Contains(f, "/specs/"):
			var sp agent.Spec
			if err := json.Unmarshal(data, &sp); err != nil {
				return nil, fmt.Errorf("%s: %v", f, err)
			}
			s.specs[sp.Name], s.paths[sp.Name] = sp, f
		case strings.HasPrefix(f, engineDir+"packs/"):
			p, err := parsePack(f, string(data))
			if err != nil {
				return nil, err
			}
			s.packs = append(s.packs, p)
		case strings.HasPrefix(f, skillsDir) && path.Base(f) == "SKILL.md":
			k, err := parseSkill(f, string(data))
			if err != nil {
				return nil, err
			}
			for g := range s.files {
				if path.Dir(g) == path.Dir(f) && g != f {
					k.Files = append(k.Files, path.Base(g))
				}
			}
			slices.Sort(k.Files)
			s.skills = append(s.skills, k)
		}
	}
	return s, nil
}

type specsKey struct{}

// withSpecs is ctx carrying a run's spec set: every spec, prompt, pack and skill read below it.
func withSpecs(ctx context.Context, s *Specs) context.Context {
	return context.WithValue(ctx, specsKey{}, s)
}

// specsOf is ctx's spec set (the built-ins outside a run).
func specsOf(ctx context.Context) *Specs {
	if s, ok := ctx.Value(specsKey{}).(*Specs); ok && s != nil {
		return s
	}
	return builtinSpecs
}

// sp is the run's spec set (the built-ins for a run a test made by hand).
func (r *run) sp() *Specs {
	if r == nil || r.specs == nil {
		return builtinSpecs
	}
	return r.specs
}

// Spec is a tool's or subagent's spec by name.
func (s *Specs) Spec(name string) agent.Spec { return s.specs[name] }

// prompt is specs/<name>.md ("lead", "agents/read_figures").
func (s *Specs) prompt(name string) string {
	b, ok := s.files[engineDir+"specs/"+name+".md"]
	if !ok {
		panic("engine: no prompt specs/" + name + ".md")
	}
	return string(b)
}

// agentHash is the hash of a subagent's spec and prompt and of every tool its allowed list names
// (their specs, in the list's order): what its cached answers are keyed by, so an override of any of
// them, or a tool added to the list, reads again. For read_figures and judge_figures as built in it
// is the hash of the same files as before overrides existed, so their cached rows stay valid.
func (s *Specs) agentHash(name string) string {
	files := []string{"specs/agents/" + name + ".json", "specs/agents/" + name + ".md"}
	for _, t := range s.specs[name].Allowed {
		if p, ok := s.paths[t]; ok {
			files = append(files, strings.TrimPrefix(p, engineDir))
		}
	}
	return s.hashPaths(files)
}

// hashPaths is hash over names relative to the engine (or repo paths outside it).
func (s *Specs) hashPaths(files []string) string {
	h := sha256.New()
	for _, f := range files {
		p := f
		if !strings.HasPrefix(f, "internal/") {
			p = engineDir + f
		}
		data, ok := s.files[p]
		if !ok {
			panic("engine: no spec file " + f)
		}
		fmt.Fprintf(h, "%s %d\n", f, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// subagents are the set's subagent specs and prompts, by file name, in name order.
func (s *Specs) subagents(rt *agent.Runtime) ([]agent.Tool, error) {
	var out []agent.Tool
	for _, f := range slices.Sorted(maps.Keys(s.files)) {
		if path.Dir(f)+"/" == agentsDir && strings.HasSuffix(f, ".json") {
			t, err := rt.LoadSubagent(path.Base(f), s.files[f], s.files[strings.TrimSuffix(f, ".json")+".md"])
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// respec is a tool registered with the set's spec for it (a docs or analysis tool an admin overrode).
type respec struct {
	agent.Tool
	s agent.Spec
}

func (r respec) Spec() agent.Spec { return r.s }

// frontMatter splits a pack's or a skill's Markdown into its front matter, between "---" lines at
// its start (key: value lines, a key with no value followed by "- item" lines as a list), and its
// body. Any other line, or a key given twice, is an error.
func frontMatter(text string) (fields map[string]string, lists map[string][]string, body string, err error) {
	rest, ok := strings.CutPrefix(text, "---\n")
	head, body, ok2 := strings.Cut(rest, "\n---\n")
	if !ok || !ok2 {
		return nil, nil, "", fmt.Errorf("the front matter must open and close with a --- line")
	}
	fields, lists = map[string]string{}, map[string][]string{}
	list := ""
	for i, ln := range strings.Split(head, "\n") {
		if item, ok := strings.CutPrefix(ln, "- "); ok && list != "" {
			lists[list] = append(lists[list], strings.TrimSpace(item))
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		k = strings.TrimSpace(k)
		_, seen := fields[k]
		if !ok || k == "" || strings.ContainsAny(k, " -") || seen {
			return nil, nil, "", fmt.Errorf("front matter line %d (%q) is neither key: value nor a list item", i+2, ln)
		}
		fields[k], list = strings.TrimSpace(v), ""
		if fields[k] == "" {
			list = k
		}
	}
	return fields, lists, strings.TrimSpace(body), nil
}

// parsePack is a pack file: code, status, reviewedBy and the checklist, then its body.
func parsePack(file, text string) (Pack, error) {
	f, l, body, err := frontMatter(text)
	if err != nil {
		return Pack{}, fmt.Errorf("%s: %v", file, err)
	}
	return Pack{Code: f["code"], Status: f["status"], ReviewedBy: f["reviewedBy"], Body: body, Checklist: l["checklist"],
		Practice: path.Base(path.Dir(file)) == "practice"}, nil
}

// Skill is a named bundle of instructions (README § Skills): a Markdown body the lead reads with
// use_skill, optional reference files beside it, and the tools it expects (never a grant: only
// tools its caller may already call). Status is candidate until a named lawyer reviews it.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	ReviewedBy  string   `json:"reviewedBy"`
	Tools       []string `json:"tools"`
	Files       []string `json:"files"` // its reference files' names
	Body        string   `json:"-"`
}

func parseSkill(file, text string) (Skill, error) {
	f, l, body, err := frontMatter(text)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %v", file, err)
	}
	return Skill{Name: f["name"], Description: f["description"], Status: f["status"], ReviewedBy: f["reviewedBy"], Tools: l["tools"], Files: []string{}, Body: body}, nil
}

// skill is the set's skill named name.
func (s *Specs) skill(name string) (Skill, bool) {
	i := slices.IndexFunc(s.skills, func(k Skill) bool { return k.Name == name })
	if i < 0 {
		return Skill{}, false
	}
	return s.skills[i], true
}

// Skill catalogue bounds: what the lead's brief lists of them (names and descriptions only).
const (
	maxSkills     = 40
	maxSkillIndex = 4096 // bytes of the brief's skill list
)

// skillIndex is the brief's list of skills: one line each, name and description.
func (s *Specs) skillIndex() string {
	var b strings.Builder
	for _, k := range s.skills {
		fmt.Fprintf(&b, "- %s (%s): %s\n", k.Name, k.Status, k.Description)
	}
	return b.String()
}

// itemOf is the catalogue item a repo path belongs to ("" for none).
func itemOf(f string) string {
	switch rest, _ := strings.CutPrefix(f, engineDir); {
	case strings.HasPrefix(f, toolsDir) && strings.HasSuffix(f, ".json"):
		return "tool." + strings.TrimSuffix(path.Base(f), ".json")
	case strings.HasPrefix(f, serversDir) && strings.HasSuffix(f, ".json"):
		return "server." + strings.TrimSuffix(path.Base(f), ".json")
	case strings.HasPrefix(f, agentsDir):
		return "agent." + strings.TrimSuffix(strings.TrimSuffix(path.Base(f), ".json"), ".md")
	case strings.HasPrefix(f, skillsDir):
		return "skill." + strings.SplitN(strings.TrimPrefix(f, skillsDir), "/", 2)[0]
	case strings.HasPrefix(rest, "packs/") && strings.HasSuffix(f, ".md"):
		return "pack." + strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(rest, "packs/"), ".md"), "/", ".")
	case strings.HasPrefix(rest, "specs/") && strings.HasSuffix(f, ".md"):
		return "prompt." + strings.TrimSuffix(path.Base(f), ".md")
	case strings.HasSuffix(f, ".json") && strings.Contains(f, "/specs/"):
		name := strings.TrimSuffix(path.Base(f), ".json")
		if slices.Contains(passes, name) {
			return "pass." + name
		}
		return "tool." + name
	}
	return ""
}

// passes are the engine's own passes over the run (Go tools whose specs the lead or Go calls).
var passes = []string{"sweep", "committee", "cover", "finish", "ledger"}

// itemFiles are an item's files in files, sorted.
func itemFiles(files map[string][]byte, id string) []string {
	var out []string
	for f := range files {
		if itemOf(f) == id {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}
