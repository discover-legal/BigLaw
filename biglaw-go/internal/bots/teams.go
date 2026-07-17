// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Teams bot — Outgoing Webhook receiver + Incoming Webhook sender.
// HMAC-SHA256 verification on every incoming request.
// Matter → channel links stored in memory and pre-loaded from
// TEAMS_MATTER_WEBHOOKS = '{"M-001":"https://...","M-002":"..."}' env var.

package bots

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/integrations"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// ─── Matter link store ────────────────────────────────────────────────────────

// TeamsMatterLink maps a matter number to a Teams Incoming Webhook URL.
type TeamsMatterLink struct {
	MatterNumber string `json:"matterNumber"`
	WebhookURL   string `json:"webhookUrl"`
	TeamID       string `json:"teamId,omitempty"`
	ChannelID    string `json:"channelId,omitempty"`
}

var (
	teamsMu    sync.RWMutex
	teamsLinks = map[string]TeamsMatterLink{}
)

func init() {
	raw := os.Getenv("TEAMS_MATTER_WEBHOOKS")
	if raw == "" {
		return
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return
	}
	for mn, u := range parsed {
		teamsLinks[mn] = TeamsMatterLink{MatterNumber: mn, WebhookURL: u}
	}
}

// SetTeamsMatterLink stores or updates a matter → channel link.
func SetTeamsMatterLink(link TeamsMatterLink) {
	teamsMu.Lock()
	defer teamsMu.Unlock()
	teamsLinks[link.MatterNumber] = link
}

// GetTeamsMatterLink returns the link for a matter (ok=false if not found).
func GetTeamsMatterLink(matterNumber string) (TeamsMatterLink, bool) {
	teamsMu.RLock()
	defer teamsMu.RUnlock()
	l, ok := teamsLinks[matterNumber]
	return l, ok
}

// DeleteTeamsMatterLink removes a matter link.
func DeleteTeamsMatterLink(matterNumber string) {
	teamsMu.Lock()
	defer teamsMu.Unlock()
	delete(teamsLinks, matterNumber)
}

// ─── HMAC verification ────────────────────────────────────────────────────────

var teamsAtRe = regexp.MustCompile(`<at>[^<]+</at>`)

// VerifyTeamsSignature checks the HMAC-SHA256 Authorization header.
func VerifyTeamsSignature(body, authHeader, secret string) bool {
	if !strings.HasPrefix(authHeader, "HMAC ") {
		return false
	}
	provided := strings.TrimSpace(authHeader[5:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(provided), []byte(expected))
}

// ─── Route handlers (framework-agnostic) ─────────────────────────────────────

// TeamsWebhookRequest is the deserialized body of a Teams Outgoing Webhook.
type TeamsWebhookRequest struct {
	Text        string `json:"text"`
	Attachments []struct {
		Content string `json:"content"`
	} `json:"attachments"`
	From struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
	ChannelData struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
	} `json:"channelData"`
}

// HandleTeamsWebhook processes an incoming Teams Outgoing Webhook request.
// rawBody is the verbatim request body string (for HMAC verification).
// Returns the JSON bytes to send back immediately, or an error.
func HandleTeamsWebhook(rawBody, authHeader string, orch OrchestratorFacade) ([]byte, int, error) {
	secret := os.Getenv("TEAMS_WEBHOOK_SECRET")
	if secret == "" {
		return jsonErr("Teams bot not configured"), 503, nil
	}
	if !VerifyTeamsSignature(rawBody, authHeader, secret) {
		slog.Warn("Teams webhook: invalid HMAC signature")
		return jsonErr("Invalid signature"), 401, nil
	}

	var req TeamsWebhookRequest
	if err := json.Unmarshal([]byte(rawBody), &req); err != nil {
		return jsonErr("bad request"), 400, nil
	}
	text := teamsAtRe.ReplaceAllString(req.Text, "")
	if req.Text == "" && len(req.Attachments) > 0 {
		text = req.Attachments[0].Content
	}
	text = strings.TrimSpace(text)
	channelID := req.ChannelData.Channel.ID
	teamID := req.ChannelData.Team.ID
	if !publicBotCommand(text) && (!idAllowed(os.Getenv("TEAMS_ALLOWED_USER_IDS"), req.From.ID) ||
		!idAllowed(os.Getenv("TEAMS_ALLOWED_TEAM_IDS"), teamID)) {
		slog.Warn("Teams webhook: sender not authorized", "teamId", teamID)
		return jsonErr("Sender is not authorized for Big Michael"), 403, nil
	}

	response := Dispatch(BotMessage{
		Text:       text,
		SenderName: req.From.Name,
		ChannelID:  channelID,
		TeamID:     teamID,
		Platform:   PlatformTeams,
	}, orch)

	if response.AsyncWork != nil {
		targetURL := ""
		if l, ok := GetTeamsMatterLink(channelID); ok {
			targetURL = l.WebhookURL
		}
		if targetURL == "" {
			targetURL = os.Getenv("TEAMS_INCOMING_WEBHOOK_URL")
		}
		if !enqueueBotWork(func() {
			result, err := response.AsyncWork()
			if err != nil {
				result = fmt.Sprintf("Error: %s", err.Error())
			}
			if targetURL != "" {
				if postErr := integrations.PostToTeamsWebhook(targetURL, "Big Michael", result, nil); postErr != nil {
					slog.Warn("Teams async post failed", "error", postErr)
				}
			}
		}) {
			return jsonErr("Big Michael is busy; please retry shortly"), 503, nil
		}
	}

	out, _ := json.Marshal(map[string]string{"type": "message", "text": response.Immediate})
	return out, 200, nil
}

// PostToMatter posts a message to the Teams channel linked to a matter.
func PostToMatter(matterNumber, title, text string) error {
	l, ok := GetTeamsMatterLink(matterNumber)
	if !ok {
		u := os.Getenv("TEAMS_INCOMING_WEBHOOK_URL")
		if u == "" {
			return fmt.Errorf("no Teams channel linked to matter %s", matterNumber)
		}
		return integrations.PostToTeamsWebhook(u, title, text, nil)
	}
	return integrations.PostToTeamsWebhook(l.WebhookURL, title, text, nil)
}

// NotifyTaskComplete posts a completion notification for a task to its linked Teams channel.
func NotifyTeamsTaskComplete(taskID, matterNumber, workflowType, output string, findingCount int) {
	l, ok := GetTeamsMatterLink(matterNumber)
	url := ""
	if ok {
		url = l.WebhookURL
	} else {
		url = os.Getenv("TEAMS_INCOMING_WEBHOOK_URL")
	}
	if url == "" {
		return
	}
	body := output
	if len(body) > 800 {
		body = strutil.Truncate(body, 800)
	}
	facts := []integrations.WebhookFact{
		{Name: "Task ID", Value: taskID},
		{Name: "Workflow", Value: workflowType},
		{Name: "Findings", Value: fmt.Sprintf("%d", findingCount)},
	}
	if err := integrations.PostToTeamsWebhook(url, fmt.Sprintf("Matter %s — Task Complete", matterNumber), body, facts); err != nil {
		slog.Warn("Teams task notifier failed", "error", err)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func jsonErr(msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}
