// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package authtest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestIdPRefusesWhatARealProviderWould: the fake's token endpoint refuses a verifier that is not
// the challenge's, a wrong client secret and a reused code, so the sign-in tests rely on it.
func TestIdPRefusesWhatARealProviderWould(t *testing.T) {
	i := New(t)
	var doc map[string]any
	resp, err := http.Get(i.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	if doc["issuer"] != i.URL {
		t.Fatalf("discovery: %v", doc)
	}
	c := Browser(t)
	code := func() string {
		resp, err := c.Get(i.URL + "/authorize?response_type=code&client_id=" + i.ClientID + "&redirect_uri=http://127.0.0.1:1/cb&scope=openid&state=s&nonce=n" +
			"&code_challenge_method=S256&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM") // the S256 of RFC 7636's example verifier
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, _ := url.Parse(resp.Header.Get("Location"))
		return u.Query().Get("code")
	}
	token := func(code, verifier, secret string) int {
		req, _ := http.NewRequest("POST", i.URL+"/token", strings.NewReader(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:1/cb"}, "code_verifier": {verifier}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(i.ClientID, secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if got := token(code(), "wrong", i.Secret); got != 400 {
		t.Errorf("a wrong verifier: %d", got)
	}
	if got := token(code(), verifier, "wrong"); got != 401 {
		t.Errorf("a wrong secret: %d", got)
	}
	k := code()
	if got := token(k, verifier, i.Secret); got != 200 {
		t.Errorf("a valid exchange: %d", got)
	}
	if got := token(k, verifier, i.Secret); got != 400 {
		t.Errorf("a reused code: %d", got)
	}
}
