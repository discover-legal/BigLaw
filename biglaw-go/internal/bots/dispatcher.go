// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Bot command dispatcher — shared by Teams and Slack bots.
// Parses "@BigMichael <command> [args]" and dispatches to the orchestrator.
// Synchronous commands respond immediately; long tasks post back asynchronously.

package bots

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Orchestrator interface (bots need only this subset) ─────────────────────

// KnowledgeSearcher is the knowledge store subset used by the dispatcher.
type KnowledgeSearcher interface {
	Search(query string, opts map[string]interface{}) ([]types.SearchResult, error)
}

// BriefingGenerator generates a client intelligence briefing.
type BriefingGenerator interface {
	Generate(client *types.Client, tasks []*types.Task, entries []types.TimeEntry) (*types.ClientBriefing, error)
}

// ClientLookup finds clients by number or name.
type ClientLookup interface {
	GetByClientNumber(number string) *types.Client
	List() []*types.Client
}

// OrchestratorFacade is the subset of the orchestrator the dispatcher needs.
type OrchestratorFacade interface {
	ListTasks() []*types.Task
	GetTask(id string) *types.Task
	SubmitTask(description, workflowType string) (*types.Task, error)
	ListTemplates() []types.TaskTemplate
	SubmitFromTemplate(templateID string) (*types.Task, error)
	Clients() ClientLookup
	Knowledge() KnowledgeSearcher
	Briefing() BriefingGenerator
	ListTimeEntries() []types.TimeEntry
	// LPM commands return ready-to-post Markdown (or an error/availability
	// message); kept as strings so the dispatcher stays decoupled from the LPM
	// package.
	LPMReport(matterNumber string) (string, error)
	LPMPortfolio() (string, error)
	// Budget + docket inputs (return a human-readable confirmation/summary).
	BudgetStatus(matterNumber string) (string, error)
	SetMatterBudget(matterNumber, amount string) (string, error)
	WatchDocket(matterNumber, docketNumber, court string) (string, error)
	UnwatchDocket(matterNumber string) (string, error)
	ListDockets() (string, error)
}

// ─── Message / response types ─────────────────────────────────────────────────

// BotPlatform identifies the channel the message originated from.
type BotPlatform string

const (
	PlatformTeams BotPlatform = "teams"
	PlatformSlack BotPlatform = "slack"
)

// BotMessage is a normalized inbound message after @-mention stripping.
type BotMessage struct {
	Text        string
	SenderName  string
	SenderEmail string
	ChannelID   string
	TeamID      string
	ThreadID    string
	Platform    BotPlatform
}

// BotResponse is the dispatcher's reply.
type BotResponse struct {
	// Immediate is the synchronous reply (sent in the same HTTP turn).
	Immediate string
	// AsyncWork, when non-nil, is run in background; its result is posted back.
	AsyncWork func() (string, error)
}

// ─── Dispatcher ───────────────────────────────────────────────────────────────

var botNameRe = regexp.MustCompile(`(?i)^@?big[-_]?michael[\s:,]*`)

func parseCommand(raw string) (command, args string) {
	text := strings.TrimSpace(botNameRe.ReplaceAllString(raw, ""))
	i := strings.IndexByte(text, ' ')
	if i < 0 {
		return strings.ToLower(text), ""
	}
	return strings.ToLower(text[:i]), strings.TrimSpace(text[i+1:])
}

// Dispatch parses a BotMessage and returns a BotResponse.
func Dispatch(msg BotMessage, orch OrchestratorFacade) BotResponse {
	command, args := parseCommand(msg.Text)

	switch command {

	case "status":
		mn := strings.TrimSpace(args)
		if mn == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael status [matter-number]`"}
		}
		var matching []*types.Task
		for _, t := range orch.ListTasks() {
			if t.MatterNumber == mn {
				matching = append(matching, t)
			}
		}
		if len(matching) == 0 {
			return BotResponse{Immediate: fmt.Sprintf("No tasks found for matter **%s**.", mn)}
		}
		running := 0
		gates := 0
		for _, t := range matching {
			if t.Status == types.TaskStatusRunning {
				running++
			}
			gates += len(t.PendingGates)
		}
		lines := []string{
			fmt.Sprintf("**Matter %s** — %d task(s)", mn, len(matching)),
			"",
			fmt.Sprintf("**Running:** %d | **Pending gates:** %d", running, gates),
		}
		return BotResponse{Immediate: strings.Join(lines, "\n")}

	case "briefing":
		clientRef := strings.TrimSpace(args)
		if clientRef == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael briefing [client-name-or-number]`"}
		}
		cl := orch.Clients()
		var clientRecord *types.Client
		if c := cl.GetByClientNumber(clientRef); c != nil {
			clientRecord = c
		} else {
			ref := strings.ToLower(clientRef)
			for _, c := range cl.List() {
				if strings.Contains(strings.ToLower(c.Name), ref) {
					clientRecord = c
					break
				}
			}
		}
		if clientRecord == nil {
			return BotResponse{
				Immediate: fmt.Sprintf("Client not found: **%s**. Check the client number or name.", clientRef),
			}
		}
		cr := clientRecord
		bg := orch.Briefing()
		allTasks := orch.ListTasks()
		allEntries := orch.ListTimeEntries()
		return BotResponse{
			Immediate: fmt.Sprintf("Assembling briefing for **%s** — scanning all sources…", cr.Name),
			AsyncWork: func() (string, error) {
				briefing, err := bg.Generate(cr, allTasks, allEntries)
				if err != nil {
					return "", err
				}
				return briefing.Document, nil
			},
		}

	case "search":
		if args == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael search [query]`"}
		}
		results, err := orch.Knowledge().Search(args, map[string]interface{}{"topK": 5})
		if err != nil || len(results) == 0 {
			return BotResponse{Immediate: fmt.Sprintf("No results found for: **%s**", args)}
		}
		lines := []string{fmt.Sprintf("**Knowledge search:** %s", args), ""}
		for _, r := range results {
			if len(lines) > 12 {
				break
			}
			title := r.Document.Title
			if title == "" {
				title = "Result"
			}
			snippet := r.Excerpt
			if len(snippet) > 150 {
				snippet = strutil.Truncate(snippet, 150)
			}
			lines = append(lines, fmt.Sprintf("**%s**", title), snippet, "")
		}
		return BotResponse{Immediate: strings.Join(lines, "\n")}

	case "task":
		if args == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael task [description]`"}
		}
		desc := args
		return BotResponse{
			Immediate: fmt.Sprintf("Submitting task: _%s_…", truncate80(desc)),
			AsyncWork: func() (string, error) {
				task, err := orch.SubmitTask(desc, "roundtable")
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Task submitted ✓\n**ID:** `%s`\nUse `@BigMichael status` to follow progress.", task.ID), nil
			},
		}

	case "report":
		mn := strings.TrimSpace(args)
		if mn == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael report [matter-number]`"}
		}
		return BotResponse{
			Immediate: fmt.Sprintf("Generating status report for **%s**…", mn),
			AsyncWork: func() (string, error) { return orch.LPMReport(mn) },
		}

	case "portfolio":
		return BotResponse{
			Immediate: "Assembling the portfolio briefing…",
			AsyncWork: func() (string, error) { return orch.LPMPortfolio() },
		}

	case "budget":
		f := strings.Fields(args)
		if len(f) == 0 {
			return BotResponse{Immediate: "Usage: `@BigMichael budget [matter] [amount?]` (omit amount to see burn)"}
		}
		if len(f) >= 2 {
			return BotResponse{Immediate: immediateResult(orch.SetMatterBudget(f[0], f[1]))}
		}
		return BotResponse{Immediate: immediateResult(orch.BudgetStatus(f[0]))}

	case "watch":
		f := strings.Fields(args)
		if len(f) < 2 {
			return BotResponse{Immediate: "Usage: `@BigMichael watch [matter] [docket-number] [court?]`"}
		}
		court := ""
		if len(f) >= 3 {
			court = f[2]
		}
		return BotResponse{Immediate: immediateResult(orch.WatchDocket(f[0], f[1], court))}

	case "unwatch":
		mn := strings.TrimSpace(args)
		if mn == "" {
			return BotResponse{Immediate: "Usage: `@BigMichael unwatch [matter]`"}
		}
		return BotResponse{Immediate: immediateResult(orch.UnwatchDocket(mn))}

	case "dockets":
		return BotResponse{Immediate: immediateResult(orch.ListDockets())}

	case "run":
		templateID := strings.TrimSpace(args)
		if templateID == "" {
			tmpls := orch.ListTemplates()
			lines := make([]string, 0, len(tmpls)+2)
			lines = append(lines, "**Available templates:**")
			for _, t := range tmpls {
				lines = append(lines, fmt.Sprintf("• `%s` — %s", t.ID, t.Name))
			}
			lines = append(lines, "", "Usage: `@BigMichael run [template-id]`")
			return BotResponse{Immediate: strings.Join(lines, "\n")}
		}
		tid := templateID
		return BotResponse{
			Immediate: fmt.Sprintf("Running template `%s`…", tid),
			AsyncWork: func() (string, error) {
				task, err := orch.SubmitFromTemplate(tid)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Template `%s` started ✓\n**Task ID:** `%s`", tid, task.ID), nil
			},
		}

	default:
		return BotResponse{Immediate: helpText}
	}
}

// immediateResult formats a synchronous facade result for posting.
func immediateResult(msg string, err error) string {
	if err != nil {
		return "Error: " + err.Error()
	}
	return msg
}

func truncate80(s string) string {
	return strutil.Truncate(s, 80)
}

const helpText = `**Big Michael** — multi-agent legal AI

| Command | Description |
|---------|-------------|
| ` + "`status [matter]`" + ` | Matter health score + active tasks |
| ` + "`report [matter]`" + ` | Daily LPM status report for a matter |
| ` + "`portfolio`" + ` | 0600 BLUF portfolio briefing across active matters |
| ` + "`budget [matter] [amount?]`" + ` | Show burn, or set the matter budget |
| ` + "`watch [matter] [docket] [court?]`" + ` | Watch a court docket for new filings |
| ` + "`unwatch [matter]`" + ` | Stop watching a matter's docket |
| ` + "`dockets`" + ` | List watched dockets |
| ` + "`briefing [client]`" + ` | Client intelligence briefing (all sources) |
| ` + "`search [query]`" + ` | Semantic search across the knowledge store |
| ` + "`task [description]`" + ` | Submit a new roundtable AI task |
| ` + "`run [template-id]`" + ` | Run a named workflow template |
| ` + "`help`" + ` | Show this message |`
