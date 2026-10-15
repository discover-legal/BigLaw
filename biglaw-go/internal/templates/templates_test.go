// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// shippedDir is the repo's templates/ directory (this package lives at
// biglaw-go/internal/templates).
const shippedDir = "../../../templates"

var placeholder = regexp.MustCompile(`{{\s*\w+\s*}}`)

// Every shipped template must load with a non-empty prompt and instantiate
// with its own default substitutions into a complete task description (no
// unresolved {{placeholders}}). Before the loader fix, 7 templates written
// with taskDescriptionTemplate loaded with an EMPTY prompt.
func TestShippedTemplatesHavePromptsAndInstantiate(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(shippedDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped templates found in %s: %v", shippedDir, err)
	}
	s := NewStore()
	if err := s.Load(shippedDir); err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := s.List()
	if len(loaded) != len(files) {
		t.Fatalf("loaded %d templates from %d files — a template was skipped as invalid", len(loaded), len(files))
	}
	seen := map[string]bool{}
	for _, tpl := range loaded {
		if seen[tpl.ID] {
			t.Errorf("duplicate template id %q", tpl.ID)
		}
		seen[tpl.ID] = true
		if strings.TrimSpace(tpl.PromptTemplate) == "" {
			t.Errorf("%s: empty promptTemplate", tpl.ID)
			continue
		}
		if tpl.WorkflowType == "" {
			t.Errorf("%s: no workflowType", tpl.ID)
		}
		desc, wf := InstantiateTemplate(tpl, nil)
		if strings.TrimSpace(desc) == "" || wf == "" {
			t.Errorf("%s: instantiated to empty description/workflow", tpl.ID)
		}
		if left := placeholder.FindAllString(desc, -1); len(left) > 0 {
			t.Errorf("%s: unresolved placeholders after default substitutions: %v", tpl.ID, left)
		}
	}
}

// The canonical field is promptTemplate; shipped files must use it (the
// legacy alias is for third-party/older files only).
func TestShippedTemplatesUseCanonicalField(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(shippedDir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"taskDescriptionTemplate"`) {
			t.Errorf("%s uses the legacy taskDescriptionTemplate field; use promptTemplate", filepath.Base(f))
		}
	}
}

func TestLoaderAcceptsLegacyAliasAndSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("legacy.json", `{"id":"legacy","workflowType":"review","taskDescriptionTemplate":"Review {{thing}}.","substitutions":{"thing":"the contract"},"preferredDomains":["review"],"metadata":{"x":1}}`)
	write("both.json", `{"id":"both","workflowType":"review","promptTemplate":"Canonical wins.","taskDescriptionTemplate":"ignored"}`)
	write("empty.json", `{"id":"empty","workflowType":"review"}`)
	write("array.json", `[{"id":"a1","workflowType":"roundtable","promptTemplate":"One."},{"id":"","promptTemplate":"no id"}]`)
	write("broken.json", `{not json`)

	s := NewStore()
	if err := s.Load(dir); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get("legacy"); got == nil || got.PromptTemplate != "Review {{thing}}." {
		t.Fatalf("legacy alias not loaded: %+v", got)
	}
	if got := s.Get("both"); got == nil || got.PromptTemplate != "Canonical wins." {
		t.Fatalf("promptTemplate should win over the alias: %+v", got)
	}
	if s.Get("empty") != nil {
		t.Fatal("template with no prompt was loaded")
	}
	if s.Get("a1") == nil || len(s.List()) != 3 {
		t.Fatalf("unexpected set: %+v", s.List())
	}
}

func TestInstantiateMergesDefaultsWithOverrides(t *testing.T) {
	tpl := types.TaskTemplate{
		PromptTemplate: "Review {{doc}} under {{law}}.",
		WorkflowType:   types.WorkflowReview,
		Substitutions:  map[string]string{"doc": "the contract", "law": "English law"},
	}
	desc, wf := InstantiateTemplate(tpl, map[string]string{"law": "New York law", "unused": "x"})
	if desc != "Review the contract under New York law." || wf != string(types.WorkflowReview) {
		t.Fatalf("got %q / %q", desc, wf)
	}
	if tpl.Substitutions["law"] != "English law" {
		t.Fatal("instantiation mutated the template's defaults")
	}
}
