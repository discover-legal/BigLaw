// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Plugin adapter system — loads external legal tools from JSON files in adapters/external/.
// JSON plugins require zero code; just drop a file with the schema below.
// Lavern adapter converts Lavern agent/workflow JSON to internal types.

package adapters

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── JSON plugin format ───────────────────────────────────────────────────────

// PluginAuth describes how to authenticate the plugin's MCP server.
type PluginAuth struct {
	Type           string `json:"type"` // "api-key" | "none"
	APIKeyEnvVar   string `json:"apiKeyEnvVar,omitempty"`
	EndpointEnvVar string `json:"endpointEnvVar,omitempty"`
}

// PluginToolDef is a tool provided by an external plugin.
type PluginToolDef struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	RemoteName   string                 `json:"remoteName,omitempty"`
	RequiresAuth bool                   `json:"requiresAuth"`
}

// PluginAgentDef is an agent contributed by a plugin.
type PluginAgentDef struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Tier          int      `json:"tier"`
	Domain        string   `json:"domain"`
	Description   string   `json:"description"`
	SystemPrompt  string   `json:"systemPrompt"`
	AllowedTools  []string `json:"allowedTools"`
	Jurisdictions []string `json:"jurisdictions,omitempty"`
}

// PluginWorkflowDef is a workflow contributed by a plugin.
type PluginWorkflowDef struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	WorkflowType   string `json:"workflowType"`
	PromptTemplate string `json:"promptTemplate"`
}

// LegalPlugin is a JSON-only external tool integration.
type LegalPlugin struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description"`
	Auth        PluginAuth          `json:"auth"`
	Tools       []PluginToolDef     `json:"tools"`
	Agents      []PluginAgentDef    `json:"agents"`
	Workflows   []PluginWorkflowDef `json:"workflows"`
}

// ResolvedPlugin is a validated plugin with runtime API key and endpoint.
type ResolvedPlugin struct {
	Plugin   LegalPlugin
	APIKey   string
	Endpoint string
	Enabled  bool
}

// ─── Registry ─────────────────────────────────────────────────────────────────

// Registry holds all loaded external plugins.
type Registry struct {
	mu      sync.RWMutex
	plugins []*ResolvedPlugin
}

// New creates an empty plugin Registry.
func New() *Registry {
	return &Registry{}
}

// LoadDirectory loads all .json files from dir as LegalPlugin descriptors.
func (r *Registry) LoadDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("Plugin adapter directory not found — skipping", "dir", dir)
			return nil
		}
		return err
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		// Skip the example file
		if strings.HasPrefix(entry.Name(), "example") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("Plugin adapter: failed to read file", "file", entry.Name(), "error", err)
			continue
		}
		var plugin LegalPlugin
		if err := json.Unmarshal(data, &plugin); err != nil {
			slog.Warn("Plugin adapter: failed to parse file", "file", entry.Name(), "error", err)
			continue
		}
		if err := r.Register(plugin); err != nil {
			slog.Warn("Plugin adapter: invalid plugin", "file", entry.Name(), "error", err)
			continue
		}
		loaded++
	}
	if loaded > 0 {
		slog.Info("Plugin adapters loaded", "count", loaded, "dir", dir)
	}
	return nil
}

// Register validates and adds a plugin to the registry.
func (r *Registry) Register(plugin LegalPlugin) error {
	if plugin.ID == "" {
		return fmt.Errorf("plugin missing id")
	}
	if plugin.Name == "" {
		return fmt.Errorf("plugin %q missing name", plugin.ID)
	}

	apiKey := ""
	endpoint := ""
	enabled := true

	if plugin.Auth.Type == "api-key" {
		if plugin.Auth.APIKeyEnvVar != "" {
			apiKey = os.Getenv(plugin.Auth.APIKeyEnvVar)
		}
		if plugin.Auth.EndpointEnvVar != "" {
			endpoint = os.Getenv(plugin.Auth.EndpointEnvVar)
		}
		if apiKey == "" {
			enabled = false
		}
	}

	resolved := &ResolvedPlugin{
		Plugin:   plugin,
		APIKey:   apiKey,
		Endpoint: endpoint,
		Enabled:  enabled,
	}

	r.mu.Lock()
	r.plugins = append(r.plugins, resolved)
	r.mu.Unlock()

	slog.Info("Plugin registered", "id", plugin.ID, "name", plugin.Name, "enabled", enabled)
	return nil
}

// List returns all resolved plugins.
func (r *Registry) List() []*ResolvedPlugin {
	r.mu.RLock()
	out := make([]*ResolvedPlugin, len(r.plugins))
	copy(out, r.plugins)
	r.mu.RUnlock()
	return out
}

// AgentDefinitions extracts all agent definitions from all enabled plugins.
func (r *Registry) AgentDefinitions() []types.AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var defs []types.AgentDefinition
	for _, p := range r.plugins {
		if !p.Enabled {
			continue
		}
		for _, a := range p.Plugin.Agents {
			if a.ID == "" || a.Name == "" {
				continue
			}
			tier := types.AgentTier(a.Tier)
			domain := types.DomainResearch
			switch strings.ToLower(a.Domain) {
			case "analysis":
				domain = types.DomainAnalysis
			case "drafting":
				domain = types.DomainDrafting
			case "compliance":
				domain = types.DomainCompliance
			case "tool":
				domain = types.DomainTool
			}
			defs = append(defs, types.AgentDefinition{
				ID:            p.Plugin.ID + ":" + a.ID,
				Name:          a.Name,
				Tier:          tier,
				Type:          types.AgentTypeSpecialist,
				Domain:        domain,
				Description:   a.Description,
				SystemPrompt:  a.SystemPrompt,
				AllowedTools:  a.AllowedTools,
				Jurisdictions: a.Jurisdictions,
			})
		}
	}
	return defs
}

// TaskTemplates extracts workflow templates from all enabled plugins.
func (r *Registry) TaskTemplates() []types.TaskTemplate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var templates []types.TaskTemplate
	for _, p := range r.plugins {
		if !p.Enabled {
			continue
		}
		for _, w := range p.Plugin.Workflows {
			if w.ID == "" || w.PromptTemplate == "" {
				continue
			}
			wt := types.WorkflowRoundtable
			switch strings.ToLower(w.WorkflowType) {
			case "full_bench", "full-bench":
				wt = types.WorkflowFullBench
			case "review":
				wt = types.WorkflowReview
			case "tabulate":
				wt = types.WorkflowTabulate
			case "adversarial":
				wt = types.WorkflowAdversarial
			}
			templates = append(templates, types.TaskTemplate{
				ID:             p.Plugin.ID + ":" + w.ID,
				Name:           w.Name,
				Description:    w.Description,
				WorkflowType:   wt,
				PromptTemplate: w.PromptTemplate,
			})
		}
	}
	return templates
}

// ─── Lavern adapter ───────────────────────────────────────────────────────────

// LavernAgent is the JSON format of a Lavern agent config.
type LavernAgent struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Role          string   `json:"role"` // "orchestrator" | "specialist" | "tool-only"
	Specialties   []string `json:"specialties"`
	SystemPrompt  string   `json:"systemPrompt"`
	AllowedTools  []string `json:"allowedTools"`
	Jurisdictions []string `json:"jurisdictions,omitempty"`
	BillingRate   *float64 `json:"billingRate,omitempty"`
}

// LavernWorkflow is the JSON format of a Lavern workflow config.
type LavernWorkflow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	WorkflowType   string `json:"workflowType"` // "adversarial" | "counsel" | "full-bench" | etc.
	PromptTemplate string `json:"promptTemplate"`
}

// LoadLavernAgents reads all Lavern agent JSON files from dir.
func LoadLavernAgents(dir string) ([]types.AgentDefinition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var defs []types.AgentDefinition
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("LavernAdapter: failed to read", "file", entry.Name(), "error", err)
			continue
		}
		// Try as array first, then single
		var agentsArr []LavernAgent
		if err := json.Unmarshal(data, &agentsArr); err != nil {
			var single LavernAgent
			if err2 := json.Unmarshal(data, &single); err2 != nil {
				slog.Warn("LavernAdapter: failed to parse", "file", entry.Name(), "error", err)
				continue
			}
			agentsArr = []LavernAgent{single}
		}
		for _, a := range agentsArr {
			if a.ID == "" {
				continue
			}
			defs = append(defs, lavernToAgentDef(a))
		}
	}
	if len(defs) > 0 {
		slog.Info("Lavern agents loaded", "count", len(defs), "dir", dir)
	}
	return defs, nil
}

// LoadLavernWorkflows reads all Lavern workflow JSON files from dir.
func LoadLavernWorkflows(dir string) ([]types.TaskTemplate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var templates []types.TaskTemplate
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("LavernWorkflowAdapter: failed to read", "file", entry.Name(), "error", err)
			continue
		}
		var wfs []LavernWorkflow
		if err := json.Unmarshal(data, &wfs); err != nil {
			var single LavernWorkflow
			if err2 := json.Unmarshal(data, &single); err2 != nil {
				slog.Warn("LavernWorkflowAdapter: failed to parse", "file", entry.Name(), "error", err)
				continue
			}
			wfs = []LavernWorkflow{single}
		}
		for _, w := range wfs {
			if w.ID == "" || w.PromptTemplate == "" {
				continue
			}
			templates = append(templates, lavernWorkflowToTemplate(w))
		}
	}
	if len(templates) > 0 {
		slog.Info("Lavern workflows loaded", "count", len(templates), "dir", dir)
	}
	return templates, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func lavernToAgentDef(a LavernAgent) types.AgentDefinition {
	tier := types.TierSpecialist
	agentType := types.AgentTypeSpecialist
	switch strings.ToLower(a.Role) {
	case "orchestrator":
		tier = types.TierManager
		agentType = types.AgentTypeManager
	case "tool-only", "tool_only":
		tier = types.TierTool
		agentType = types.AgentTypeTool
	}

	domain := inferDomain(a.Role, a.Specialties)

	return types.AgentDefinition{
		ID:            a.ID,
		Name:          a.Name,
		Tier:          tier,
		Type:          agentType,
		Domain:        domain,
		Description:   strings.Join(a.Specialties, ", "),
		SystemPrompt:  a.SystemPrompt,
		AllowedTools:  a.AllowedTools,
		Jurisdictions: a.Jurisdictions,
		BillingRate:   a.BillingRate,
	}
}

func inferDomain(role string, specialties []string) types.AgentDomain {
	combined := strings.ToLower(role + " " + strings.Join(specialties, " "))
	switch {
	case strings.Contains(combined, "draft") || strings.Contains(combined, "writ") || strings.Contains(combined, "document"):
		return types.DomainDrafting
	case strings.Contains(combined, "comply") || strings.Contains(combined, "regulat") || strings.Contains(combined, "compliance"):
		return types.DomainCompliance
	case strings.Contains(combined, "analys") || strings.Contains(combined, "review"):
		return types.DomainAnalysis
	case strings.Contains(combined, "tool") || strings.Contains(combined, "search") || strings.Contains(combined, "retriev"):
		return types.DomainTool
	default:
		return types.DomainResearch
	}
}

func lavernWorkflowToTemplate(w LavernWorkflow) types.TaskTemplate {
	return types.TaskTemplate{
		ID:             w.ID,
		Name:           w.Name,
		Description:    w.Description,
		WorkflowType:   mapWorkflowType(w.WorkflowType),
		PromptTemplate: w.PromptTemplate,
	}
}

func mapWorkflowType(s string) types.WorkflowType {
	switch strings.ToLower(strings.ReplaceAll(s, "-", "_")) {
	case "full_bench":
		return types.WorkflowFullBench
	case "review":
		return types.WorkflowReview
	case "tabulate":
		return types.WorkflowTabulate
	case "adversarial":
		return types.WorkflowAdversarial
	case "verification":
		return types.WorkflowType("verification")
	default:
		return types.WorkflowRoundtable
	}
}

// rePromptMarker matches the structural markers used by the agent-output
// parsers (findings, debate challenges/resolutions, round-goal sections).
// Case-insensitive to mirror the (?i) parsers that consume them.
var rePromptMarker = regexp.MustCompile(
	`(?i)\b(?:FINDING:|END_FINDING\b|NO_FINDINGS\b|NO_CHALLENGE\b|CHALLENGE:|END_CHALLENGE\b|RESOLUTION:|DESCRIPTION:|EXPECTED_OUTPUT_\d+:)`)

// SanitizePromptContent strips prompt injection markers and ASCII control
// characters (except tab/newline) from user content before it is interpolated
// into a model prompt. Markers are neutralised by bracket-wrapping so the
// downstream output parsers treat them as inert text.
func SanitizePromptContent(s string) string {
	s = rePromptMarker.ReplaceAllStringFunc(s, func(m string) string {
		return "[" + m + "]"
	})
	if strings.IndexFunc(s, isPromptControlRune) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isPromptControlRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isPromptControlRune reports whether r is an ASCII control character that
// must not survive into prompts: 0x00–0x08, 0x0b–0x1f, 0x7f (tab and newline
// are kept).
func isPromptControlRune(r rune) bool {
	return r <= 0x08 || (r >= 0x0b && r <= 0x1f) || r == 0x7f
}

// InstantiateTemplate substitutes {{key}} placeholders in a prompt template.
func InstantiateTemplate(template string, subs map[string]string) string {
	result := template
	for k, v := range subs {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}
