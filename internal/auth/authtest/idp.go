// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package authtest is test support: a local OpenID provider (httptest) that serves discovery and a
// JWKS, checks PKCE S256, client credentials and redirect URIs at its token endpoint, and signs ID
// tokens with an RSA or an EC key; plus the helpers tests use to craft refused tokens and to walk a
// browser through a sign-in. No production code imports it.
package authtest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// IdP is the fake provider. Fields may be changed between sign-ins (under no concurrent use).
type IdP struct {
	*httptest.Server
	ClientID, Secret string
	RSA              *rsa.PrivateKey
	EC               *ecdsa.PrivateKey
	Alg              string                    // what the token endpoint signs with: RS256 (default) or ES256
	Kid              string                    // the kid it names ("" with Alg: the key's own)
	User             map[string]any            // the signed-in person's claims (sub, email, email_verified, name)
	Edit             func(map[string]any)      // changes the claims before signing (wrong iss, aud, exp, nonce…)
	Raw              func(nonce string) string // replaces the whole id_token (a crafted one)
	Now              func() time.Time
	Slow             time.Duration // discovery and the JWKS answer this late (a slow issuer)

	mu          sync.Mutex
	rsaKid      string
	keys        []map[string]any // the JWKS served
	grants      map[string]grant
	JWKSFetches int
	Discoveries int
	Tokens      []url.Values // every token request's form, as sent
	TokenAuth   []string     // its Authorization header
}

type grant struct{ challenge, nonce, redirect string }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// New starts the provider on loopback (plain http, which BigLaw accepts only for a loopback issuer).
func New(t testing.TB) *IdP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i := &IdP{ClientID: "biglaw-test", Secret: "client-secret-value-for-tests", RSA: rk, EC: ek, Alg: "RS256", rsaKid: "rsa-1", grants: map[string]grant{},
		User: map[string]any{"sub": "u-ada", "email": "ada@firm.example", "email_verified": true, "name": "Ada Lovelace"}, Now: time.Now}
	i.keys = []map[string]any{RSAJWK(&rk.PublicKey, "rsa-1"), ECJWK(&ek.PublicKey, "ec-1")}
	mux := http.NewServeMux()
	i.Server = httptest.NewServer(mux)
	t.Cleanup(i.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		i.Discoveries++
		slow := i.Slow
		i.mu.Unlock()
		time.Sleep(slow)
		json.NewEncoder(w).Encode(map[string]any{"issuer": i.URL, "authorization_endpoint": i.URL + "/authorize", "token_endpoint": i.URL + "/token",
			"jwks_uri": i.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		slow := i.Slow
		i.mu.Unlock()
		time.Sleep(slow)
		i.mu.Lock()
		defer i.mu.Unlock()
		i.JWKSFetches++
		json.NewEncoder(w).Encode(map[string]any{"keys": i.keys})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("client_id") != i.ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
			q.Get("nonce") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "openid") {
			http.Error(w, "bad authorization request", 400)
			return
		}
		code := b64(randomBytes())
		i.mu.Lock()
		i.grants[code] = grant{q.Get("code_challenge"), q.Get("nonce"), q.Get("redirect_uri")}
		i.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		i.mu.Lock()
		i.Tokens = append(i.Tokens, r.PostForm)
		i.TokenAuth = append(i.TokenAuth, r.Header.Get("Authorization"))
		g, ok := i.grants[r.PostForm.Get("code")]
		delete(i.grants, r.PostForm.Get("code"))
		i.mu.Unlock()
		id, secret, _ := r.BasicAuth()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		w.Header().Set("Content-Type", "application/json")
		if id != i.ClientID || secret != i.Secret {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != g.redirect || b64(sum[:]) != g.challenge {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"leaky detail ` + r.PostForm.Get("code") + `"}`))
			return
		}
		var tok string
		if i.Raw != nil {
			tok = i.Raw(g.nonce)
		} else {
			tok = i.IDToken(g.nonce)
		}
		json.NewEncoder(w).Encode(map[string]any{"id_token": tok, "access_token": "access-token-not-used", "token_type": "Bearer"})
	})
	return i
}

func randomBytes() []byte {
	b := make([]byte, 24)
	rand.Read(b)
	return b
}

// Claims are a valid token's claims for nonce, before Edit.
func (i *IdP) Claims(nonce string) map[string]any {
	now := i.Now()
	c := map[string]any{"iss": i.URL, "aud": i.ClientID, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce}
	for k, v := range i.User {
		c[k] = v
	}
	if i.Edit != nil {
		i.Edit(c)
	}
	return c
}

// IDToken is a token for nonce signed as Alg and Kid say.
func (i *IdP) IDToken(nonce string) string {
	kid := i.Kid
	if kid == "" {
		kid = map[string]string{"RS256": i.rsaKid, "ES256": "ec-1"}[i.Alg]
	}
	return i.Sign(i.Alg, kid, i.Claims(nonce))
}

// Sign signs claims as alg (RS256 with the RSA key, ES256 with the EC key) naming kid.
func (i *IdP) Sign(alg, kid string, claims map[string]any) string {
	h := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	in := Segment(h) + "." + Segment(claims)
	d := sha256.Sum256([]byte(in))
	var sig []byte
	switch alg {
	case "ES256":
		r, s, _ := ecdsa.Sign(rand.Reader, i.EC, d[:])
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	default:
		sig, _ = rsa.SignPKCS1v15(rand.Reader, i.RSA, crypto.SHA256, d[:])
	}
	return in + "." + b64(sig)
}

// HS256 is claims MACed with secret: the key-confusion attack when secret is the RSA public key's bytes.
func HS256(secret []byte, kid string, claims map[string]any) string {
	in := Segment(map[string]any{"alg": "HS256", "kid": kid}) + "." + Segment(claims)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(in))
	return in + "." + b64(m.Sum(nil))
}

// None is an unsigned token (alg none).
func None(claims map[string]any) string {
	return Segment(map[string]any{"alg": "none"}) + "." + Segment(claims) + "."
}

// Segment is v as a base64url JSON segment.
func Segment(v any) string { b, _ := json.Marshal(v); return b64(b) }

// RSAJWK and ECJWK are public keys as JWKS entries.
func RSAJWK(k *rsa.PublicKey, kid string) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
}

func ECJWK(k *ecdsa.PublicKey, kid string) map[string]any {
	b, _ := k.Bytes() // 0x04 || X || Y
	return map[string]any{"kty": "EC", "kid": kid, "use": "sig", "alg": "ES256", "crv": "P-256", "x": b64(b[1:33]), "y": b64(b[33:])}
}

// Rotate replaces the RSA key with a new one under kid (the old one leaves the JWKS): a key
// rotation, which BigLaw meets with a token naming a kid it has not seen.
func (i *IdP) Rotate(t testing.TB, kid string) {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.RSA, i.rsaKid = rk, kid
	i.keys = []map[string]any{RSAJWK(&rk.PublicKey, kid), ECJWK(&i.EC.PublicKey, "ec-1")}
}

// Discovered is how often the discovery document has been fetched.
func (i *IdP) Discovered() int { i.mu.Lock(); defer i.mu.Unlock(); return i.Discoveries }

// SetSlow makes discovery and the JWKS answer d late.
func (i *IdP) SetSlow(d time.Duration) { i.mu.Lock(); i.Slow = d; i.mu.Unlock() }

// Fetches is how often the JWKS has been fetched.
func (i *IdP) Fetches() int { i.mu.Lock(); defer i.mu.Unlock(); return i.JWKSFetches }

// Browser is a client with a cookie jar that follows no redirect: a test walks each hop.
func Browser(t testing.TB) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// SignIn walks c through a sign-in at BigLaw's base URL: /auth/login, the provider's authorize
// (which signs in i.User at once), and BigLaw's callback, whose response it returns (body unread).
func (i *IdP) SignIn(t testing.TB, c *http.Client, base string) *http.Response {
	t.Helper()
	get := func(u string) *http.Response {
		t.Helper()
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := get(base + "/auth/login")
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("/auth/login: %d", resp.StatusCode)
	}
	resp = get(resp.Header.Get("Location"))
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	return get(resp.Header.Get("Location"))
}
