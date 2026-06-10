// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// BriefingEngine — hub-and-spoke client intelligence swarm.
// Hub (Sonnet) synthesises intel written to a shared Chalkboard by 10+ parallel
// spokes: clio, imanage, slack, google_drive, box, email_graph, email_gmail,
// sharepoint, teams_chat, knowledge_store, internal_tasks, internal_time.
// Slow or unconfigured spokes time out at 12 s and never block the hub.

package briefing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/email"
	"github.com/discover-legal/biglaw-go/internal/integrations"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const spokeSpokeTimeout = 12 * time.Second

// ─── Chalkboard ───────────────────────────────────────────────────────────────

// IntelItem is a single piece of intelligence written by a spoke agent.
type IntelItem struct {
	Source       string                 `json:"source"`
	Category     string                 `json:"category"`
	EventAt      string                 `json:"eventAt,omitempty"`
	MatterNumber string                 `json:"matterNumber,omitempty"`
	Headline     string                 `json:"headline"`
	Data         map[string]interface{} `json:"data"`
}

// Chalkboard is the shared write-board for spoke results.
type Chalkboard struct {
	mu    sync.Mutex
	items []IntelItem
}

func (c *Chalkboard) Write(item IntelItem) {
	c.mu.Lock()
	c.items = append(c.items, item)
	c.mu.Unlock()
}
func (c *Chalkboard) WriteMany(items []IntelItem) {
	c.mu.Lock()
	c.items = append(c.items, items...)
	c.mu.Unlock()
}
func (c *Chalkboard) All() []IntelItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]IntelItem, len(c.items))
	copy(cp, c.items)
	return cp
}
func (c *Chalkboard) BySource(src string) []IntelItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []IntelItem
	for _, i := range c.items {
		if i.Source == src {
			out = append(out, i)
		}
	}
	return out
}
func (c *Chalkboard) ByCategory(cat string) []IntelItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []IntelItem
	for _, i := range c.items {
		if i.Category == cat {
			out = append(out, i)
		}
	}
	return out
}
func (c *Chalkboard) Size() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.items) }

// ─── Spoke result ─────────────────────────────────────────────────────────────

type spokeResult struct {
	source     string
	items      []IntelItem
	durationMs int64
	errMsg     string
}

// ─── KnowledgeSearcher ────────────────────────────────────────────────────────

// KnowledgeSearcher is the subset of the knowledge store the briefing engine needs.
type KnowledgeSearcher interface {
	Search(query string, topK int) []types.SearchResult
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine runs the hub-and-spoke swarm.
type Engine struct {
	provider providers.Provider
	sonnet   string
}

// New creates a BriefingEngine.
func New(provider providers.Provider, sonnetModel string) *Engine {
	return &Engine{provider: provider, sonnet: sonnetModel}
}

// GenerateOpts are optional parameters for a briefing run.
type GenerateOpts struct {
	Knowledge       KnowledgeSearcher
	TaskID          string
	BriefingDate    string
	IndustryContext string
}

// Generate launches all spokes in parallel and synthesises the result.
func (e *Engine) Generate(client *types.Client, allTasks []*types.Task, timeEntries []types.TimeEntry, opts GenerateOpts) (*types.ClientBriefing, error) {
	briefingDate := opts.BriefingDate
	if briefingDate == "" {
		briefingDate = time.Now().UTC().Format("2006-01-02")
	}

	cb := &Chalkboard{}

	// Launch spokes concurrently
	type future struct {
		ch chan spokeResult
	}
	launch := func(fn func() spokeResult) chan spokeResult {
		ch := make(chan spokeResult, 1)
		go func() { ch <- fn() }()
		return ch
	}

	futures := []chan spokeResult{
		launch(func() spokeResult { return e.runEmailSpoke(client) }),
		launch(func() spokeResult { return e.runSharePointSpoke(client) }),
		launch(func() spokeResult { return e.runTeamsChatSpoke(client) }),
		launch(func() spokeResult { return e.runInternalSpoke(client, allTasks, timeEntries) }),
	}
	if opts.Knowledge != nil {
		futures = append(futures, launch(func() spokeResult { return e.runKnowledgeSpoke(client, opts.Knowledge, opts.IndustryContext) }))
	}

	spokeSummary := map[string]map[string]interface{}{}
	deadline := time.After(spokeSpokeTimeout)
	remaining := len(futures)
	for remaining > 0 {
		select {
		case <-deadline:
			remaining = 0
		default:
			for _, ch := range futures {
				select {
				case r := <-ch:
					cb.WriteMany(r.items)
					entry := map[string]interface{}{"items": len(r.items), "durationMs": r.durationMs}
					if r.errMsg != "" {
						entry["error"] = r.errMsg
						slog.Warn("Briefing spoke error", "source", r.source, "error", r.errMsg)
					}
					spokeSummary[r.source] = entry
					remaining--
				default:
				}
			}
			if remaining > 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	matters := e.buildMatterSnapshots(client.Matters, allTasks, timeEntries)
	billing := e.buildBillingSnapshot(timeEntries, client.ClientNumber, matters)
	openItems := e.collectOpenItems(cb, matters)

	execSummary, document := e.synthesise(client, cb, matters, billing, openItems, opts)

	return &types.ClientBriefing{
		ID:                uuid.New().String(),
		ClientID:          client.ID,
		ClientName:        client.Name,
		ClientNumber:      client.ClientNumber,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		BriefingDate:      briefingDate,
		ExecutiveSummary:  execSummary,
		Matters:           matters,
		Billing:           billing,
		OpenItems:         openItems,
		RelationshipNotes: client.Notes,
		IndustryContext:   opts.IndustryContext,
		Document:          document,
	}, nil
}

// ─── Spokes ───────────────────────────────────────────────────────────────────

func (e *Engine) runEmailSpoke(client *types.Client) spokeResult {
	start := time.Now()
	var items []IntelItem

	graphMsgs, graphErr := email.SearchGraphMail(client.Name, 20, 90)
	gmailMsgs, gmailErr := email.SearchGmail(client.Name, 20, 90)

	for _, m := range graphMsgs {
		items = append(items, IntelItem{
			Source:       "email_graph",
			Category:     "email",
			EventAt:      m.ReceivedAt,
			MatterNumber: m.MatterRef,
			Headline:     fmt.Sprintf("%s — from %s", m.Subject, m.From),
			Data:         map[string]interface{}{"id": m.ID, "subject": m.Subject, "from": m.From, "receivedAt": m.ReceivedAt, "snippet": m.Snippet, "hasAttachments": m.HasAttachments},
		})
	}
	for _, m := range gmailMsgs {
		items = append(items, IntelItem{
			Source:       "email_gmail",
			Category:     "email",
			EventAt:      m.ReceivedAt,
			MatterNumber: m.MatterRef,
			Headline:     fmt.Sprintf("%s — from %s", m.Subject, m.From),
			Data:         map[string]interface{}{"id": m.ID, "subject": m.Subject, "from": m.From, "receivedAt": m.ReceivedAt, "snippet": m.Snippet},
		})
	}

	// Sort by date desc
	sort.Slice(items, func(i, j int) bool {
		ti := parseEventTime(items[i].EventAt)
		tj := parseEventTime(items[j].EventAt)
		return ti.After(tj)
	})

	errMsgs := []string{}
	if graphErr != nil {
		errMsgs = append(errMsgs, "Graph: "+graphErr.Error())
	}
	if gmailErr != nil {
		errMsgs = append(errMsgs, "Gmail: "+gmailErr.Error())
	}
	return spokeResult{source: "email_graph", items: items, durationMs: time.Since(start).Milliseconds(), errMsg: strings.Join(errMsgs, "; ")}
}

func (e *Engine) runSharePointSpoke(client *types.Client) spokeResult {
	start := time.Now()
	files, err := integrations.SearchSharePoint(client.Name, 15)
	var items []IntelItem
	for _, f := range files {
		items = append(items, IntelItem{
			Source:       "sharepoint",
			Category:     "document",
			EventAt:      f.LastModified,
			MatterNumber: f.MatterRef,
			Headline:     f.Name,
			Data:         map[string]interface{}{"id": f.ID, "name": f.Name, "webUrl": f.WebURL, "lastModified": f.LastModified},
		})
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	return spokeResult{source: "sharepoint", items: items, durationMs: time.Since(start).Milliseconds(), errMsg: errMsg}
}

func (e *Engine) runTeamsChatSpoke(client *types.Client) spokeResult {
	start := time.Now()
	messages, err := integrations.SearchTeamsMessages(client.Name, 15)
	var items []IntelItem
	for _, m := range messages {
		body := m.Body
		if len(body) > 100 {
			body = body[:100]
		}
		items = append(items, IntelItem{
			Source:       "teams_chat",
			Category:     "correspondence",
			EventAt:      m.CreatedAt,
			MatterNumber: m.MatterRef,
			Headline:     fmt.Sprintf("%s: %s", m.From, body),
			Data:         map[string]interface{}{"id": m.ID, "from": m.From, "body": m.Body, "createdAt": m.CreatedAt, "webUrl": m.WebURL},
		})
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	return spokeResult{source: "teams_chat", items: items, durationMs: time.Since(start).Milliseconds(), errMsg: errMsg}
}

func (e *Engine) runKnowledgeSpoke(client *types.Client, ks KnowledgeSearcher, industryContext string) spokeResult {
	start := time.Now()
	q := client.Name + " industry regulation compliance"
	if industryContext != "" {
		q = client.Name + " " + industryContext + " regulatory"
	}
	results := ks.Search(q, 5)
	var items []IntelItem
	for _, r := range results {
		items = append(items, IntelItem{
			Source:   "knowledge_store",
			Category: "regulatory",
			Headline: r.Document.Title,
			Data:     map[string]interface{}{"title": r.Document.Title, "content": truncate(r.Excerpt, 500)},
		})
	}
	return spokeResult{source: "knowledge_store", items: items, durationMs: time.Since(start).Milliseconds()}
}

func (e *Engine) runInternalSpoke(client *types.Client, allTasks []*types.Task, timeEntries []types.TimeEntry) spokeResult {
	start := time.Now()
	var items []IntelItem
	now := time.Now()

	for _, t := range allTasks {
		if t.ClientNumber != client.ClientNumber && t.ClientNumber != client.ID {
			continue
		}
		output := ""
		if len(t.Output) > 300 {
			output = t.Output[:300]
		} else {
			output = t.Output
		}
		items = append(items, IntelItem{
			Source:       "internal_tasks",
			Category:     "matter_status",
			EventAt:      t.UpdatedAt.Format(time.RFC3339),
			MatterNumber: t.MatterNumber,
			Headline:     fmt.Sprintf("Task %s: %s", t.Status, truncate(t.Description, 80)),
			Data:         map[string]interface{}{"taskId": t.ID, "status": t.Status, "outputSnippet": output},
		})
	}

	for _, e := range timeEntries {
		if e.ClientNumber != client.ClientNumber {
			continue
		}
		if e.EndedAt != nil {
			continue
		}
		wipAgeDays := int(math.Floor(float64(now.Sub(e.StartedAt)) / float64(24*time.Hour)))
		billUsd := 0.0
		if e.BillingAmountUsd != nil {
			billUsd = *e.BillingAmountUsd
		}
		items = append(items, IntelItem{
			Source:       "internal_time",
			Category:     "billing",
			EventAt:      e.StartedAt.Format(time.RFC3339),
			MatterNumber: e.MatterNumber,
			Headline:     fmt.Sprintf("WIP: %s ($%.0f unbilled, open %dd)", truncate(e.Description, 80), billUsd, wipAgeDays),
			Data:         map[string]interface{}{"entryId": e.ID, "description": e.Description, "billingAmountUsd": billUsd, "wipAgeDays": wipAgeDays},
		})
	}
	return spokeResult{source: "internal_tasks", items: items, durationMs: time.Since(start).Milliseconds()}
}

// ─── Snapshot builders ────────────────────────────────────────────────────────

func (e *Engine) buildMatterSnapshots(clientMatters []types.ClientMatter, tasks []*types.Task, entries []types.TimeEntry) []types.BriefingMatterSnapshot {
	now := time.Now()
	snapshots := make([]types.BriefingMatterSnapshot, 0, len(clientMatters))
	for _, m := range clientMatters {
		mTasks := filterTasks(tasks, m.MatterNumber)
		mEntries := filterEntries(entries, m.MatterNumber)

		var lastActivity time.Time
		for _, t := range mTasks {
			if t.UpdatedAt.After(lastActivity) {
				lastActivity = t.UpdatedAt
			}
		}
		daysSince := 999
		if !lastActivity.IsZero() {
			daysSince = int(now.Sub(lastActivity).Hours() / 24)
		}

		openBilling := 0.0
		totalBilled := 0.0
		for _, en := range mEntries {
			if en.EndedAt == nil && en.BillingAmountUsd != nil {
				openBilling += *en.BillingAmountUsd
			} else if en.EndedAt != nil && en.BillingAmountUsd != nil {
				totalBilled += *en.BillingAmountUsd
			}
		}

		pendingGates := 0
		for _, t := range mTasks {
			pendingGates += len(t.PendingGates)
		}

		status := "idle"
		var latestTask *types.Task
		for _, t := range mTasks {
			if latestTask == nil || t.UpdatedAt.After(latestTask.UpdatedAt) {
				latestTask = t
			}
		}
		if latestTask != nil {
			switch latestTask.Status {
			case types.TaskStatusRunning:
				status = "active"
			case types.TaskStatusComplete:
				status = "complete"
			}
		}

		lastOutput := ""
		if latestTask != nil && len(latestTask.Output) > 0 {
			lastOutput = truncate(latestTask.Output, 300)
		}

		snapshots = append(snapshots, types.BriefingMatterSnapshot{
			MatterNumber:      m.MatterNumber,
			Description:       m.Description,
			PracticeArea:      m.PracticeArea,
			Status:            status,
			DaysSinceActivity: daysSince,
			OpenBillingUsd:    openBilling,
			TotalBilledUsd:    totalBilled,
			PendingGates:      pendingGates,
			LastOutput:        lastOutput,
		})
	}
	return snapshots
}

func (e *Engine) buildBillingSnapshot(entries []types.TimeEntry, clientNumber string, matters []types.BriefingMatterSnapshot) types.BriefingBillingSnapshot {
	now := time.Now()
	ninety := now.AddDate(0, 0, -90)
	last90 := 0.0
	wip := 0.0
	oldestDays := 0
	openCount := 0
	for _, m := range matters {
		if m.Status != "complete" {
			openCount++
		}
		wip += m.OpenBillingUsd
	}
	for _, en := range entries {
		if en.ClientNumber != clientNumber {
			continue
		}
		if en.EndedAt != nil && en.BillingAmountUsd != nil && en.StartedAt.After(ninety) {
			last90 += *en.BillingAmountUsd
		}
		if en.EndedAt == nil {
			days := int(now.Sub(en.StartedAt).Hours() / 24)
			if days > oldestDays {
				oldestDays = days
			}
		}
	}
	return types.BriefingBillingSnapshot{
		Last90DaysUsd:   last90,
		WipUsd:          wip,
		OldestWipDays:   oldestDays,
		OpenMatterCount: openCount,
	}
}

func (e *Engine) collectOpenItems(cb *Chalkboard, matters []types.BriefingMatterSnapshot) []string {
	var items []string
	for _, m := range matters {
		if m.PendingGates > 0 {
			items = append(items, fmt.Sprintf("%s: %d gate(s) await partner approval", m.MatterNumber, m.PendingGates))
		}
		if m.OpenBillingUsd > 0 {
			items = append(items, fmt.Sprintf("%s: $%.0f WIP unbilled", m.MatterNumber, m.OpenBillingUsd))
		}
		if m.Status == "idle" && m.DaysSinceActivity > 30 {
			items = append(items, fmt.Sprintf("%s: no activity for %d days — confirm status with client", m.MatterNumber, m.DaysSinceActivity))
		}
	}
	for _, ci := range cb.ByCategory("correspondence") {
		if len(items) >= 15 {
			break
		}
		items = append(items, "Correspondence: "+ci.Headline)
	}
	emails := mergedEmailItems(cb)
	for i, ei := range emails {
		if i >= 3 || len(items) >= 15 {
			break
		}
		date := "?"
		if len(ei.EventAt) >= 10 {
			date = ei.EventAt[:10]
		}
		items = append(items, fmt.Sprintf("Email [%s]: %s", date, truncate(ei.Headline, 100)))
	}
	if len(items) > 15 {
		items = items[:15]
	}
	return items
}

// ─── Hub synthesis ────────────────────────────────────────────────────────────

func (e *Engine) synthesise(
	client *types.Client,
	cb *Chalkboard,
	matters []types.BriefingMatterSnapshot,
	billing types.BriefingBillingSnapshot,
	openItems []string,
	opts GenerateOpts,
) (execSummary, document string) {
	bySource := func(src string, limit int) string {
		items := cb.BySource(src)
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		if len(items) == 0 {
			return "  (none)"
		}
		lines := make([]string, len(items))
		for i, it := range items {
			date := ""
			if len(it.EventAt) >= 10 {
				date = " " + it.EventAt[:10]
			}
			lines[i] = fmt.Sprintf("  - [%s%s] %s", it.Category, date, it.Headline)
		}
		return strings.Join(lines, "\n")
	}

	emails := mergedEmailItems(cb)
	if len(emails) > 12 {
		emails = emails[:12]
	}
	emailLines := make([]string, len(emails))
	for i, it := range emails {
		date := "?"
		if len(it.EventAt) >= 10 {
			date = it.EventAt[:10]
		}
		emailLines[i] = fmt.Sprintf("  - [%s] %s", date, it.Headline)
	}
	emailBlock := strings.Join(emailLines, "\n")
	if emailBlock == "" {
		emailBlock = "  (not configured or no results)"
	}

	matterLines := make([]string, len(matters))
	for i, m := range matters {
		area := ""
		if m.PracticeArea != "" {
			area = " (" + m.PracticeArea + ")"
		}
		gate := ""
		if m.PendingGates > 0 {
			gate = fmt.Sprintf(" | ⚠ %d gate(s)", m.PendingGates)
		}
		matterLines[i] = fmt.Sprintf("• %s [%s] — %s%s | $%.0f billed | %dd idle%s",
			m.MatterNumber, strings.ToUpper(m.Status), m.Description, area, m.TotalBilledUsd, m.DaysSinceActivity, gate)
	}
	if len(matterLines) == 0 {
		matterLines = []string{"(no matters)"}
	}

	openLine := "(None)"
	if len(openItems) > 0 {
		ol := make([]string, len(openItems))
		for i, it := range openItems {
			ol[i] = "- " + it
		}
		openLine = strings.Join(ol, "\n")
	}

	relNotes := ""
	if client.Notes != "" {
		relNotes = "\nRELATIONSHIP NOTES:\n" + client.Notes + "\n"
	}
	indCtx := ""
	if opts.IndustryContext != "" {
		indCtx = "\nINDUSTRY CONTEXT:\n" + opts.IndustryContext + "\n"
	}

	prompt := fmt.Sprintf(`You are writing a pre-call partner briefing synthesised from multiple connected systems.

CLIENT: %s (%s)
BRIEFING DATE: %s

MATTER STATUS:
%s

BILLING:
  Last 90 days: $%.0f
  WIP unbilled: $%.0f
  Oldest open entry: %dd
  Open matters: %d

OPEN ITEMS:
%s

INTELLIGENCE FROM CONNECTED SYSTEMS:
Email (most recent first):
%s
Slack:
%s
Documents (Drive/Box):
%s
SharePoint:
%s
Teams Conversations:
%s
Knowledge Store:
%s
%s%s
Write:
1. A 2-sentence EXECUTIVE SUMMARY — the single most important thing the partner needs to know.
2. A full BRIEFING DOCUMENT in Markdown with sections: Executive Summary, Matter Status, Billing Posture, Recent Email Threads, Correspondence & Activity, Documents in Play, Regulatory/Industry Context, Open Items & Actions Required, Relationship Notes.

Return JSON: {"executiveSummary":"...","document":"..."}`,
		client.Name, client.ClientNumber, opts.BriefingDate,
		strings.Join(matterLines, "\n"),
		billing.Last90DaysUsd, billing.WipUsd, billing.OldestWipDays, billing.OpenMatterCount,
		openLine,
		emailBlock,
		bySource("slack", 8),
		bySource("google_drive", 5),
		bySource("sharepoint", 5),
		bySource("teams_chat", 5),
		bySource("knowledge_store", 8),
		relNotes, indCtx,
	)

	start := time.Now()
	resp, err := e.provider.Chat(providers.ChatParams{
		Model:     e.sonnet,
		MaxTokens: 2000,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("BriefingEngine synthesis failed", "error", err)
		return e.fallback(client, matters, billing, openItems)
	}

	dms := time.Since(start).Milliseconds()
	cw, cr := 0, 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	costUSD := cost.CalcCostUSD(e.sonnet, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	cost.Default.Record(cost.RecordRequest{
		Model: e.sonnet, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "client_briefing", TaskID: opts.TaskID,
	})

	raw := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			raw = blk.Text
			break
		}
	}
	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return e.fallback(client, matters, billing, openItems)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return e.fallback(client, matters, billing, openItems)
	}
	return parsed["executiveSummary"], parsed["document"]
}

func (e *Engine) fallback(client *types.Client, matters []types.BriefingMatterSnapshot, billing types.BriefingBillingSnapshot, openItems []string) (string, string) {
	summary := fmt.Sprintf("%s has %d matter(s) on record. $%.0f WIP outstanding; %d open item(s) require attention.",
		client.Name, len(matters), billing.WipUsd, len(openItems))
	lines := make([]string, len(openItems))
	for i, it := range openItems {
		lines[i] = "- " + it
	}
	doc := fmt.Sprintf("## %s — Partner Briefing\n\n**Matters:** %d | **WIP:** $%.0f | **Open:** %d\n\n%s",
		client.Name, len(matters), billing.WipUsd, len(openItems), strings.Join(lines, "\n"))
	return summary, doc
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func mergedEmailItems(cb *Chalkboard) []IntelItem {
	items := append(cb.BySource("email_graph"), cb.BySource("email_gmail")...)
	sort.Slice(items, func(i, j int) bool {
		ti := parseEventTime(items[i].EventAt)
		tj := parseEventTime(items[j].EventAt)
		return ti.After(tj)
	})
	return items
}

func parseEventTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func filterTasks(tasks []*types.Task, matterNumber string) []*types.Task {
	var out []*types.Task
	for _, t := range tasks {
		if t.MatterNumber == matterNumber {
			out = append(out, t)
		}
	}
	return out
}

func filterEntries(entries []types.TimeEntry, matterNumber string) []types.TimeEntry {
	var out []types.TimeEntry
	for _, e := range entries {
		if e.MatterNumber == matterNumber {
			out = append(out, e)
		}
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
