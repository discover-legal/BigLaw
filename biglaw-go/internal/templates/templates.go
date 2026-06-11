// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			var many []types.TaskTemplate
			if err := json.Unmarshal(data, &many); err == nil {
				for _, t := range many {
					s.Add(t)
				}
				continue
			}

			var one types.TaskTemplate
			if err := json.Unmarshal(data, &one); err == nil {
				s.Add(one)
			}
		}
	}
	return nil
}

// Add appends a single TaskTemplate to the store under a write lock.
func (s *Store) Add(t types.TaskTemplate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, t)
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

// InstantiateTemplate substitutes {{key}} placeholders in t.PromptTemplate
// with corresponding values from subs, returning the rendered description and
// the workflow type string. Keys present in subs that have no corresponding
// placeholder are ignored; placeholders with no matching key are left as-is.
func InstantiateTemplate(t types.TaskTemplate, subs map[string]string) (description, workflowType string) {
	result := t.PromptTemplate
	for key, val := range subs {
		result = strings.ReplaceAll(result, "{{"+key+"}}", val)
	}
	return result, string(t.WorkflowType)
}
