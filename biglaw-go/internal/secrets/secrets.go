// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Infisical secrets loader — call Load() before reading config.
// Authenticates with Universal Auth, fetches all secrets, injects into os.Getenv.
// Falls back silently if INFISICAL_* vars are absent.

package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// LoadResult describes the outcome of a secrets load.
type LoadResult struct {
	Source       string // "infisical" or "env"
	Count        int    // secrets injected
	InfisicalURL string
}

// Load fetches secrets from Infisical and injects them into os.Getenv.
// Safe to call multiple times — already-set vars are never overwritten.
// Never panics — on any error logs a warning and returns env fallback.
func Load() LoadResult {
	clientID := os.Getenv("INFISICAL_CLIENT_ID")
	clientSecret := os.Getenv("INFISICAL_CLIENT_SECRET")
	projectID := os.Getenv("INFISICAL_PROJECT_ID")

	if clientID == "" || clientSecret == "" || projectID == "" {
		return LoadResult{Source: "env"}
	}

	infisicalURL := os.Getenv("INFISICAL_URL")
	if infisicalURL == "" {
		infisicalURL = "https://app.infisical.com"
	}
	environment := os.Getenv("INFISICAL_ENV")
	if environment == "" {
		environment = "production"
	}
	secretPath := os.Getenv("INFISICAL_PATH")
	if secretPath == "" {
		secretPath = "/"
	}

	token, err := login(infisicalURL, clientID, clientSecret)
	if err != nil {
		slog.Warn("Infisical auth failed — falling back to env vars", "error", err)
		return LoadResult{Source: "env"}
	}

	secrets, err := fetchSecrets(infisicalURL, token, projectID, environment, secretPath)
	if err != nil {
		slog.Warn("Infisical secrets fetch failed — falling back to env vars", "error", err)
		return LoadResult{Source: "env"}
	}

	count := 0
	for k, v := range secrets {
		if os.Getenv(k) == "" {
			os.Setenv(k, v) //nolint:errcheck
			count++
		}
	}

	slog.Info("Secrets loaded from Infisical",
		"count", count, "total", len(secrets), "url", infisicalURL, "env", environment)
	return LoadResult{Source: "infisical", Count: count, InfisicalURL: infisicalURL}
}

func login(baseURL, clientID, clientSecret string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
	})
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(
		baseURL+"/api/v1/auth/universal-auth/login",
		"application/json",
		bytesReader(body),
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Infisical auth %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func fetchSecrets(baseURL, token, projectID, environment, secretPath string) (map[string]string, error) {
	u, err := url.Parse(baseURL + "/api/v3/secrets/raw")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("workspaceId", projectID)
	q.Set("environment", environment)
	q.Set("secretPath", secretPath)
	q.Set("recursive", "true")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Infisical secrets %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result struct {
		Secrets []struct {
			SecretKey   string `json:"secretKey"`
			SecretValue string `json:"secretValue"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(result.Secrets))
	for _, s := range result.Secrets {
		out[s.SecretKey] = s.SecretValue
	}
	return out, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

type bytesReaderT struct {
	data []byte
	pos  int
}

func bytesReader(b []byte) *bytesReaderT { return &bytesReaderT{data: b} }

func (r *bytesReaderT) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func truncate(s string, max int) string {
	return strutil.Truncate(s, max)
}
