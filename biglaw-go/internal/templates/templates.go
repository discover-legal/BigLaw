// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package templates

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Store is a thread-safe registry of TaskTemplates that can be populated from
// one or more directories of JSON files.
type Store struct {
	mu        sync.RWMutex
	templates []types.TaskTemplate
}

// NewStore returns an empty Store ready for use.
func NewStore() *Store {
	return &Store{}
}

// Load walks each supplied directory and reads every *.json file it finds.
// Each file may contain a single TaskTemplate object or a JSON array of them.
// Files that cannot be read or parsed are skipped silently.
// An error is returned only if a directory cannot be opened at all.
func (s *Store) Load(dirs ...string) error {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}

			fpath := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(fpath)
			if err != nil {
				continue
			}

			// Try array first, then single object.
			var many []templateFile
			if err := json.Unmarshal(data, &many); err == nil {
				for _, t := range many {
					s.addLoaded(fpath, t)
				}
				continue
			}

			var one templateFile
			if err := json.Unmarshal(data, &one); err == nil {
				s.addLoaded(fpath, one)
			} else {
				slog.Warn("templates: skipping unparseable template file", "file", fpath, "err", err)
			}
		}
	}
	return nil
}

// templateFile is the on-disk template schema. The canonical prompt field is
// promptTemplate (the same field adapter workflows use);
// taskDescriptionTemplate is accepted as a legacy alias so older template
// files keep working. preferredDomains / metadata / source are informational
// and ignored by the loader.
type templateFile struct {
	types.TaskTemplate
	TaskDescriptionTemplate string `json:"taskDescriptionTemplate"`
}

// normalize resolves the legacy alias into PromptTemplate.
func (f templateFile) normalize() types.TaskTemplate {
	t := f.TaskTemplate
	if strings.TrimSpace(t.PromptTemplate) == "" {
		t.PromptTemplate = f.TaskDescriptionTemplate
	}
	return t
}

// addLoaded adds a template read from disk, skipping (with a warning) any
// that would load with no id or an empty prompt — previously such a
// template loaded silently and produced empty task descriptions.
func (s *Store) addLoaded(fpath string, f templateFile) {
	t := f.normalize()
	if t.ID == "" || strings.TrimSpace(t.PromptTemplate) == "" {
		slog.Warn("templates: skipping template with no id or empty promptTemplate", "file", fpath, "id", t.ID)
		return
	}
	s.Add(t)
}

// Add appends a single TaskTemplate to the store under a write lock.
func (s *Store) Add(t types.TaskTemplate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, t)
}

// Replace swaps the entire template set under a write lock. Used by the
// flavour filter after all sources (files, Lavern, plugins) have loaded.
func (s *Store) Replace(ts []types.TaskTemplate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = ts
}

// List returns a shallow copy of all stored templates under a read lock.
func (s *Store) List() []types.TaskTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.TaskTemplate, len(s.templates))
	copy(out, s.templates)
	return out
}

// Get returns a pointer to the first template whose ID matches id, or nil if
// none is found. The search is performed under a read lock.
func (s *Store) Get(id string) *types.TaskTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.templates {
		if s.templates[i].ID == id {
			t := s.templates[i]
			return &t
		}
	}
	return nil
}

// placeholderRe matches a {{key}} placeholder.
var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)

// InstantiateTemplate substitutes {{key}} placeholders in t.PromptTemplate,
// returning the rendered description, the workflow type string, and the
// keys of any placeholders left unfilled (sorted, deduplicated). Values come
// from the template's own default Substitutions, overridden by subs
// (callers pass only what they want to change). Keys with no corresponding
// placeholder are ignored. A non-empty missing list means the description
// still contains literal placeholders and must not be submitted as a task.
func InstantiateTemplate(t types.TaskTemplate, subs map[string]string) (description, workflowType string, missing []string) {
	merged := make(map[string]string, len(t.Substitutions)+len(subs))
	for k, v := range t.Substitutions {
		merged[k] = v
	}
	for k, v := range subs {
		merged[k] = v
	}
	seen := map[string]bool{}
	result := placeholderRe.ReplaceAllStringFunc(t.PromptTemplate, func(m string) string {
		key := placeholderRe.FindStringSubmatch(m)[1]
		if val, ok := merged[key]; ok {
			return val
		}
		if !seen[key] {
			seen[key] = true
			missing = append(missing, key)
		}
		return m
	})
	sort.Strings(missing)
	return result, string(t.WorkflowType), missing
}
