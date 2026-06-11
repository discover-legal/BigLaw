// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Mail transports for the outbound drafter. Two providers (Microsoft Graph and
// Gmail) implement create-draft and send. These are only ever reached in the
// "draft" and "send_gate" write modes, always after the confidentiality guard
// has passed. All calls use a short timeout and a capped response read; message
// bodies are never logged.
package lpm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/email"
	"github.com/discover-legal/biglaw-go/internal/integrations"
)

const (
	transportTimeout  = 15 * time.Second
	transportMaxBytes = 256 * 1024
)

// NewTransport selects a transport from the configured providers, preferring
// Graph. Returns nil when neither is configured (the drafter then refuses
// draft/send_gate modes rather than guessing).
func NewTransport(graphEnabled, gmailEnabled bool, graphUser, gmailUser string) MailTransport {
	switch {
	case graphEnabled && graphUser != "":
		return &graphTransport{user: graphUser}
	case gmailEnabled:
		if gmailUser == "" {
			gmailUser = "me"
		}
		return &gmailTransport{user: gmailUser}
	default:
		return nil
	}
}

// ─── Microsoft Graph ──────────────────────────────────────────────────────────

type graphTransport struct{ user string }

func (g *graphTransport) message(d Draft) map[string]interface{} {
	to := make([]map[string]interface{}, 0, len(d.To))
	for _, addr := range d.To {
		to = append(to, map[string]interface{}{"emailAddress": map[string]string{"address": addr}})
	}
	return map[string]interface{}{
		"subject":      d.Subject,
		"body":         map[string]string{"contentType": "Text", "content": d.Body},
		"toRecipients": to,
	}
}

func (g *graphTransport) CreateDraft(d Draft) error {
	token, err := integrations.GetGraphToken()
	if err != nil || token == "" {
		return fmt.Errorf("graph token unavailable")
	}
	u := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages", url.PathEscape(g.user))
	return graphPost(u, token, g.message(d))
}

func (g *graphTransport) Send(d Draft) error {
	token, err := integrations.GetGraphToken()
	if err != nil || token == "" {
		return fmt.Errorf("graph token unavailable")
	}
	u := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/sendMail", url.PathEscape(g.user))
	return graphPost(u, token, map[string]interface{}{"message": g.message(d), "saveToSentItems": true})
}

func graphPost(u, token string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doTransport(req)
}

// ─── Gmail ────────────────────────────────────────────────────────────────────

type gmailTransport struct{ user string }

// rfc822 builds a minimal RFC-822 message and base64url-encodes it for the Gmail API.
func (g *gmailTransport) raw(d Draft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(d.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", d.Subject)
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
	b.WriteString(d.Body)
	return base64.URLEncoding.EncodeToString([]byte(b.String()))
}

func (g *gmailTransport) CreateDraft(d Draft) error {
	token, err := email.GmailToken()
	if err != nil || token == "" {
		return fmt.Errorf("gmail token unavailable")
	}
	u := fmt.Sprintf("https://gmail.googleapis.com/gmail/v1/users/%s/drafts", url.PathEscape(g.user))
	return gmailPost(u, token, map[string]interface{}{"message": map[string]string{"raw": g.raw(d)}})
}

func (g *gmailTransport) Send(d Draft) error {
	token, err := email.GmailToken()
	if err != nil || token == "" {
		return fmt.Errorf("gmail token unavailable")
	}
	u := fmt.Sprintf("https://gmail.googleapis.com/gmail/v1/users/%s/messages/send", url.PathEscape(g.user))
	return gmailPost(u, token, map[string]string{"raw": g.raw(d)})
}

func gmailPost(u, token string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return doTransport(req)
}

func doTransport(req *http.Request) error {
	client := &http.Client{Timeout: transportTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Read a capped, body-free error context (status only — no message echo).
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, transportMaxBytes))
		return fmt.Errorf("mail transport HTTP %d", resp.StatusCode)
	}
	return nil
}
