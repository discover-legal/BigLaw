// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Clio practice management integration.
// OAuth 2.0 authorization code grant; tokens persisted to CLIO_TOKENS_FILE.
// Region routing via hard-coded allowlist (SSRF-safe): CLIO_REGION = us|eu|ca|au.
// Response bodies capped at 2 MB.

package integrations

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const clioResponseCap = 2_000_000

var clioRegions = map[string]string{
	"us": "https://app.clio.com",
	"eu": "https://eu.app.clio.com",
	"ca": "https://ca.app.clio.com",
	"au": "https://au.app.clio.com",
}

// ClioTokens holds OAuth2 credentials persisted to disk.
type ClioTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // unix ms
	TokenType    string `json:"tokenType"`
	FirmID       string `json:"firmId,omitempty"`
	FirmName     string `json:"firmName,omitempty"`
	ConnectedAt  string `json:"connectedAt"`
}

// ClioClient wraps the Clio REST API.
type ClioClient struct {
	mu         sync.Mutex
	tokens     *ClioTokens
	base       string
	tokensFile string
}

var DefaultClioClient *ClioClient

func init() {
	region := os.Getenv("CLIO_REGION")
	if region == "" {
		region = "us"
	}
	base, ok := clioRegions[region]
	if !ok {
		base = clioRegions["us"]
	}
	tokensFile := os.Getenv("CLIO_TOKENS_FILE")
	if tokensFile == "" {
		tokensFile = "./data/clio-tokens.json"
	}
	DefaultClioClient = &ClioClient{base: base, tokensFile: tokensFile}
	_ = DefaultClioClient.Load()
}

// AuthURL returns the OAuth2 authorization URL to redirect the user to.
func (c *ClioClient) AuthURL(state string) string {
	p := url.Values{
		"response_type": {"code"},
		"client_id":     {os.Getenv("CLIO_CLIENT_ID")},
		"redirect_uri":  {os.Getenv("CLIO_REDIRECT_URI")},
		"state":         {state},
	}
	if scopes := os.Getenv("CLIO_SCOPES"); scopes != "" {
		p.Set("scope", scopes)
	}
	return c.base + "/oauth/authorize?" + p.Encode()
}

// ExchangeCode exchanges an authorization code for tokens and persists them.
func (c *ClioClient) ExchangeCode(code string) error {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {os.Getenv("CLIO_REDIRECT_URI")},
		"client_id":     {os.Getenv("CLIO_CLIENT_ID")},
		"client_secret": {os.Getenv("CLIO_CLIENT_SECRET")},
	}
	data, err := c.oauthPost(form)
	if err != nil {
		return err
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	c.mu.Lock()
	c.tokens = &ClioTokens{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    time.Now().UnixMilli() + int64(body.ExpiresIn)*1000,
		TokenType:    body.TokenType,
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	c.mu.Unlock()

	// Try to fetch firm name (non-fatal)
	if me, err := c.Get("/api/v4/users/who_am_i.json", map[string]string{
		"fields": "id,name,account{id,name}",
	}); err == nil {
		var meResp struct {
			Data struct {
				Account struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				} `json:"account"`
			} `json:"data"`
		}
		if b, err := json.Marshal(me); err == nil {
			_ = json.Unmarshal(b, &meResp)
			c.mu.Lock()
			c.tokens.FirmID = fmt.Sprintf("%d", meResp.Data.Account.ID)
			c.tokens.FirmName = meResp.Data.Account.Name
			c.mu.Unlock()
		}
	}
	return c.save()
}

func (c *ClioClient) refresh() error {
	c.mu.Lock()
	if c.tokens == nil {
		c.mu.Unlock()
		return fmt.Errorf("Clio not connected")
	}
	rt := c.tokens.RefreshToken
	c.mu.Unlock()

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {os.Getenv("CLIO_CLIENT_ID")},
		"client_secret": {os.Getenv("CLIO_CLIENT_SECRET")},
	}
	data, err := c.oauthPost(form)
	if err != nil {
		c.mu.Lock()
		c.tokens = nil
		c.mu.Unlock()
		_ = c.save()
		return fmt.Errorf("Clio token refresh failed: %w — reconnect required", err)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	c.mu.Lock()
	c.tokens.AccessToken = body.AccessToken
	if body.RefreshToken != "" {
		c.tokens.RefreshToken = body.RefreshToken
	}
	c.tokens.ExpiresAt = time.Now().UnixMilli() + int64(body.ExpiresIn)*1000
	c.tokens.TokenType = body.TokenType
	c.mu.Unlock()
	return c.save()
}

func (c *ClioClient) ensureValid() (string, error) {
	c.mu.Lock()
	if c.tokens == nil {
		c.mu.Unlock()
		return "", fmt.Errorf("Clio not connected — visit /auth/clio/connect")
	}
	exp := c.tokens.ExpiresAt
	at := c.tokens.AccessToken
	c.mu.Unlock()

	if time.Now().UnixMilli() >= exp-60_000 {
		if err := c.refresh(); err != nil {
			return "", err
		}
		c.mu.Lock()
		at = c.tokens.AccessToken
		c.mu.Unlock()
	}
	return at, nil
}

// Load reads persisted tokens from disk.
func (c *ClioClient) Load() error {
	data, err := os.ReadFile(c.tokensFile)
	if err != nil {
		return err
	}
	var t ClioTokens
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	c.mu.Lock()
	c.tokens = &t
	c.mu.Unlock()
	return nil
}

func (c *ClioClient) save() error {
	c.mu.Lock()
	data, err := json.MarshalIndent(c.tokens, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.tokensFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(c.tokensFile, data, 0600)
}

// Disconnect clears stored tokens.
func (c *ClioClient) Disconnect() error {
	c.mu.Lock()
	c.tokens = nil
	c.mu.Unlock()
	return c.save()
}

// IsConnected returns true when tokens are available.
func (c *ClioClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens != nil
}

// IsConfigured reports whether the Clio OAuth app credentials are present.
// Mirrors Config.clio.enabled in the TS implementation (CLIO_CLIENT_ID set).
func (c *ClioClient) IsConfigured() bool {
	return os.Getenv("CLIO_CLIENT_ID") != ""
}

// Status returns the connection status.
func (c *ClioClient) Status() map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		return map[string]interface{}{"connected": false}
	}
	return map[string]interface{}{
		"connected":   true,
		"firmName":    c.tokens.FirmName,
		"firmId":      c.tokens.FirmID,
		"connectedAt": c.tokens.ConnectedAt,
	}
}

// Get performs a Clio API GET request.
func (c *ClioClient) Get(path string, params map[string]string) (interface{}, error) {
	token, err := c.ensureValid()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(c.base + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", u.String(), nil)
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
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Clio GET %s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, clioResponseCap))
	if err != nil {
		return nil, err
	}
	var result interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListMatters returns matters filtered by status.
func (c *ClioClient) ListMatters(status string, limit, page int) (interface{}, error) {
	params := map[string]string{
		"fields": "id,display_number,description,status,client{id,name},practice_area{name},open_date,close_date",
		"limit":  fmt.Sprintf("%d", limit),
		"page":   fmt.Sprintf("%d", page),
	}
	if status != "" && status != "all" {
		params["status[]"] = status
	}
	return c.Get("/api/v4/matters.json", params)
}

// GetMatter returns full matter details including client, practice area,
// responsible attorney, and custom fields.
func (c *ClioClient) GetMatter(id int) (interface{}, error) {
	return c.Get(fmt.Sprintf("/api/v4/matters/%d.json", id), map[string]string{
		"fields": "id,display_number,description,status,client{id,name,email_addresses},practice_area{name},open_date,close_date,custom_fields{value,field_name},responsible_attorney{id,name},originating_attorney{id,name}",
	})
}

// ListDocuments lists documents for a matter.
func (c *ClioClient) ListDocuments(matterID int, limit int) (interface{}, error) {
	return c.Get("/api/v4/documents.json", map[string]string{
		"matter_id": fmt.Sprintf("%d", matterID),
		"fields":    "id,name,content_type,latest_document_version{id,fully_uploaded}",
		"limit":     fmt.Sprintf("%d", limit),
	})
}

// DownloadDocument fetches the raw bytes of a document. Clio serves the
// content via a redirect to blob storage, which the default client follows
// (the Authorization header is dropped on cross-host redirects). The body is
// capped at 2 MB, matching the TS getBuffer().
func (c *ClioClient) DownloadDocument(documentID int) ([]byte, error) {
	token, err := c.ensureValid()
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api/v4/documents/%d/download", documentID)
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Clio download %s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, clioResponseCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > clioResponseCap {
		return nil, fmt.Errorf("Clio download exceeded 2 MB cap")
	}
	return data, nil
}

// CreateActivity logs a time entry in Clio.
func (c *ClioClient) CreateActivity(matterID int, description, dateOn string, durationHours float64) (interface{}, error) {
	body := map[string]interface{}{
		"data": map[string]interface{}{
			"type":              "TimeEntry",
			"date":              dateOn,
			"quantity_in_hours": durationHours,
			"note":              description,
			"matter":            map[string]int{"id": matterID},
		},
	}
	return c.post("/api/v4/activities.json", body)
}

// CreateNote posts a note to a matter — used to save synthesis output and
// research memos back into the client file.
func (c *ClioClient) CreateNote(matterID int, subject, detail string) (interface{}, error) {
	body := map[string]interface{}{
		"data": map[string]interface{}{
			"subject": subject,
			"detail":  detail,
			"matter":  map[string]int{"id": matterID},
		},
	}
	return c.post("/api/v4/notes.json", body)
}

// ListContacts lists contacts, optionally filtered by type (Person or Company).
func (c *ClioClient) ListContacts(contactType string, limit int) (interface{}, error) {
	params := map[string]string{
		"fields": "id,name,type,email_addresses,phone_numbers",
		"limit":  fmt.Sprintf("%d", limit),
	}
	if contactType != "" {
		params["type"] = contactType
	}
	return c.Get("/api/v4/contacts.json", params)
}

func (c *ClioClient) post(path string, body interface{}) (interface{}, error) {
	token, err := c.ensureValid()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", c.base+path, io.NopCloser(
		func() io.Reader {
			var r io.Reader = &bytesReader{b: b}
			return r
		}(),
	))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Clio POST %s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, clioResponseCap))
	if err != nil {
		return nil, err
	}
	var result interface{}
	_ = json.Unmarshal(data, &result)
	return result, nil
}

func (c *ClioClient) oauthPost(form url.Values) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("POST", c.base+"/oauth/token",
		io.NopCloser(func() io.Reader {
			return &stringReader{s: form.Encode()}
		}()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Clio OAuth: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, clioResponseCap))
}

// minimal io.Reader wrappers to avoid "bytes" import cycle
type bytesReader struct {
	b   []byte
	pos int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

type stringReader struct {
	s   string
	pos int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}
