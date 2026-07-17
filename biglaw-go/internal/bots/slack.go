// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Slack bot — Events API receiver + Web API sender.
// Signature verified via HMAC-SHA256 (v0:timestamp:body).
// Replay protection: rejects requests older than 5 minutes.
// Matter → channel links pre-loaded from SLACK_MATTER_CHANNELS env var.

package bots

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/strutil"
)

const (
	slackAPI     = "https://slack.com/api"
	slackTimeout = 15 * time.Second
)

// ─── Matter link store ────────────────────────────────────────────────────────

// SlackMatterLink maps a matter number to a Slack channel ID.
type SlackMatterLink struct {
	MatterNumber string `json:"matterNumber"`
	ChannelID    string `json:"channelId"`
	ChannelName  string `json:"channelName,omitempty"`
}

var (
	slackMu    sync.RWMutex
	slackLinks = map[string]SlackMatterLink{}
)

func init() {
	raw := os.Getenv("SLACK_MATTER_CHANNELS")
	if raw == "" {
		return
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return
	}
	for mn, ch := range parsed {
		slackLinks[mn] = SlackMatterLink{MatterNumber: mn, ChannelID: ch}
	}
}

// SetSlackMatterLink stores or updates a matter → channel link.
func SetSlackMatterLink(link SlackMatterLink) {
	slackMu.Lock()
	defer slackMu.Unlock()
	slackLinks[link.MatterNumber] = link
}

// GetSlackMatterLink returns the link for a matter.
func GetSlackMatterLink(matterNumber string) (SlackMatterLink, bool) {
	slackMu.RLock()
	defer slackMu.RUnlock()
	l, ok := slackLinks[matterNumber]
	return l, ok
}

// DeleteSlackMatterLink removes a matter link.
func DeleteSlackMatterLink(matterNumber string) {
	slackMu.Lock()
	defer slackMu.Unlock()
	delete(slackLinks, matterNumber)
}

// ─── Signature verification ───────────────────────────────────────────────────

// VerifySlackSignature validates the Slack request signature.
// Returns false if the timestamp is more than 5 minutes old (replay protection).
func VerifySlackSignature(body, timestamp, signature, secret string) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if math.Abs(float64(time.Now().Unix()-ts)) > 300 {
		return false
	}
	sigBase := "v0:" + timestamp + ":" + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigBase))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

// ─── Slack Web API ────────────────────────────────────────────────────────────

func slackAPICall(method string, payload map[string]interface{}) (map[string]interface{}, error) {
	token := os.Getenv("SLACK_BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("SLACK_BOT_TOKEN not configured")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: slackTimeout}
	req, err := http.NewRequest("POST", slackAPI+"/"+method, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if ok, _ := result["ok"].(bool); !ok {
		return nil, fmt.Errorf("Slack API error: %v", result["error"])
	}
	return result, nil
}

// PostToSlackChannel posts a Markdown message to a Slack channel.
func PostToSlackChannel(channelID, text string, opts ...string) error {
	payload := map[string]interface{}{
		"channel":      channelID,
		"text":         text,
		"mrkdwn":       true,
		"unfurl_links": false,
	}
	if len(opts) > 0 && opts[0] != "" {
		payload["thread_ts"] = opts[0]
	}
	_, err := slackAPICall("chat.postMessage", payload)
	return err
}

// SearchSlack searches Slack messages.
func SearchSlack(query string, count int) ([]map[string]interface{}, error) {
	result, err := slackAPICall("search.messages", map[string]interface{}{
		"query":    query,
		"count":    count,
		"sort":     "timestamp",
		"sort_dir": "desc",
	})
	if err != nil {
		return nil, err
	}
	msgs, _ := result["messages"].(map[string]interface{})
	if msgs == nil {
		return nil, nil
	}
	matches, _ := msgs["matches"].([]interface{})
	out := make([]map[string]interface{}, 0, len(matches))
	for _, m := range matches {
		if mm, ok := m.(map[string]interface{}); ok {
			out = append(out, mm)
		}
	}
	return out, nil
}

// ─── Event handler ────────────────────────────────────────────────────────────

// SlackEventBody is a parsed Slack Events API payload.
type SlackEventBody struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Event     struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		User     string `json:"user"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
		BotID    string `json:"bot_id"`
		Subtype  string `json:"subtype"`
	} `json:"event"`
}

// HandleSlackEvent processes an inbound Slack Events API request.
// Returns the immediate response bytes (200 OK), and whether async work
// was started (caller must not block on it).
func HandleSlackEvent(rawBody, timestamp, signature string, orch OrchestratorFacade) ([]byte, int, error) {
	signingSecret := os.Getenv("SLACK_SIGNING_SECRET")
	if signingSecret == "" {
		return jsonErr("Slack bot not configured"), 503, nil
	}
	if !VerifySlackSignature(rawBody, timestamp, signature, signingSecret) {
		slog.Warn("Slack events: invalid signature")
		return jsonErr("Invalid signature"), 401, nil
	}

	var body SlackEventBody
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		return jsonErr("bad request"), 400, nil
	}

	// URL verification challenge
	if body.Type == "url_verification" {
		out, _ := json.Marshal(map[string]string{"challenge": body.Challenge})
		return out, 200, nil
	}
	if body.Type != "event_callback" {
		return []byte(`{"ok":true}`), 200, nil
	}

	ev := body.Event
	if ev.Type != "app_mention" && ev.Type != "message" {
		return []byte(`{"ok":true}`), 200, nil
	}
	if ev.BotID != "" || ev.Subtype == "bot_message" {
		return []byte(`{"ok":true}`), 200, nil
	}
	// Strip Slack user-mention tags <@Uxxxxxxx>
	slackMentionRe := regexp.MustCompile(`<@[A-Z0-9]+>`)
	text := strings.TrimSpace(slackMentionRe.ReplaceAllString(ev.Text, ""))
	if !publicBotCommand(text) && !idAllowed(os.Getenv("SLACK_ALLOWED_USER_IDS"), ev.User) {
		slog.Warn("Slack events: sender not authorized", "channelId", ev.Channel)
		return jsonErr("Sender is not authorized for Big Michael"), 403, nil
	}
	channelID := ev.Channel
	threadTS := ev.ThreadTS
	if threadTS == "" {
		threadTS = ev.TS
	}

	// Respond immediately — Slack requires ack within 3 s
	if !enqueueBotWork(func() {
		response := Dispatch(BotMessage{
			Text:       text,
			SenderName: ev.User,
			ChannelID:  channelID,
			Platform:   PlatformSlack,
		}, orch)

		if postErr := PostToSlackChannel(channelID, response.Immediate, threadTS); postErr != nil {
			slog.Error("Slack post failed", "error", postErr)
			return
		}
		if response.AsyncWork != nil {
			result, err := response.AsyncWork()
			if err != nil {
				result = "Error: " + err.Error()
			}
			if postErr := PostToSlackChannel(channelID, result, threadTS); postErr != nil {
				slog.Warn("Slack async post failed", "error", postErr)
			}
		}
	}) {
		return jsonErr("Big Michael is busy; please retry shortly"), 503, nil
	}

	return []byte(`{"ok":true}`), 200, nil
}

// NotifySlackTaskComplete posts a task-complete notification to the linked channel.
func NotifySlackTaskComplete(taskID, matterNumber, workflowType, output string, findingCount int) {
	l, ok := GetSlackMatterLink(matterNumber)
	channelID := ""
	if ok {
		channelID = l.ChannelID
	} else {
		channelID = os.Getenv("SLACK_DEFAULT_CHANNEL")
	}
	if channelID == "" {
		return
	}
	body := output
	if len(body) > 500 {
		body = strutil.Truncate(body, 500)
	}
	text := fmt.Sprintf("*Matter %s — Task Complete* ✓\n\n%s\n\n_Task ID: `%s` | Workflow: %s | Findings: %d_",
		matterNumber, body, taskID, workflowType, findingCount)
	if err := PostToSlackChannel(channelID, text); err != nil {
		slog.Warn("Slack task notifier failed", "error", err)
	}
}
