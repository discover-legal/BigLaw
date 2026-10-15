// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package modules is the feature-module registry: the slim always-on core
// (orchestrator, agents, DyTopo, documents, REST) stays unconditional, and
// every optional subsystem registers here as a named module with a default
// derived from its config. Operators flip any module with a single env var:
//
//	BIGLAW_MODULE_<NAME>=on|off      (e.g. BIGLAW_MODULE_BILLING=off)
//
// The env override always wins over the config-derived default. The registry
// is consulted at wiring time (cmd/biglaw/main.go and the API route table),
// and GET /modules reports every module's state and why.
package modules

import (
	"os"
	"strings"
	"sync"
)

// Status is one module's resolved state.
type Status struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Reason      string `json:"reason"` // what decided the state
}

// Registry resolves and reports module states. Safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	mods  map[string]Status
	order []string
}

// Default is the process-wide registry, populated at boot (same pattern as
// audit.Default / cost.Default). Unregistered names resolve to enabled so
// core paths and tests never need registration.
var Default = NewRegistry()

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{mods: map[string]Status{}}
}

// Register records a module with its config-derived default and resolves the
// BIGLAW_MODULE_<NAME> env override. defaultReason documents what produced
// defaultOn (e.g. "LPM_ENABLED", "requires INTAKE_HMAC_SECRET"). Returns the
// resolved enabled state.
func (r *Registry) Register(name, description string, defaultOn bool, defaultReason string) bool {
	enabled, reason := defaultOn, defaultReason
	if reason == "" {
		reason = "default"
	}
	envKey := "BIGLAW_MODULE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envKey))) {
	case "on", "true", "1", "yes":
		enabled, reason = true, envKey+"=on"
	case "off", "false", "0", "no":
		enabled, reason = false, envKey+"=off"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.mods[name]; !seen {
		r.order = append(r.order, name)
	}
	r.mods[name] = Status{Name: name, Description: description, Enabled: enabled, Reason: reason}
	return enabled
}

// Enabled reports a module's state. Unregistered modules are enabled — the
// registry constrains only what explicitly opted into being optional.
func (r *Registry) Enabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.mods[name]
	if !ok {
		return true
	}
	return s.Enabled
}

// List returns every registered module in registration order.
func (r *Registry) List() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Status, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.mods[name])
	}
	return out
}

// Reset clears the registry (tests only).
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mods = map[string]Status{}
	r.order = nil
}
