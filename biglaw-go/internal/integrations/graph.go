// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Microsoft Graph — SharePoint search, Teams message search, webhook posting.
// Auth: client-credentials OAuth2 using GRAPH_TENANT_ID / CLIENT_ID / CLIENT_SECRET.
// All calls: 15 s timeout, 512 KB response cap. Credentials never logged.

package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/strutil"
)

const (
	graphBase     = "https://graph.microsoft.com/v1.0"
	graphTimeout  = 15 * time.Second
	graphMaxBytes = 512 * 1024
)

// ─── Token cache ──────────────────────────────────────────────────────────────

var (
	graphTokenMu  sync.Mutex
	graphTokenVal string
	graphTokenExp time.Time
)

// GetGraphToken returns a valid MS Graph bearer token.
// Returns "" if not configured.
func GetGraphToken() (string, error) {
	// Pre-obtained token (dev mode)
	if t := os.Getenv("GRAPH_ACCESS_TOKEN"); t != "" {
		return t, nil
	}
	tenantID := os.Getenv("GRAPH_TENANT_ID")
	clientID := os.Getenv("GRAPH_CLIENT_ID")
	clientSecret := os.Getenv("GRAPH_CLIENT_SECRET")
	if tenantID == "" || clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("Microsoft Graph not configured")
	}

	graphTokenMu.Lock()
	defer graphTokenMu.Unlock()
	if graphTokenVal != "" && time.Now().Before(graphTokenExp.Add(-60*time.Second)) {
		return graphTokenVal, nil
	}

	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
		"grant_type":    {"client_credentials"},
	}
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	resp, err := httpPost(tokenURL, "application/x-www-form-urlencoded", bytes.NewBufferString(form.Encode()), "")
	if err != nil {
		return "", err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(resp, &tok); err != nil {
		return "", err
	}
	graphTokenVal = tok.AccessToken
	graphTokenExp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return tok.AccessToken, nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func httpGet(urlStr, bearer string) ([]byte, error) {
	client := &http.Client{Timeout: graphTimeout}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Graph GET %s: HTTP %d", urlStr, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, graphMaxBytes))
}

func httpPost(urlStr, contentType string, body io.Reader, bearer string) ([]byte, error) {
	client := &http.Client{Timeout: graphTimeout}
	req, err := http.NewRequest("POST", urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("Graph POST %s: HTTP %d — %s", urlStr, resp.StatusCode, b)
	}
	return io.ReadAll(io.LimitReader(resp.Body, graphMaxBytes))
}

func graphPost(path, token string, payload interface{}) ([]byte, error) {
	urlStr := path
	if len(path) > 0 && path[0] == '/' {
		urlStr = graphBase + path
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return httpPost(urlStr, "application/json", bytes.NewReader(b), token)
}

// ─── SharePoint ───────────────────────────────────────────────────────────────

type SharePointFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	WebURL       string `json:"webUrl"`
	LastModified string `json:"lastModified"`
	Size         int64  `json:"size"`
	SiteID       string `json:"siteId"`
	SiteName     string `json:"siteName"`
	MatterRef    string `json:"matterRef,omitempty"`
}

func SearchSharePoint(query string, maxResults int) ([]SharePointFile, error) {
	token, err := GetGraphToken()
	if err != nil {
		return nil, nil // not configured — return empty silently
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	body := map[string]interface{}{
		"requests": []map[string]interface{}{{
			"entityTypes": []string{"driveItem"},
			"query":       map[string]string{"queryString": query},
			"from":        0,
			"size":        maxResults,
			"fields":      []string{"id", "name", "webUrl", "lastModifiedDateTime", "size", "parentReference"},
		}},
	}
	data, err := graphPost("/search/query", token, body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Value []struct {
			HitsContainers []struct {
				Hits []struct {
					Resource map[string]interface{} `json:"resource"`
				} `json:"hits"`
			} `json:"hitsContainers"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var files []SharePointFile
	for _, v := range result.Value {
		for _, hc := range v.HitsContainers {
			for _, h := range hc.Hits {
				r := h.Resource
				parent, _ := r["parentReference"].(map[string]interface{})
				f := SharePointFile{
					ID:           str(r["id"]),
					Name:         str(r["name"]),
					WebURL:       str(r["webUrl"]),
					LastModified: str(r["lastModifiedDateTime"]),
					SiteID:       str(parent["siteId"]),
					SiteName:     str(parent["siteName"]),
				}
				if s, ok := r["size"].(float64); ok {
					f.Size = int64(s)
				}
				f.MatterRef = extractMatterRef(f.Name)
				files = append(files, f)
			}
		}
	}
	if len(files) > maxResults {
		files = files[:maxResults]
	}
	return files, nil
}

// ─── Teams messages ───────────────────────────────────────────────────────────

type TeamsMessage struct {
	ID          string `json:"id"`
	TeamName    string `json:"teamName"`
	ChannelName string `json:"channelName"`
	ChannelID   string `json:"channelId"`
	From        string `json:"from"`
	CreatedAt   string `json:"createdAt"`
	Body        string `json:"body"`
	WebURL      string `json:"webUrl,omitempty"`
	MatterRef   string `json:"matterRef,omitempty"`
}

func SearchTeamsMessages(query string, maxResults int) ([]TeamsMessage, error) {
	token, err := GetGraphToken()
	if err != nil {
		return nil, nil
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	body := map[string]interface{}{
		"requests": []map[string]interface{}{{
			"entityTypes": []string{"chatMessage"},
			"query":       map[string]string{"queryString": query},
			"from":        0,
			"size":        maxResults,
		}},
	}
	data, err := graphPost("/search/query", token, body)
	if err != nil {
		return nil, err
	}
	var result struct {
		Value []struct {
			HitsContainers []struct {
				Hits []struct {
					Resource map[string]interface{} `json:"resource"`
				} `json:"hits"`
			} `json:"hitsContainers"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var msgs []TeamsMessage
	for _, v := range result.Value {
		for _, hc := range v.HitsContainers {
			for _, h := range hc.Hits {
				r := h.Resource
				chId := r["channelIdentity"]
				var chMap map[string]interface{}
				if m, ok := chId.(map[string]interface{}); ok {
					chMap = m
				}
				fromObj, _ := r["from"].(map[string]interface{})
				fromName := ""
				if u, ok := fromObj["user"].(map[string]interface{}); ok {
					fromName = str(u["displayName"])
				} else if a, ok := fromObj["application"].(map[string]interface{}); ok {
					fromName = str(a["displayName"])
				}
				bodyObj, _ := r["body"].(map[string]interface{})
				bodyText := stripHTML(str(bodyObj["content"]))
				if len(bodyText) > 400 {
					bodyText = strutil.Truncate(bodyText, 400)
				}
				m := TeamsMessage{
					ID:        str(r["id"]),
					ChannelID: str(chMap["channelId"]),
					From:      fromName,
					CreatedAt: str(r["createdDateTime"]),
					Body:      bodyText,
					WebURL:    str(r["webUrl"]),
				}
				m.MatterRef = extractMatterRef(bodyText)
				msgs = append(msgs, m)
			}
		}
	}
	if len(msgs) > maxResults {
		msgs = msgs[:maxResults]
	}
	return msgs, nil
}

// ─── Teams Incoming Webhook ───────────────────────────────────────────────────

type WebhookFact struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PostToTeamsWebhook posts a MessageCard to a Teams Incoming Webhook URL.
func PostToTeamsWebhook(webhookURL, title, text string, facts []WebhookFact) error {
	card := map[string]interface{}{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": "0076D7",
		"summary":    title,
		"sections": []map[string]interface{}{{
			"activityTitle": title,
			"activityText":  text,
			"facts":         facts,
		}},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return err
	}
	_, err = httpPost(webhookURL, "application/json", bytes.NewReader(b), "")
	return err
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

var (
	matterRefRe1 = regexp.MustCompile(`\[([A-Z]{1,5}[-/]\d{3,8})\]`)
	matterRefRe2 = regexp.MustCompile(`\b([A-Z]{1,5}[-/]\d{4,8})\b`)
	htmlTagRe    = regexp.MustCompile(`<[^>]+>`)
)

func extractMatterRef(s string) string {
	if m := matterRefRe1.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	if m := matterRefRe2.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, " ")
}

func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
