// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// MCP (Model Context Protocol) stdio server for BigLaw.
// Exposes the orchestrator, knowledge store, and agent registry as MCP tools
// so that Claude Code and other MCP clients can drive BigLaw directly.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// MCPServer wraps the BigLaw subsystems and exposes them as MCP tools.
type MCPServer struct {
	orch      *orchestrator.Orchestrator
	knowledge *knowledge.Store
	registry  *agents.Registry
	plugins   *adapters.Registry
	time      *timekeeping.TimeStore
	srv       *server.MCPServer
}

// New creates a new MCPServer. Call Serve() to start the stdio loop.
func New(
	orch *orchestrator.Orchestrator,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	plugins *adapters.Registry,
	timeStore *timekeeping.TimeStore,
) *MCPServer {
	s := &MCPServer{
		orch:      orch,
		knowledge: knowledgeStore,
		registry:  registry,
		plugins:   plugins,
		time:      timeStore,
	}

	s.srv = server.NewMCPServer(
		"biglaw",
		"0.5.0",
		server.WithToolCapabilities(false),
		server.WithInstructions(
			"BigLaw multi-agent legal AI platform. "+
				"Use submit_task to start a research or drafting task, "+
				"get_task to poll status, and approve_gate / reject_gate to handle human review gates.",
		),
	)

	s.registerTools()
	return s
}

// Serve starts the MCP stdio server and blocks until stdin closes or a signal
// is received.
func (s *MCPServer) Serve() error {
	return server.ServeStdio(s.srv)
}

// ─── Tool registration ────────────────────────────────────────────────────────

func (s *MCPServer) registerTools() {
	// 1. submit_task
	s.srv.AddTool(
		mcplib.NewTool("submit_task",
			mcplib.WithDescription("Start a multi-agent legal task. Returns the new Task object including its ID."),
			mcplib.WithString("description",
				mcplib.Required(),
				mcplib.Description("Full description of the legal task to research or draft."),
				mcplib.MaxLength(20_000),
			),
			mcplib.WithString("workflowType",
				mcplib.Required(),
				mcplib.Description("Workflow type: counsel | roundtable | adversarial | review | tabulate | full_bench | legal_design | pre_engagement"),
				mcplib.Enum(
					string(types.WorkflowCounsel),
					string(types.WorkflowRoundtable),
					string(types.WorkflowAdversarial),
					string(types.WorkflowReview),
					string(types.WorkflowTabulate),
					string(types.WorkflowFullBench),
					string(types.WorkflowLegalDesign),
					string(types.WorkflowPreEngagement),
				),
			),
			mcplib.WithString("jurisdiction",
				mcplib.Description("BCP-47-style jurisdiction tag (e.g. US, US-NY, EU, UK, AU). Filters jurisdiction-specific agents."),
			),
			mcplib.WithString("clientNumber",
				mcplib.Description("Client number to associate with this task."),
			),
			mcplib.WithString("matterNumber",
				mcplib.Description("Matter number to associate with this task."),
			),
		),
		s.handleSubmitTask,
	)

	// 2. get_task
	s.srv.AddTool(
		mcplib.NewTool("get_task",
			mcplib.WithDescription("Poll the status and findings of a task by its ID."),
			mcplib.WithString("taskId",
				mcplib.Required(),
				mcplib.Description("The task ID returned by submit_task."),
			),
		),
		s.handleGetTask,
	)

	// 3. list_tasks
	s.srv.AddTool(
		mcplib.NewTool("list_tasks",
			mcplib.WithDescription("List all tasks (summary view, no findings content)."),
		),
		s.handleListTasks,
	)

	// 4. approve_gate
	s.srv.AddTool(
		mcplib.NewTool("approve_gate",
			mcplib.WithDescription("Approve a human-review gate on a task finding, allowing the task to proceed."),
			mcplib.WithString("taskId",
				mcplib.Required(),
				mcplib.Description("The task ID."),
			),
			mcplib.WithString("gateId",
				mcplib.Required(),
				mcplib.Description("The gate ID from task.pendingGates[].id."),
			),
			mcplib.WithString("note",
				mcplib.Description("Optional reviewer note."),
			),
		),
		s.handleApproveGate,
	)

	// 5. reject_gate
	s.srv.AddTool(
		mcplib.NewTool("reject_gate",
			mcplib.WithDescription("Reject a human-review gate, removing the associated finding from the task."),
			mcplib.WithString("taskId",
				mcplib.Required(),
				mcplib.Description("The task ID."),
			),
			mcplib.WithString("gateId",
				mcplib.Required(),
				mcplib.Description("The gate ID from task.pendingGates[].id."),
			),
			mcplib.WithString("reason",
				mcplib.Required(),
				mcplib.Description("Reason for rejection."),
			),
		),
		s.handleRejectGate,
	)

	// 6. submit_from_template
	s.srv.AddTool(
		mcplib.NewTool("submit_from_template",
			mcplib.WithDescription("Run a pre-built workflow template. Use list_templates to discover available template IDs."),
			mcplib.WithString("templateId",
				mcplib.Required(),
				mcplib.Description("Template ID from list_templates."),
			),
			mcplib.WithString("substitutions",
				mcplib.Description("JSON object of template variable substitutions, e.g. {\"company\":\"Acme\",\"issue\":\"merger\"}. Keys match the template's substitutions map."),
			),
			mcplib.WithString("documentIds",
				mcplib.Description("JSON array of document IDs to attach, e.g. [\"doc-uuid-1\",\"doc-uuid-2\"]."),
			),
		),
		s.handleSubmitFromTemplate,
	)

	// 7. list_templates
	s.srv.AddTool(
		mcplib.NewTool("list_templates",
			mcplib.WithDescription("List all available workflow templates."),
		),
		s.handleListTemplates,
	)

	// 8. get_round
	s.srv.AddTool(
		mcplib.NewTool("get_round",
			mcplib.WithDescription("Inspect the state of a specific DyTopo round within a task."),
			mcplib.WithString("taskId",
				mcplib.Required(),
				mcplib.Description("The task ID."),
			),
			mcplib.WithNumber("round",
				mcplib.Required(),
				mcplib.Description("Round number (1-based)."),
				mcplib.Min(1),
			),
		),
		s.handleGetRound,
	)

	// 9. ingest_document
	s.srv.AddTool(
		mcplib.NewTool("ingest_document",
			mcplib.WithDescription("Add a document to the knowledge store for semantic search and task attachment."),
			mcplib.WithString("title",
				mcplib.Required(),
				mcplib.Description("Document title."),
			),
			mcplib.WithString("content",
				mcplib.Required(),
				mcplib.Description("Full text content of the document."),
			),
			mcplib.WithString("ownerID",
				mcplib.Description("Profile ID of the owning lawyer (scopes visibility)."),
			),
		),
		s.handleIngestDocument,
	)

	// 10. search_knowledge
	s.srv.AddTool(
		mcplib.NewTool("search_knowledge",
			mcplib.WithDescription("Semantic search across ingested documents."),
			mcplib.WithString("query",
				mcplib.Required(),
				mcplib.Description("Natural-language search query."),
			),
			mcplib.WithNumber("topK",
				mcplib.Description("Maximum number of results to return (default 5)."),
				mcplib.Min(1),
				mcplib.Max(50),
			),
		),
		s.handleSearchKnowledge,
	)

	// 11. list_agents
	s.srv.AddTool(
		mcplib.NewTool("list_agents",
			mcplib.WithDescription("Browse the full agent registry (tier, domain, description, jurisdictions)."),
		),
		s.handleListAgents,
	)

	// 12. query_memory
	s.srv.AddTool(
		mcplib.NewTool("query_memory",
			mcplib.WithDescription("Return recent inter-round memory entries from the audit log as a proxy for the memory store."),
		),
		s.handleQueryMemory,
	)

	// 13. get_audit
	s.srv.AddTool(
		mcplib.NewTool("get_audit",
			mcplib.WithDescription("Retrieve recent entries from the append-only audit log."),
			mcplib.WithNumber("limit",
				mcplib.Description("Maximum number of entries to return (default 50, max 500)."),
				mcplib.Min(1),
				mcplib.Max(500),
			),
			mcplib.WithString("taskId",
				mcplib.Description("If set, filter entries to this task ID only."),
			),
		),
		s.handleGetAudit,
	)

	// 14. list_plugins
	s.srv.AddTool(
		mcplib.NewTool("list_plugins",
			mcplib.WithDescription("List all loaded external plugins (JSON drop-in adapters), including counts of their contributed tools, agents, and workflow templates."),
		),
		s.handleListPlugins,
	)

	// 15. get_time_entries
	s.srv.AddTool(
		mcplib.NewTool("get_time_entries",
			mcplib.WithDescription("Retrieve billable time entries. MCP runs with full (partner) access. Supports filtering by profileId, taskId, matterNumber, and an ISO date range."),
			mcplib.WithString("profileId",
				mcplib.Description("If set, filter entries to this lawyer profile ID."),
			),
			mcplib.WithString("taskId",
				mcplib.Description("If set, filter entries to this task ID."),
			),
			mcplib.WithString("matterNumber",
				mcplib.Description("If set, filter entries to this matter number."),
			),
			mcplib.WithString("from",
				mcplib.Description("ISO date string — only entries started at or after this time."),
			),
			mcplib.WithString("to",
				mcplib.Description("ISO date string — only entries started at or before this time."),
			),
		),
		s.handleGetTimeEntries,
	)
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (s *MCPServer) handleSubmitTask(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	description := stringArg(args, "description")
	if description == "" {
		return mcplib.NewToolResultError("description is required"), nil
	}

	wfStr := stringArg(args, "workflowType")
	if wfStr == "" {
		return mcplib.NewToolResultError("workflowType is required"), nil
	}

	params := orchestrator.SubmitParams{
		Description:  description,
		WorkflowType: types.WorkflowType(wfStr),
		Jurisdiction: stringArg(args, "jurisdiction"),
		ClientNumber: stringArg(args, "clientNumber"),
		MatterNumber: stringArg(args, "matterNumber"),
	}

	task, err := s.orch.SubmitTask(params)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("submit_task failed: %v", err)), nil
	}

	return jsonResult(task)
}

func (s *MCPServer) handleGetTask(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	taskID := stringArg(req.Params.Arguments, "taskId")
	if taskID == "" {
		return mcplib.NewToolResultError("taskId is required"), nil
	}

	task := s.orch.GetTask(taskID)
	if task == nil {
		return mcplib.NewToolResultError(fmt.Sprintf("task not found: %s", taskID)), nil
	}

	return jsonResult(task)
}

func (s *MCPServer) handleListTasks(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	tasks := s.orch.ListTasks()

	// Return lightweight summaries to keep the response compact.
	type taskSummary struct {
		ID           string             `json:"id"`
		Description  string             `json:"description"`
		WorkflowType types.WorkflowType `json:"workflowType"`
		Status       types.TaskStatus   `json:"status"`
		CurrentPhase types.TaskPhase    `json:"currentPhase"`
		CurrentRound int                `json:"currentRound"`
		Findings     int                `json:"findings"`
		PendingGates int                `json:"pendingGates"`
		CreatedAt    time.Time          `json:"createdAt"`
		UpdatedAt    time.Time          `json:"updatedAt"`
	}

	summaries := make([]taskSummary, len(tasks))
	for i, t := range tasks {
		summaries[i] = taskSummary{
			ID:           t.ID,
			Description:  truncate(t.Description, 200),
			WorkflowType: t.WorkflowType,
			Status:       t.Status,
			CurrentPhase: t.CurrentPhase,
			CurrentRound: t.CurrentRound,
			Findings:     len(t.Findings),
			PendingGates: countPendingGates(t),
			CreatedAt:    t.CreatedAt,
			UpdatedAt:    t.UpdatedAt,
		}
	}

	return jsonResult(summaries)
}

func (s *MCPServer) handleApproveGate(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	taskID := stringArg(args, "taskId")
	gateID := stringArg(args, "gateId")
	if taskID == "" || gateID == "" {
		return mcplib.NewToolResultError("taskId and gateId are required"), nil
	}

	note := stringArg(args, "note")

	if err := s.orch.ApproveGate(taskID, gateID, note, ""); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("approve_gate failed: %v", err)), nil
	}

	return mcplib.NewToolResultText(fmt.Sprintf(`{"ok":true,"taskId":%q,"gateId":%q}`, taskID, gateID)), nil
}

func (s *MCPServer) handleRejectGate(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	taskID := stringArg(args, "taskId")
	gateID := stringArg(args, "gateId")
	reason := stringArg(args, "reason")
	if taskID == "" || gateID == "" || reason == "" {
		return mcplib.NewToolResultError("taskId, gateId and reason are required"), nil
	}

	if err := s.orch.RejectGate(taskID, gateID, reason, ""); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("reject_gate failed: %v", err)), nil
	}

	return mcplib.NewToolResultText(fmt.Sprintf(`{"ok":true,"taskId":%q,"gateId":%q}`, taskID, gateID)), nil
}

func (s *MCPServer) handleSubmitFromTemplate(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	templateID := stringArg(args, "templateId")
	if templateID == "" {
		return mcplib.NewToolResultError("templateId is required"), nil
	}

	// Parse optional substitutions JSON string → map.
	subs := map[string]string{}
	if subsStr := stringArg(args, "substitutions"); subsStr != "" {
		if err := json.Unmarshal([]byte(subsStr), &subs); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("substitutions must be valid JSON object: %v", err)), nil
		}
	}

	// Parse optional documentIds JSON string → slice.
	var docIDs []string
	if docIDsStr := stringArg(args, "documentIds"); docIDsStr != "" {
		if err := json.Unmarshal([]byte(docIDsStr), &docIDs); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("documentIds must be a valid JSON array: %v", err)), nil
		}
	}

	task, err := s.orch.SubmitFromTemplate(templateID, subs, docIDs, orchestrator.SubmitParams{})
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("submit_from_template failed: %v", err)), nil
	}

	return jsonResult(task)
}

func (s *MCPServer) handleListTemplates(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	templates := s.orch.ListTemplates()
	return jsonResult(templates)
}

func (s *MCPServer) handleGetRound(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	taskID := stringArg(args, "taskId")
	if taskID == "" {
		return mcplib.NewToolResultError("taskId is required"), nil
	}

	roundNum := intArg(args, "round")
	if roundNum < 1 {
		return mcplib.NewToolResultError("round must be >= 1"), nil
	}

	task := s.orch.GetTask(taskID)
	if task == nil {
		return mcplib.NewToolResultError(fmt.Sprintf("task not found: %s", taskID)), nil
	}

	// Rounds are 1-based in the protocol but 0-based in the slice.
	idx := roundNum - 1
	if idx >= len(task.Rounds) {
		return mcplib.NewToolResultError(
			fmt.Sprintf("round %d not found; task has %d round(s) so far", roundNum, len(task.Rounds)),
		), nil
	}

	return jsonResult(task.Rounds[idx])
}

func (s *MCPServer) handleIngestDocument(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	title := stringArg(args, "title")
	content := stringArg(args, "content")
	if title == "" || content == "" {
		return mcplib.NewToolResultError("title and content are required"), nil
	}

	doc := types.Document{
		Title:      title,
		Content:    content,
		OwnerID:    stringArg(args, "ownerID"),
		IngestedAt: time.Now(),
	}

	// MCP runs as the trusted local operator (Claude Code) → system identity.
	saved, err := s.knowledge.Ingest(store.WithSystem(context.Background()), doc)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("ingest_document failed: %v", err)), nil
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "document.ingested",
		ActorID: audit.ActorSystem,
		Data: map[string]interface{}{
			"documentId": saved.ID,
			"title":      title,
		},
	})

	return jsonResult(saved)
}

func (s *MCPServer) handleSearchKnowledge(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	query := stringArg(args, "query")
	if query == "" {
		return mcplib.NewToolResultError("query is required"), nil
	}

	topK := intArg(args, "topK")
	if topK <= 0 {
		topK = 5
	}

	results, err := s.knowledge.Search(query, knowledge.SearchOpts{TopK: topK})
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("search_knowledge failed: %v", err)), nil
	}

	return jsonResult(results)
}

func (s *MCPServer) handleListAgents(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	defs := s.registry.ListAll()

	// Strip the system-prompt text to keep the response lean.
	type agentSummary struct {
		ID            string            `json:"id"`
		Name          string            `json:"name"`
		Tier          types.AgentTier   `json:"tier"`
		Type          types.AgentType   `json:"type"`
		Domain        types.AgentDomain `json:"domain"`
		Description   string            `json:"description"`
		AllowedTools  []string          `json:"allowedTools"`
		Skills        []string          `json:"skills"`
		Jurisdictions []string          `json:"jurisdictions,omitempty"`
		SuccessScore  float64           `json:"successScore"`
	}

	summaries := make([]agentSummary, len(defs))
	for i, d := range defs {
		summaries[i] = agentSummary{
			ID:            d.ID,
			Name:          d.Name,
			Tier:          d.Tier,
			Type:          d.Type,
			Domain:        d.Domain,
			Description:   d.Description,
			AllowedTools:  d.AllowedTools,
			Skills:        d.Skills,
			Jurisdictions: d.Jurisdictions,
			SuccessScore:  d.SuccessScore,
		}
	}

	return jsonResult(summaries)
}

func (s *MCPServer) handleQueryMemory(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	// Return recent round-related audit entries as a proxy for inter-round memory.
	entries := audit.Default.ReadRecent("", 100)

	type memoryItem struct {
		ID      string                 `json:"id"`
		TS      string                 `json:"ts"`
		Event   string                 `json:"event"`
		TaskID  string                 `json:"taskId,omitempty"`
		AgentID string                 `json:"agentId,omitempty"`
		Data    map[string]interface{} `json:"data"`
	}

	items := make([]memoryItem, 0, len(entries))
	for _, e := range entries {
		// Surface round-digest and phase events as memory entries.
		switch e.Event {
		case "phase.complete", "phase.start", "task.complete", "task.started", "round.digest":
			items = append(items, memoryItem{
				ID:      e.ID,
				TS:      e.TS,
				Event:   e.Event,
				TaskID:  e.TaskID,
				AgentID: e.AgentID,
				Data:    e.Data,
			})
		}
	}

	return jsonResult(items)
}

func (s *MCPServer) handleGetAudit(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	limit := intArg(args, "limit")
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	taskID := stringArg(args, "taskId")

	entries := audit.Default.ReadRecent(taskID, limit)
	return jsonResult(entries)
}

func (s *MCPServer) handleListPlugins(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	// Summary shape mirrors GET /plugins (ops.go) and the TS pluginRegistry
	// list(): descriptor fields + counts, never API keys or endpoints.
	type pluginSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Version     string `json:"version,omitempty"`
		Description string `json:"description,omitempty"`
		Source      string `json:"source"`
		Enabled     bool   `json:"enabled"`
		Tools       int    `json:"tools"`
		Agents      int    `json:"agents"`
		Workflows   int    `json:"workflows"`
	}

	summaries := []pluginSummary{}
	if s.plugins != nil {
		for _, p := range s.plugins.List() {
			summaries = append(summaries, pluginSummary{
				ID:          p.Plugin.ID,
				Name:        p.Plugin.Name,
				Version:     p.Plugin.Version,
				Description: p.Plugin.Description,
				Source:      "json", // the Go port loads JSON adapters only
				Enabled:     p.Enabled,
				Tools:       len(p.Plugin.Tools),
				Agents:      len(p.Plugin.Agents),
				Workflows:   len(p.Plugin.Workflows),
			})
		}
	}

	return jsonResult(summaries)
}

func (s *MCPServer) handleGetTimeEntries(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.Params.Arguments

	// MCP over stdio runs as the local partner — full visibility, same as the
	// TS get_time_entries tool.
	filter := timekeeping.TimeFilter{
		ProfileID:    stringArg(args, "profileId"),
		TaskID:       stringArg(args, "taskId"),
		MatterNumber: stringArg(args, "matterNumber"),
	}
	if from := stringArg(args, "from"); from != "" {
		t, err := parseISOTime(from)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("invalid from date: %v", err)), nil
		}
		filter.From = &t
	}
	if to := stringArg(args, "to"); to != "" {
		t, err := parseISOTime(to)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("invalid to date: %v", err)), nil
		}
		filter.To = &t
	}

	entries := s.time.List(filter)
	if entries == nil {
		entries = []types.TimeEntry{}
	}
	return jsonResult(entries)
}

// ─── Utility helpers ──────────────────────────────────────────────────────────

// parseISOTime accepts RFC 3339 timestamps or bare ISO dates (YYYY-MM-DD).
func parseISOTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", v)
}

// jsonResult marshals v to JSON and wraps it in a TextContent tool result.
func jsonResult(v interface{}) (*mcplib.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to marshal result: %v", err)), nil
	}
	return mcplib.NewToolResultText(string(data)), nil
}

// stringArg extracts a string argument from the MCP arguments map.
func stringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// intArg extracts an integer argument from the MCP arguments map.
// Handles both float64 (JSON number) and string representations.
func intArg(args map[string]interface{}, key string) int {
	if args == nil {
		return 0
	}
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

// truncate shortens s to at most n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strutil.Truncate(s, n) + "…"
}

// countPendingGates returns the number of gates whose status is "pending".
func countPendingGates(t *types.Task) int {
	n := 0
	for _, g := range t.PendingGates {
		if g.Status == "pending" {
			n++
		}
	}
	return n
}
