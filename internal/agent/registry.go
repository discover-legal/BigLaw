// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/discover-legal/BigLaw/internal/model"
)

// Caps (README: ≤ 22 tools in the registry, ≤ 14 visible to any one agent).
const (
	MaxTools   = 22
	MaxVisible = 14
)

// Registry is the single table of tools, addressed by name. A tool's definition is frozen when it
// is registered: no matter data can reach a schema, and the model's cached prefix stays valid.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*entry
}

type entry struct {
	tool    Tool
	spec    Spec
	in, out *Schema
	def     model.Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]*entry{}} }

var (
	toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	sentenceRe = regexp.MustCompile(`[.!?](\s|$)`)
	exampleRe  = regexp.MustCompile(`(?m)^Example: (\{.*\})\s*$`)
	lengthKw   = regexp.MustCompile(`,"m(?:in|ax)Length":\d+|"m(?:in|ax)Length":\d+,`) // in compacted JSON; a string's quotes are escaped
)

// Register adds t after checking its spec: a unique name, compilable strict-shaped schemas, a
// description of at least three sentences with a "Do not use" line and 1–3 example calls that
// validate against the input schema, and, for a subagent, a turn cap and Go-only allowed tools.
func (r *Registry) Register(t Tool) error {
	s := t.Spec()
	bad := func(format string, a ...any) error {
		return fmt.Errorf("agent: tool %q: "+format, append([]any{s.Name}, a...)...)
	}
	e := &entry{tool: t, spec: s}
	var err error
	if e.in, err = Compile(s.In); err != nil {
		return bad("input schema: %v", err)
	}
	if e.out, err = Compile(s.Out); err != nil {
		return bad("output schema: %v", err)
	}
	examples := exampleRe.FindAllStringSubmatch(s.Description, -1)
	switch {
	case !toolNameRe.MatchString(s.Name):
		return bad("the name is not 2–64 lower-case letters, digits and _")
	case !slices.Contains([]string{"go", "subagent", "fanout"}, s.Kind):
		return bad("kind is go, subagent or fanout")
	case !slices.Contains([]string{"plan", "evidence", "draft", "committee", "free"}, s.Class):
		return bad("class is plan, evidence, draft, committee or free")
	case e.in.Strict("in") != "" || e.out.Strict("out") != "":
		return bad("schema not strict-shaped: %s%s", e.in.Strict("in"), e.out.Strict("out"))
	case len(sentenceRe.FindAllString(exampleRe.ReplaceAllString(s.Description, ""), -1)) < 3:
		return bad("the description needs at least three sentences")
	case !strings.Contains(strings.ToLower(s.Description), "do not use"):
		return bad("the description must say when not to use it (a \"Do not use\" line)")
	case len(examples) < 1 || len(examples) > 3:
		return bad("the description needs 1–3 \"Example: {…}\" lines")
	case s.MaxTurns < 0 || s.Kind == "subagent" && (s.MaxTurns < 1 || s.Role != model.Specialist && s.Role != model.Critic):
		return bad("a subagent needs a turn cap and the specialist or critic role")
	}
	for _, ex := range examples {
		if err := e.in.Validate(json.RawMessage(ex[1])); err != nil {
			return bad("example %s: %v", ex[1], err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range s.Allowed {
		if x, ok := r.tools[a]; !ok || x.spec.Kind != "go" {
			return bad("allowed tool %q is not a registered Go tool (subagents cannot call subagents)", a)
		}
	}
	switch {
	case r.tools[s.Name] != nil:
		return bad("a tool with this name is already registered")
	case len(r.tools) >= MaxTools:
		return bad("the registry holds its maximum of %d tools", MaxTools)
	}
	// Frozen copies: later changes to the caller's Spec reach nothing. The model is not sent
	// string-length bounds (strict mode refuses them); Validate still enforces them.
	var params bytes.Buffer
	_ = json.Compact(&params, s.In)
	e.def = model.Tool{Name: s.Name, Description: s.Description, Parameters: lengthKw.ReplaceAll(params.Bytes(), nil), Strict: true}
	e.spec.In, e.spec.Out, e.spec.Allowed, e.spec.Solo = slices.Clone(s.In), slices.Clone(s.Out), slices.Clone(s.Allowed), slices.Clone(s.Solo)
	r.tools[s.Name] = e
	return nil
}

func (r *Registry) get(name string) *entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Spec returns a registered tool's frozen spec.
func (r *Registry) Spec(name string) (Spec, bool) {
	if e := r.get(name); e != nil {
		return e.spec, true
	}
	return Spec{}, false
}

// Names lists the registered tools, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Defs returns the frozen definitions of names, in that order: the same bytes on every turn.
func (r *Registry) Defs(names []string) ([]model.Tool, error) {
	if len(names) > MaxVisible {
		return nil, fmt.Errorf("agent: %d visible tools; at most %d", len(names), MaxVisible)
	}
	out := make([]model.Tool, 0, len(names))
	for _, n := range names {
		e := r.get(n)
		if e == nil {
			return nil, fmt.Errorf("agent: visible tool %q is not registered", n)
		}
		out = append(out, e.def)
	}
	return out, nil
}
