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
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/bots"
	"github.com/discover-legal/biglaw-go/internal/briefing"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/integrations"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/discover-legal/biglaw-go/internal/urlguard"
)

// mountBots registers the bot webhook + matter-link routes and starts the
// task-completion notifier. Webhook receivers verify their own HMAC signatures,
// so they are public; matter-link management is partner-gated.
func (s *Server) mountBots(r *gin.Engine) {
	midID := routing.Mid(s.cfg)
	facade := &botFacade{s: s, briefing: briefing.New(s.provReg.MustGet(midID), midID)}

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

	// Matter↔channel link management + internal notify endpoints (partner-gated).
	link := r.Group("/bots")
	link.Use(func(c *gin.Context) {
		if !requirePartner(c) {
			c.Abort()
		}
	})
	link.POST("/teams/notify", s.handleTeamsNotify)
	link.POST("/slack/notify", s.handleSlackNotify)
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

// ─── Internal notify endpoints ───────────────────────────────────────────────

// handleTeamsNotify posts a MessageCard to a Teams Incoming Webhook. Target
// resolution mirrors the TS handler (src/bots/teams.ts): explicit webhookUrl
// (SSRF-validated, https only) > matter link > TEAMS_INCOMING_WEBHOOK_URL.
// Partner-gated by the route group.
func (s *Server) handleTeamsNotify(c *gin.Context) {
	var body struct {
		MatterNumber string                     `json:"matterNumber"`
		Title        string                     `json:"title"`
		Text         string                     `json:"text"`
		WebhookURL   string                     `json:"webhookUrl"`
		Facts        []integrations.WebhookFact `json:"facts"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	target := body.WebhookURL
	if target == "" && body.MatterNumber != "" {
		if l, ok := bots.GetTeamsMatterLink(body.MatterNumber); ok {
			target = l.WebhookURL
		}
	}
	if target == "" {
		target = s.cfg.Bots.Teams.IncomingWebhookURL
	}
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No webhook URL configured for this matter"})
		return
	}

	// Only the caller-supplied override needs validation; linked/default URLs
	// were configured by a partner or the operator.
	if body.WebhookURL != "" {
		validated, err := botsAssertPublicHTTPSURL(body.WebhookURL, "webhookUrl")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		target = validated
	}

	if err := integrations.PostToTeamsWebhook(target, body.Title, body.Text, body.Facts); err != nil {
		// Never echo the error verbatim — webhook URLs are capability tokens.
		slog.Warn("Teams notify failed", "matterNumber", body.MatterNumber)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to post to Teams"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleSlackNotify posts a message to a Slack channel. Target resolution
// mirrors the TS handler (src/bots/slack.ts): explicit channelId > matter
// link > SLACK_DEFAULT_CHANNEL. Partner-gated by the route group.
func (s *Server) handleSlackNotify(c *gin.Context) {
	var body struct {
		MatterNumber string `json:"matterNumber"`
		ChannelID    string `json:"channelId"`
		Text         string `json:"text"`
		ThreadTS     string `json:"threadTs"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	target := body.ChannelID
	if target == "" && body.MatterNumber != "" {
		if l, ok := bots.GetSlackMatterLink(body.MatterNumber); ok {
			target = l.ChannelID
		}
	}
	if target == "" {
		target = s.cfg.Bots.Slack.DefaultChannel
	}
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No channel configured"})
		return
	}

	if err := bots.PostToSlackChannel(target, body.Text, body.ThreadTS); err != nil {
		slog.Warn("Slack notify failed", "matterNumber", body.MatterNumber)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to post to Slack"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ─── URL validation ──────────────────────────────────────────────────────────

// botsAssertPublicHTTPSURL validates a caller-supplied webhook override: it
// must be a public https URL (no SSRF via loopback/private/link-local hosts).
// Delegates to the shared urlguard validator.
func botsAssertPublicHTTPSURL(raw, label string) (string, error) {
	return urlguard.AssertPublicHTTPS(raw, label)
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
