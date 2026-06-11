// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Remy / CNTXT integration routes: the client-facing advocate agent pushes
// per-matter advocacy briefs (client voice) and posts notifications to a
// matter. The advocacy brief is surfaced at human gates by the orchestrator;
// notifications fan out to linked Teams/Slack channels when configured.

package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/bots"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func (s *Server) registerRemyRoutes(r *gin.Engine) {
	// Same /matters tree as matters.go — the param name must stay ":matterNumber".
	r.PUT("/matters/:matterNumber/client-voice", s.handleSetClientVoice)
	r.GET("/matters/:matterNumber/client-voice", s.handleGetClientVoice)
	r.POST("/matters/:matterNumber/notify", s.handleNotifyMatter)
	r.GET("/matters/:matterNumber/notifications", s.handleListMatterNotifications)
}

const maxClientVoiceEntries = 200

type clientVoiceBody struct {
	ClientID string                   `json:"clientId"`
	Source   string                   `json:"source"`
	Entries  []types.ClientVoiceEntry `json:"entries"`
}

func (s *Server) handleSetClientVoice(c *gin.Context) {
	cv := s.orch.ClientVoice()
	if cv == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client voice store not configured"})
		return
	}
	var body clientVoiceBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	entries := make([]types.ClientVoiceEntry, 0, len(body.Entries))
	for _, e := range body.Entries {
		note := strings.TrimSpace(e.Note)
		if note == "" {
			continue
		}
		if len(note) > 2000 {
			note = strutil.Truncate(note, 2000)
		}
		cat := strings.TrimSpace(e.Category)
		switch cat {
		case "goal", "concern", "constraint", "preference":
		default:
			cat = "note"
		}
		entries = append(entries, types.ClientVoiceEntry{Category: cat, Note: note, At: e.At})
		if len(entries) >= maxClientVoiceEntries {
			break
		}
	}
	if len(entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "entries is required"})
		return
	}

	voice := cv.SetVoice(types.ClientVoice{
		MatterNumber: c.Param("matterNumber"),
		ClientID:     body.ClientID,
		Source:       body.Source,
		Entries:      entries,
	})
	audit.Default.Write(audit.WriteRequest{
		Event:   "matter.client_voice_updated",
		ActorID: voice.Source,
		Data: map[string]interface{}{
			"matterNumber": voice.MatterNumber,
			"entries":      len(voice.Entries),
		},
	})
	c.JSON(http.StatusOK, voice)
}

func (s *Server) handleGetClientVoice(c *gin.Context) {
	cv := s.orch.ClientVoice()
	if cv == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client voice store not configured"})
		return
	}
	voice := cv.Voice(c.Param("matterNumber"))
	if voice == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no client voice for this matter"})
		return
	}
	c.JSON(http.StatusOK, voice)
}

type notifyMatterBody struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

func (s *Server) handleNotifyMatter(c *gin.Context) {
	cv := s.orch.ClientVoice()
	if cv == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client voice store not configured"})
		return
	}
	var body notifyMatterBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if len(msg) > 4000 {
		msg = strutil.Truncate(msg, 4000)
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "remy"
	}
	matterNumber := c.Param("matterNumber")

	n := cv.Notify(matterNumber, source, msg)
	audit.Default.Write(audit.WriteRequest{
		Event:   "matter.notification",
		ActorID: source,
		Data: map[string]interface{}{
			"matterNumber": matterNumber,
			"message":      msg,
		},
	})

	// Best-effort fan-out to linked channels; failures are reported in the
	// response but never fail the notification itself. Admin-toggleable:
	// when off, the message is stored and audited but pings no channel.
	channels := []string{}
	if s.cfg.ClientVoice.MatterNotifications {
		if _, ok := bots.GetTeamsMatterLink(matterNumber); ok {
			if err := bots.PostToMatter(matterNumber, "Message from "+source, msg); err == nil {
				channels = append(channels, "teams")
			}
		}
		if link, ok := bots.GetSlackMatterLink(matterNumber); ok {
			if err := bots.PostToSlackChannel(link.ChannelID, "["+source+"] "+msg); err == nil {
				channels = append(channels, "slack")
			}
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"notification": n,
		"deliveredTo":  channels,
		"suppressed":   !s.cfg.ClientVoice.MatterNotifications,
	})
}

func (s *Server) handleListMatterNotifications(c *gin.Context) {
	cv := s.orch.ClientVoice()
	if cv == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "client voice store not configured"})
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil {
		limit = 100
	}
	list := cv.Notifications(c.Param("matterNumber"), limit)
	c.JSON(http.StatusOK, list)
}
