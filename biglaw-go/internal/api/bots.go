// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Big Michael inbound wiring — mounts the Teams/Slack webhook receivers, the
// matter↔channel link management endpoints, and a task-completion notifier, and
// adapts the orchestrator + stores to the bots.OrchestratorFacade the command
// dispatcher needs (including the LPM report/portfolio commands).
package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/bots"
	"github.com/discover-legal/biglaw-go/internal/briefing"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// mountBots registers the bot webhook + matter-link routes and starts the
// task-completion notifier. Webhook receivers verify their own HMAC signatures,
// so they are public; matter-link management is partner-gated.
func (s *Server) mountBots(r *gin.Engine) {
	facade := &botFacade{s: s, briefing: briefing.New(s.provReg.MustGet(routing.ModelSonnet), routing.ModelSonnet)}

	r.POST("/bots/teams/webhook", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		out, status, err := bots.HandleTeamsWebhook(string(body), c.GetHeader("Authorization"), facade)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Data(status, "application/json", out)
	})

	r.POST("/bots/slack/events", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		out, status, err := bots.HandleSlackEvent(
			string(body),
			c.GetHeader("X-Slack-Request-Timestamp"),
			c.GetHeader("X-Slack-Signature"),
			facade,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Data(status, "application/json", out)
	})

	// Matter↔channel link management (partner-gated).
	link := r.Group("/bots")
	link.Use(func(c *gin.Context) {
		if !requirePartner(c) {
			c.Abort()
		}
	})
	link.POST("/teams/matter-link", func(c *gin.Context) {
		var l bots.TeamsMatterLink
		if err := c.ShouldBindJSON(&l); err != nil || l.MatterNumber == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber required"})
			return
		}
		bots.SetTeamsMatterLink(l)
		c.JSON(http.StatusOK, l)
	})
	link.GET("/teams/matter-link/:mn", func(c *gin.Context) {
		if l, ok := bots.GetTeamsMatterLink(c.Param("mn")); ok {
			c.JSON(http.StatusOK, l)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not linked"})
	})
	link.DELETE("/teams/matter-link/:mn", func(c *gin.Context) {
		bots.DeleteTeamsMatterLink(c.Param("mn"))
		c.Status(http.StatusNoContent)
	})
	link.POST("/slack/matter-link", func(c *gin.Context) {
		var l bots.SlackMatterLink
		if err := c.ShouldBindJSON(&l); err != nil || l.MatterNumber == "" || l.ChannelID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber and channelId required"})
			return
		}
		bots.SetSlackMatterLink(l)
		c.JSON(http.StatusOK, l)
	})
	link.GET("/slack/matter-link/:mn", func(c *gin.Context) {
		if l, ok := bots.GetSlackMatterLink(c.Param("mn")); ok {
			c.JSON(http.StatusOK, l)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not linked"})
	})
	link.DELETE("/slack/matter-link/:mn", func(c *gin.Context) {
		bots.DeleteSlackMatterLink(c.Param("mn"))
		c.Status(http.StatusNoContent)
	})

	s.startTaskNotifier()
}

// startTaskNotifier posts a chat notification when a task completes. No-op if
// neither chat platform is configured.
func (s *Server) startTaskNotifier() {
	if !s.cfg.Bots.Teams.Enabled && !s.cfg.Bots.Slack.Enabled {
		return
	}
	ch := orchestrator.SubscribeProgress()
	go func() {
		for ev := range ch {
			if ev.Type != "complete" {
				continue
			}
			task := s.orch.GetTask(ev.TaskID)
			if task == nil || task.MatterNumber == "" {
				continue
			}
			if s.cfg.Bots.Teams.Enabled {
				bots.NotifyTeamsTaskComplete(task.ID, task.MatterNumber, string(task.WorkflowType), task.Output, len(task.Findings))
			}
			if s.cfg.Bots.Slack.Enabled {
				bots.NotifySlackTaskComplete(task.ID, task.MatterNumber, string(task.WorkflowType), task.Output, len(task.Findings))
			}
		}
	}()
}

// ─── Facade ───────────────────────────────────────────────────────────────────

// botFacade adapts the orchestrator + stores to bots.OrchestratorFacade. It holds
// the Server pointer so LPM commands see the service even though it is attached
// after the routes are mounted.
type botFacade struct {
	s        *Server
	briefing *briefing.Engine
}

func (f *botFacade) ListTasks() []*types.Task            { return f.s.orch.ListTasks() }
func (f *botFacade) GetTask(id string) *types.Task       { return f.s.orch.GetTask(id) }
func (f *botFacade) ListTemplates() []types.TaskTemplate { return f.s.orch.ListTemplates() }

func (f *botFacade) SubmitTask(description, workflowType string) (*types.Task, error) {
	return f.s.orch.SubmitTask(orchestrator.SubmitParams{
		Description:  description,
		WorkflowType: types.WorkflowType(workflowType),
	})
}

func (f *botFacade) SubmitFromTemplate(templateID string) (*types.Task, error) {
	return f.s.orch.SubmitFromTemplate(templateID, nil, nil, orchestrator.SubmitParams{})
}

func (f *botFacade) Clients() bots.ClientLookup        { return clientLookupAdapter{f.s.clients} }
func (f *botFacade) Knowledge() bots.KnowledgeSearcher { return knowledgeSearchAdapter{f.s.knowledge} }
func (f *botFacade) Briefing() bots.BriefingGenerator {
	return briefingGenAdapter{f.briefing, f.s.knowledge}
}
func (f *botFacade) ListTimeEntries() []types.TimeEntry {
	return f.s.time.List(timekeeping.TimeFilter{})
}

func (f *botFacade) LPMReport(matterNumber string) (string, error) {
	if f.s.lpm == nil {
		return "LPM is not enabled on this server.", nil
	}
	rep, err := f.s.lpm.GenerateForMatter(lpm.MatterRef{MatterNumber: matterNumber}, "")
	if err != nil {
		return "", err
	}
	return lpm.RenderMarkdown(rep), nil
}

func (f *botFacade) LPMPortfolio() (string, error) {
	if f.s.lpm == nil {
		return "LPM is not enabled on this server.", nil
	}
	br, err := f.s.lpm.GeneratePortfolio(f.s.lpm.ActiveMatters(), "")
	if err != nil {
		return "", err
	}
	return lpm.RenderPortfolioMarkdown(br), nil
}

// ─── Budget + docket commands ───────────────────────────────────────────────

func (f *botFacade) BudgetStatus(matterNumber string) (string, error) {
	burn := f.s.budget.GetBurn(matterNumber)
	if burn == nil {
		return fmt.Sprintf("No budget set for matter **%s**. Set one with `@BigMichael budget %s [amount]`.", matterNumber, matterNumber), nil
	}
	return fmt.Sprintf("**Budget — %s**\n%.0f%% burned: $%.0f of $%.0f ($%.0f remaining).",
		matterNumber, burn.BurnPct*100, burn.BurnUsd, burn.BudgetUsd, burn.Remaining), nil
}

func (f *botFacade) SetMatterBudget(matterNumber, amount string) (string, error) {
	amt, err := strconv.ParseFloat(strings.NewReplacer(",", "", "$", "", "_", "").Replace(strings.TrimSpace(amount)), 64)
	if err != nil || amt <= 0 {
		return "", fmt.Errorf("invalid amount %q", amount)
	}
	if err := f.s.clients.SetMatterBudget(matterNumber, &amt, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("Budget for **%s** set to $%.0f.", matterNumber, amt), nil
}

func (f *botFacade) WatchDocket(matterNumber, docketNumber, court string) (string, error) {
	if f.s.dockets == nil {
		return "Docket monitoring is disabled on this server.", nil
	}
	w, err := f.s.dockets.Watch(matterNumber, docketNumber, court, "")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Now watching docket **%s** (%s) for matter **%s**.", w.DocketNumber, w.Court, matterNumber), nil
}

func (f *botFacade) UnwatchDocket(matterNumber string) (string, error) {
	if f.s.dockets == nil {
		return "Docket monitoring is disabled on this server.", nil
	}
	if !f.s.dockets.Unwatch(matterNumber) {
		return fmt.Sprintf("No docket was being watched for matter **%s**.", matterNumber), nil
	}
	return fmt.Sprintf("Stopped watching the docket for matter **%s**.", matterNumber), nil
}

func (f *botFacade) ListDockets() (string, error) {
	if f.s.dockets == nil {
		return "Docket monitoring is disabled on this server.", nil
	}
	watched := f.s.dockets.List()
	if len(watched) == 0 {
		return "No dockets are being watched.", nil
	}
	lines := []string{"**Watched dockets:**"}
	for _, w := range watched {
		lines = append(lines, fmt.Sprintf("• %s — %s (%s)", w.MatterNumber, w.DocketNumber, w.Court))
	}
	return strings.Join(lines, "\n"), nil
}

// ─── Small adapters ─────────────────────────────────────────────────────────

type clientLookupAdapter struct{ cs *clients.ClientStore }

func (a clientLookupAdapter) GetByClientNumber(number string) *types.Client {
	return a.cs.GetByClientNumber(number)
}

func (a clientLookupAdapter) List() []*types.Client {
	src := a.cs.List()
	out := make([]*types.Client, len(src))
	for i := range src {
		c := src[i]
		out[i] = &c
	}
	return out
}

type knowledgeSearchAdapter struct{ ks *knowledge.Store }

func (a knowledgeSearchAdapter) Search(query string, opts map[string]interface{}) ([]types.SearchResult, error) {
	topK := 5
	if v, ok := opts["topK"].(int); ok && v > 0 {
		topK = v
	}
	return a.ks.Search(query, knowledge.SearchOpts{TopK: topK})
}

type briefingGenAdapter struct {
	e  *briefing.Engine
	ks *knowledge.Store
}

func (a briefingGenAdapter) Generate(client *types.Client, tasks []*types.Task, entries []types.TimeEntry) (*types.ClientBriefing, error) {
	return a.e.Generate(client, tasks, entries, briefing.GenerateOpts{Knowledge: briefingKnowledge{a.ks}})
}

type briefingKnowledge struct{ ks *knowledge.Store }

func (a briefingKnowledge) Search(query string, topK int) []types.SearchResult {
	res, _ := a.ks.Search(query, knowledge.SearchOpts{TopK: topK})
	return res
}
