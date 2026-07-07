// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// OAuth 2.0 / OpenID Connect login flows for Google, Microsoft (common
// tenant), and LinkedIn. Authorization-code flow with no extra OAuth SDK:
// build the authorize URL, exchange the code for tokens, fetch userinfo,
// normalise the identity. Configured purely from cfg.Auth (env-derived);
// the HTTP handlers live in internal/api/auth.go.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// Provider names an OAuth/OIDC login provider. The string value doubles as
// the URL path segment (/auth/<provider>/login, /auth/<provider>/callback).
type Provider string

const (
	ProviderGoogle    Provider = "google"
	ProviderMicrosoft Provider = "microsoft"
	ProviderLinkedIn  Provider = "linkedin"
)

// Providers lists every supported provider in display order.
var Providers = []Provider{ProviderGoogle, ProviderMicrosoft, ProviderLinkedIn}

// Identity is the normalised result of a provider userinfo call.
type Identity struct {
	Sub           string
	Email         string
	Name          string
	Picture       string // avatar URL; empty when the provider omits it
	EmailVerified bool
}

type providerSpec struct {
	authURL     string
	tokenURL    string
	userInfoURL string
	scope       string
}

var providerSpecs = map[Provider]providerSpec{
	ProviderGoogle: {
		authURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:    "https://oauth2.googleapis.com/token",
		userInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		scope:       "openid email profile",
	},
	ProviderMicrosoft: {
		authURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		tokenURL:    "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		userInfoURL: "https://graph.microsoft.com/oidc/userinfo",
		scope:       "openid email profile",
	},
	ProviderLinkedIn: {
		authURL:     "https://www.linkedin.com/oauth/v2/authorization",
		tokenURL:    "https://www.linkedin.com/oauth/v2/accessToken",
		userInfoURL: "https://api.linkedin.com/v2/userinfo",
		scope:       "openid email profile",
	},
}

// oauthHTTP is the shared client for token + userinfo calls. 30 s mirrors the
// connector timeout used elsewhere.
var oauthHTTP = &http.Client{Timeout: 30 * time.Second}

// Credentials returns the configured client ID + secret for a provider.
func Credentials(cfg *config.Config, p Provider) (clientID, clientSecret string) {
	switch p {
	case ProviderGoogle:
		return cfg.Auth.GoogleClientID, cfg.Auth.GoogleClientSecret
	case ProviderMicrosoft:
		return cfg.Auth.MicrosoftClientID, cfg.Auth.MicrosoftClientSecret
	case ProviderLinkedIn:
		return cfg.Auth.LinkedInClientID, cfg.Auth.LinkedInClientSecret
	}
	return "", ""
}

// IsConfigured reports whether both client ID and secret are set for p.
func IsConfigured(cfg *config.Config, p Provider) bool {
	id, secret := Credentials(cfg, p)
	return id != "" && secret != ""
}

// ConfiguredProviders returns the names of every provider with credentials —
// the UI uses this to decide which login buttons to render.
func ConfiguredProviders(cfg *config.Config) []string {
	out := []string{}
	for _, p := range Providers {
		if IsConfigured(cfg, p) {
			out = append(out, string(p))
		}
	}
	return out
}

// RedirectURI is the callback URL registered with the provider:
// {PUBLIC_BASE_URL}/auth/{provider}/callback.
func RedirectURI(cfg *config.Config, p Provider) string {
	return strings.TrimRight(cfg.Auth.BaseURL, "/") + "/auth/" + string(p) + "/callback"
}

// AuthorizeURL builds the provider authorize URL for the login redirect,
// carrying the anti-CSRF state and the OIDC scopes.
func AuthorizeURL(cfg *config.Config, p Provider, state string) (string, error) {
	spec, ok := providerSpecs[p]
	if !ok {
		return "", fmt.Errorf("unknown OAuth provider %q", p)
	}
	clientID, _ := Credentials(cfg, p)
	u, err := url.Parse(spec.authURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", RedirectURI(cfg, p))
	q.Set("scope", spec.scope)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeCode swaps the authorization code for an access token.
// Error messages never include credentials or response bodies.
func ExchangeCode(ctx context.Context, cfg *config.Config, p Provider, code string) (string, error) {
	spec, ok := providerSpecs[p]
	if !ok {
		return "", fmt.Errorf("unknown OAuth provider %q", p)
	}
	clientID, clientSecret := Credentials(cfg, p)
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI(cfg, p)},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := oauthHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed (%d)", res.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&token); err != nil {
		return "", fmt.Errorf("token exchange returned invalid JSON")
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}
	return token.AccessToken, nil
}

// FetchUserInfo calls the provider's OIDC userinfo endpoint and maps the
// payload to a normalised Identity. Microsoft sometimes omits "email" and
// returns the address as "preferred_username" instead.
func FetchUserInfo(ctx context.Context, p Provider, accessToken string) (*Identity, error) {
	spec, ok := providerSpecs[p]
	if !ok {
		return nil, fmt.Errorf("unknown OAuth provider %q", p)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	res, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo failed (%d)", res.StatusCode)
	}
	var info map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("userinfo returned invalid JSON")
	}

	email := jsonStr(info, "email")
	if email == "" && p == ProviderMicrosoft {
		email = jsonStr(info, "preferred_username")
	}
	name := jsonStr(info, "name")
	if name == "" {
		name = email
	}
	if name == "" {
		name = "User"
	}
	return &Identity{
		Sub:     jsonStr(info, "sub"),
		Email:   email,
		Name:    name,
		Picture: jsonStr(info, "picture"),
		// Treat absent email_verified as verified (LinkedIn omits it);
		// only an explicit false rejects the login.
		EmailVerified: info["email_verified"] != false,
	}, nil
}

func jsonStr(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
