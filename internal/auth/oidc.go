// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package auth is BigLaw's sign-in (README § Sign-in): the OpenID Connect authorization code flow
// with PKCE (S256) against one issuer, ID tokens verified here with the standard library alone
// (discovery, JWKS, RS256/RS384/RS512/ES256/ES384 signatures, iss, aud, azp, exp, iat, nonce), and
// server-side sessions in Postgres (session.go) behind an opaque HttpOnly cookie. Admins are the
// accounts BIGLAW_ADMINS names; every other account the issuer signs in is a member.
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Bounds of the provider's answers and of a token's times.
const (
	maxAnswer    = 1 << 20          // a discovery document, a JWKS or a token answer
	clockSkew    = 60 * time.Second // allowed between BigLaw's clock and the issuer's (exp, iat)
	refetchEvery = time.Minute      // an unknown kid refetches the JWKS at most this often
)

// ErrToken is a refused ID token or token answer; its text is BigLaw's own and never holds the token.
var ErrToken = errors.New("auth: ID token refused")

// metadata is the part of the issuer's discovery document BigLaw reads.
type metadata struct {
	Issuer      string   `json:"issuer"`
	Authorize   string   `json:"authorization_endpoint"`
	Token       string   `json:"token_endpoint"`
	JWKS        string   `json:"jwks_uri"`
	PKCE        []string `json:"code_challenge_methods_supported"`
	AuthMethods []string `json:"token_endpoint_auth_methods_supported"`
}

// key is one signing key of the JWKS: RSA or EC (an "oct" key is never loaded, so a token can never be
// checked with a shared secret), with the alg the set pins it to, if any.
type key struct {
	pub crypto.PublicKey
	alg string
}

// provider is the issuer: discovered once (a failure is retried at the next sign-in) and its keys,
// fetched at first use and again for an unknown kid, at most once a refetchEvery.
type provider struct {
	issuer, clientID, secret string
	http                     *http.Client
	now                      func() time.Time

	mu      sync.Mutex
	meta    *metadata
	keys    map[string]key
	fetched time.Time
}

// loopbackHTTP reports whether u is plain http to a loopback address (local development and tests only).
func loopbackHTTP(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// endpoint checks a discovered URL: https, or plain http only when the issuer itself is loopback http.
func (p *provider) endpoint(raw string) error {
	iss, _ := url.Parse(p.issuer)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || !(u.Scheme == "https" || loopbackHTTP(iss) && loopbackHTTP(u)) {
		return errors.New("auth: the issuer's discovery document names an endpoint that is not an https URL")
	}
	return nil
}

// getJSON reads one bounded JSON answer; redirects are not followed.
func (p *provider) getJSON(ctx context.Context, what, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return fmt.Errorf("auth: the issuer's %s URL is not valid", what)
	}
	req.Header.Set("Accept", "application/json")
	return p.do(req, what, v)
}

func (p *provider) do(req *http.Request, what string, v any) error {
	resp, err := p.http.Do(req)
	if err != nil { // never err.Error(): it can quote what the server sent
		return fmt.Errorf("auth: the issuer's %s could not be reached", what)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil || len(body) > maxAnswer {
		return fmt.Errorf("auth: the issuer's %s answer is unreadable or over 1 MiB", what)
	}
	if err := json.Unmarshal(body, v); err != nil && resp.StatusCode == 200 {
		return fmt.Errorf("auth: the issuer's %s answer is not JSON", what)
	}
	if resp.StatusCode != 200 {
		return &statusError{what, resp.StatusCode}
	}
	return nil
}

type statusError struct {
	what string
	code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("auth: the issuer's %s answered %d", e.what, e.code)
}

// discover reads the issuer's /.well-known/openid-configuration once: its issuer must be exactly
// the configured one, its endpoints https, and S256 among its PKCE methods when it lists any.
func (p *provider) discover(ctx context.Context) (*metadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta != nil {
		return p.meta, nil
	}
	var m metadata
	if err := p.getJSON(ctx, "discovery document", strings.TrimSuffix(p.issuer, "/")+"/.well-known/openid-configuration", &m); err != nil {
		return nil, err
	}
	if m.Issuer != p.issuer {
		return nil, errors.New("auth: the discovery document's issuer is not BIGLAW_OIDC_ISSUER exactly (write the issuer as the provider states it)")
	}
	for _, u := range []string{m.Authorize, m.Token, m.JWKS} {
		if err := p.endpoint(u); err != nil {
			return nil, err
		}
	}
	if len(m.PKCE) > 0 && !slices.Contains(m.PKCE, "S256") {
		return nil, errors.New("auth: the issuer does not support PKCE with S256, which BigLaw requires")
	}
	p.meta = &m
	return p.meta, nil
}

// keyFor is the key a token's header names: by kid (refetching the set once for an unknown one, at
// most once a refetchEvery), or the only key that fits alg when the token names none.
func (p *provider) keyFor(ctx context.Context, m *metadata, kid, alg string) (key, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	find := func() (key, bool) {
		if kid != "" {
			k, ok := p.keys[kid]
			return k, ok && fits(k, alg)
		}
		var found key
		n := 0
		for _, k := range p.keys {
			if fits(k, alg) {
				found, n = k, n+1
			}
		}
		return found, n == 1
	}
	if k, ok := find(); ok {
		return k, nil
	}
	if p.keys != nil && p.now().Sub(p.fetched) < refetchEvery {
		return key{}, fmt.Errorf("%w: no signing key of the issuer matches the token's key id", ErrToken)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Alg, N, E, Crv, X, Y string
		} `json:"keys"`
	}
	p.fetched = p.now()
	if err := p.getJSON(ctx, "JWKS", m.JWKS, &set); err != nil {
		return key{}, err
	}
	keys := map[string]key{}
	for _, j := range set.Keys {
		if j.Use != "" && j.Use != "sig" {
			continue
		}
		var pub crypto.PublicKey
		switch j.Kty {
		case "RSA":
			n, e := b64int(j.N), b64int(j.E)
			if n == nil || e == nil || n.BitLen() < 2048 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
				continue
			}
			pub = &rsa.PublicKey{N: n, E: int(e.Int64())}
		case "EC":
			c := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384()}[j.Crv]
			x, xerr := base64.RawURLEncoding.DecodeString(j.X)
			y, yerr := base64.RawURLEncoding.DecodeString(j.Y)
			if c == nil || xerr != nil || yerr != nil {
				continue
			}
			size := (c.Params().BitSize + 7) / 8
			if len(x) != size || len(y) != size {
				continue
			}
			k, err := ecdsa.ParseUncompressedPublicKey(c, append(append([]byte{4}, x...), y...)) // on the curve, or refused
			if err != nil {
				continue
			}
			pub = k
		default: // oct (a shared secret) and anything else: never a key here
			continue
		}
		keys[j.Kid] = key{pub, j.Alg}
	}
	p.keys = keys
	if k, ok := find(); ok {
		return k, nil
	}
	return key{}, fmt.Errorf("%w: no signing key of the issuer matches the token's key id", ErrToken)
}

// fits reports whether k may check a signature of alg: the alg's key type and curve, and the set's own alg when it pins one.
func fits(k key, alg string) bool {
	if k.alg != "" && k.alg != alg {
		return false
	}
	switch pub := k.pub.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(alg, "RS")
	case *ecdsa.PublicKey:
		return alg == "ES256" && pub.Curve == elliptic.P256() || alg == "ES384" && pub.Curve == elliptic.P384()
	}
	return false
}

func b64int(s string) *big.Int {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil
	}
	return new(big.Int).SetBytes(b)
}

// hashes are the accepted algorithms. "none", HS* (a shared secret: key confusion) and anything
// else are refused before a key is looked up.
var hashes = map[string]crypto.Hash{"RS256": crypto.SHA256, "RS384": crypto.SHA384, "RS512": crypto.SHA512, "ES256": crypto.SHA256, "ES384": crypto.SHA384}

func digest(h crypto.Hash, data []byte) []byte {
	switch h {
	case crypto.SHA384:
		d := sha512.Sum384(data)
		return d[:]
	case crypto.SHA512:
		d := sha512.Sum512(data)
		return d[:]
	}
	d := sha256.Sum256(data)
	return d[:]
}

// Claims are the ID token's claims BigLaw reads.
type Claims struct {
	Iss               string   `json:"iss"`
	Sub               string   `json:"sub"`
	Aud               audience `json:"aud"`
	Azp               string   `json:"azp"`
	Exp               *float64 `json:"exp"`
	Iat               *float64 `json:"iat"`
	Nonce             string   `json:"nonce"`
	Email             string   `json:"email"`
	EmailVerified     any      `json:"email_verified"` // true, or "true" from some issuers
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
}

// audience is aud: one string or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// verified reports whether the issuer says it verified the email address.
func (c Claims) verified() bool { return c.EmailVerified == true || c.EmailVerified == "true" }

// verify checks a compact-serialised ID token: an accepted alg and no crit, the signature by the
// issuer's key, then iss, aud (with azp), exp and iat within clockSkew, the sign-in's nonce and a sub.
func (p *provider) verify(ctx context.Context, m *metadata, raw, nonce string) (Claims, error) {
	var c Claims
	bad := func(why string) (Claims, error) { return Claims{}, fmt.Errorf("%w: %s", ErrToken, why) }
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return bad("not a signed JWT")
	}
	var hdr struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Crit []string `json:"crit"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil {
		return bad("the header is not base64url JSON")
	}
	h, ok := hashes[hdr.Alg]
	if !ok {
		return bad("its algorithm is not one BigLaw accepts (RS256, RS384, RS512, ES256, ES384; never none or HS*)")
	}
	if hdr.Crit != nil {
		return bad("it names critical header extensions")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return bad("the signature is not base64url")
	}
	k, err := p.keyFor(ctx, m, hdr.Kid, hdr.Alg)
	if err != nil {
		return Claims{}, err
	}
	d := digest(h, []byte(parts[0]+"."+parts[1]))
	switch pub := k.pub.(type) {
	case *rsa.PublicKey:
		ok = rsa.VerifyPKCS1v15(pub, h, d, sig) == nil
	case *ecdsa.PublicKey:
		size := (pub.Curve.Params().BitSize + 7) / 8
		ok = len(sig) == 2*size && ecdsa.Verify(pub, d, new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:]))
	default:
		ok = false
	}
	if !ok {
		return bad("the signature does not verify with the issuer's key")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(pb, &c) != nil {
		return bad("the claims are not base64url JSON")
	}
	now := p.now()
	switch {
	case c.Iss != m.Issuer:
		return bad("its issuer is not BIGLAW_OIDC_ISSUER")
	case !slices.Contains(c.Aud, p.clientID):
		return bad("its audience is not BIGLAW_OIDC_CLIENT_ID")
	case c.Azp != "" && c.Azp != p.clientID || len(c.Aud) > 1 && c.Azp == "":
		return bad("it was issued to another client (azp)")
	case c.Exp == nil || now.After(unix(*c.Exp).Add(clockSkew)):
		return bad("it has expired")
	case c.Iat == nil || unix(*c.Iat).After(now.Add(clockSkew)):
		return bad("it is issued in the future")
	case nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return bad("its nonce is not this sign-in's")
	case c.Sub == "" || len(c.Sub) > 255:
		return bad("it has no subject")
	}
	return c, nil
}

func unix(f float64) time.Time { return time.Unix(int64(f), 0) }

// exchange trades the code for the token answer's id_token at the token endpoint, with the PKCE
// verifier and the client's credentials (client_secret_basic unless the issuer offers only _post;
// none for a public client). An error answer is reported by its OAuth error code only.
func (p *provider) exchange(ctx context.Context, m *metadata, code, verifier, redirect string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "client_id": {p.clientID}}
	basic := p.secret != "" && (len(m.AuthMethods) == 0 || slices.Contains(m.AuthMethods, "client_secret_basic"))
	if p.secret != "" && !basic {
		form.Set("client_secret", p.secret)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("auth: the issuer's token endpoint is not valid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(p.clientID), url.QueryEscape(p.secret)) // RFC 6749 § 2.3.1
	}
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := p.do(req, "token endpoint", &out); err != nil {
		var se *statusError
		if errors.As(err, &se) {
			return "", fmt.Errorf("%w: the issuer refused the code (%d %s)", ErrToken, se.code, oauthCode(out.Error))
		}
		return "", err
	}
	if out.IDToken == "" {
		return "", fmt.Errorf("%w: the token answer has no id_token", ErrToken)
	}
	return out.IDToken, nil
}

// oauthCode is an OAuth error code as the issuer sent it, if it is one (letters and _ only), else "".
func oauthCode(s string) string {
	if len(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z') }) {
		return ""
	}
	return s
}

// authURL is the authorization request: code flow, the S256 challenge of the verifier, state and nonce.
func (p *provider) authURL(m *metadata, redirect, state, nonce, verifier string) string {
	u, _ := url.Parse(m.Authorize) // checked at discovery
	q := u.Query()
	ch := sha256.Sum256([]byte(verifier))
	for k, v := range map[string]string{"response_type": "code", "client_id": p.clientID, "redirect_uri": redirect, "scope": "openid email profile",
		"state": state, "nonce": nonce, "code_challenge": base64.RawURLEncoding.EncodeToString(ch[:]), "code_challenge_method": "S256"} {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
