// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Email search client — Microsoft Graph (Exchange/O365) + Gmail.
// Both providers are optional; unconfigured providers return empty results.
// All calls: 15 s timeout, 512 KB response cap. Credentials never logged.

package email

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/integrations"
)

const (
	emailTimeout  = 15 * time.Second
	emailMaxBytes = 512 * 1024
)

// Message is a normalized email message from any provider.
type Message struct {
	ID             string `json:"id"`
	Subject        string `json:"subject"`
	From           string `json:"from"`
	ReceivedAt     string `json:"receivedAt"`
	Snippet        string `json:"snippet"`
	MatterRef      string `json:"matterRef,omitempty"`
	Provider       string `json:"provider"` // "graph" | "gmail"
	HasAttachments bool   `json:"hasAttachments"`
}

// ─── Gmail token cache ────────────────────────────────────────────────────────

var (
	gmailTokenMu  sync.Mutex
	gmailTokenVal string
	gmailTokenExp time.Time
)

// getGmailToken returns a Gmail bearer token via service-account JWT (RS256).
// Falls back to GMAIL_ACCESS_TOKEN for dev mode.
func getGmailToken() (string, error) {
	if t := os.Getenv("GMAIL_ACCESS_TOKEN"); t != "" {
		return t, nil
	}
	saKeyJSON := os.Getenv("GMAIL_SA_KEY_JSON")
	userEmail := os.Getenv("GMAIL_USER_EMAIL")
	if saKeyJSON == "" || userEmail == "" {
		return "", fmt.Errorf("Gmail not configured")
	}

	gmailTokenMu.Lock()
	defer gmailTokenMu.Unlock()
	if gmailTokenVal != "" && time.Now().Before(gmailTokenExp.Add(-60*time.Second)) {
		return gmailTokenVal, nil
	}

	// Parse service account JSON
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	raw := saKeyJSON
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		// base64-encoded
		import64, err := decodeBase64URL(raw)
		if err != nil {
			return "", fmt.Errorf("GMAIL_SA_KEY_JSON: %w", err)
		}
		raw = string(import64)
	}
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return "", fmt.Errorf("GMAIL_SA_KEY_JSON parse: %w", err)
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	// Build JWT and exchange — uses crypto/rsa + x509
	jwt, err := buildGmailJWT(sa.ClientEmail, userEmail, sa.PrivateKey, tokenURI)
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {jwt},
	}
	client := &http.Client{Timeout: emailTimeout}
	resp, err := client.PostForm(tokenURI, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Gmail token exchange: HTTP %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, emailMaxBytes))
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", err
	}
	gmailTokenVal = tok.AccessToken
	gmailTokenExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return tok.AccessToken, nil
}

// GmailToken returns a Gmail bearer token (service-account JWT or dev token).
// Exported for outbound transports; never logs credentials.
func GmailToken() (string, error) { return getGmailToken() }

// ─── Graph email search ───────────────────────────────────────────────────────

// SearchGraphMail searches Exchange/O365 mail via Microsoft Graph (last daysBack).
func SearchGraphMail(query string, maxResults, daysBack int) ([]Message, error) {
	return SearchGraphMailWindow(query, maxResults, daysBack, 0)
}

// SearchGraphMailWindow searches mail received in the window between newerDays and
// olderDays ago (olderDays == 0 means no upper bound). Used by the historical
// backfill to page through older mail without re-fetching the recent window.
func SearchGraphMailWindow(query string, maxResults, newerDays, olderDays int) ([]Message, error) {
	if os.Getenv("GRAPH_ACCESS_TOKEN") == "" &&
		(os.Getenv("GRAPH_TENANT_ID") == "" || os.Getenv("GRAPH_CLIENT_ID") == "") {
		return nil, nil
	}
	token, err := integrations.GetGraphToken()
	if err != nil || token == "" {
		return nil, nil
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if newerDays <= 0 {
		newerDays = 90
	}
	userEmail := os.Getenv("GRAPH_USER_EMAIL")
	if userEmail == "" {
		return nil, nil
	}
	since := time.Now().AddDate(0, 0, -newerDays).UTC().Format(time.RFC3339)
	filter := "receivedDateTime ge " + since
	if olderDays > 0 {
		until := time.Now().AddDate(0, 0, -olderDays).UTC().Format(time.RFC3339)
		filter += " and receivedDateTime lt " + until
	}
	qs := url.Values{
		"$search":  {`"` + query + `"`},
		"$filter":  {filter},
		"$top":     {fmt.Sprintf("%d", maxResults)},
		"$select":  {"id,subject,from,receivedDateTime,bodyPreview,hasAttachments"},
		"$orderby": {"receivedDateTime desc"},
	}
	apiURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages?%s",
		url.PathEscape(userEmail), qs.Encode())

	client := &http.Client{Timeout: emailTimeout}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, emailMaxBytes))
	if err != nil {
		return nil, err
	}
	var result struct {
		Value []struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
			From    struct {
				EmailAddress struct {
					Address string `json:"address"`
				} `json:"emailAddress"`
			} `json:"from"`
			ReceivedDateTime string `json:"receivedDateTime"`
			BodyPreview      string `json:"bodyPreview"`
			HasAttachments   bool   `json:"hasAttachments"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(result.Value))
	for _, m := range result.Value {
		subj := m.Subject
		if subj == "" {
			subj = "(no subject)"
		}
		snippet := m.BodyPreview
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		msgs = append(msgs, Message{
			ID:             m.ID,
			Subject:        subj,
			From:           m.From.EmailAddress.Address,
			ReceivedAt:     m.ReceivedDateTime,
			Snippet:        snippet,
			MatterRef:      extractEmailMatterRef(m.Subject),
			Provider:       "graph",
			HasAttachments: m.HasAttachments,
		})
	}
	return msgs, nil
}

// ─── Gmail search ─────────────────────────────────────────────────────────────

// SearchGmail searches Gmail via the Gmail API (service-account or dev token).
func SearchGmail(query string, maxResults, daysBack int) ([]Message, error) {
	return SearchGmailWindow(query, maxResults, daysBack, 0)
}

// SearchGmailWindow searches Gmail in the window between newerDays and olderDays
// ago (olderDays == 0 means no upper bound). Used by the historical backfill.
func SearchGmailWindow(query string, maxResults, newerDays, olderDays int) ([]Message, error) {
	if os.Getenv("GMAIL_ACCESS_TOKEN") == "" && os.Getenv("GMAIL_SA_KEY_JSON") == "" {
		return nil, nil
	}
	token, err := getGmailToken()
	if err != nil || token == "" {
		return nil, nil
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if newerDays <= 0 {
		newerDays = 90
	}
	userEmail := os.Getenv("GMAIL_USER_EMAIL")
	if userEmail == "" {
		userEmail = "me"
	}

	gmailQuery := fmt.Sprintf("%s newer_than:%dd", query, newerDays)
	if olderDays > 0 {
		gmailQuery += fmt.Sprintf(" older_than:%dd", olderDays)
	}
	listURL := fmt.Sprintf("https://gmail.googleapis.com/gmail/v1/users/%s/messages?%s",
		url.PathEscape(userEmail),
		url.Values{"q": {gmailQuery}, "maxResults": {fmt.Sprintf("%d", maxResults)}}.Encode())

	listData, err := gmailGET(listURL, token)
	if err != nil {
		return nil, err
	}
	var listResp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(listData, &listResp); err != nil {
		return nil, err
	}

	const concurrency = 10
	var (
		mu   sync.Mutex
		msgs []Message
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, concurrency)
	for _, m := range listResp.Messages {
		msgID := m.ID
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			msgURL := fmt.Sprintf(
				"https://gmail.googleapis.com/gmail/v1/users/%s/messages/%s?format=metadata&metadataHeaders=Subject&metadataHeaders=From&metadataHeaders=Date",
				url.PathEscape(userEmail), url.PathEscape(msgID))
			data, err := gmailGET(msgURL, token)
			if err != nil {
				return
			}
			var raw struct {
				ID      string `json:"id"`
				Snippet string `json:"snippet"`
				Payload struct {
					Headers []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"headers"`
					Parts []interface{} `json:"parts"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				return
			}
			h := func(name string) string {
				for _, hdr := range raw.Payload.Headers {
					if strings.EqualFold(hdr.Name, name) {
						return hdr.Value
					}
				}
				return ""
			}
			subj := h("subject")
			if subj == "" {
				subj = "(no subject)"
			}
			snippet := raw.Snippet
			if len(snippet) > 400 {
				snippet = snippet[:400]
			}
			mu.Lock()
			msgs = append(msgs, Message{
				ID:             raw.ID,
				Subject:        subj,
				From:           h("from"),
				ReceivedAt:     h("date"),
				Snippet:        snippet,
				MatterRef:      extractEmailMatterRef(subj),
				Provider:       "gmail",
				HasAttachments: len(raw.Payload.Parts) > 0,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()
	return msgs, nil
}

func gmailGET(urlStr, token string) ([]byte, error) {
	client := &http.Client{Timeout: emailTimeout}
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Gmail API HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, emailMaxBytes))
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

var (
	emailMatterRef1 = regexp.MustCompile(`\[([A-Z]{1,5}[-/]\d{3,8})\]`)
	emailMatterRef2 = regexp.MustCompile(`(?i)\((?:matter|file|ref)[:\s]+([A-Z0-9/-]+)\)`)
	emailMatterRef3 = regexp.MustCompile(`\b([A-Z]{1,5}[-/]\d{4,8})\b`)
)

func extractEmailMatterRef(subject string) string {
	for _, re := range []*regexp.Regexp{emailMatterRef1, emailMatterRef2, emailMatterRef3} {
		if m := re.FindStringSubmatch(subject); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}
