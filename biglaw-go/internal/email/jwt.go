// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package email

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// decodeBase64URL decodes a standard or URL-safe base64 string.
func decodeBase64URL(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.StdEncoding.DecodeString(s)
}

// buildGmailJWT signs a JWT using the service account private key (RS256).
func buildGmailJWT(clientEmail, subEmail, privateKeyPEM, audience string) (string, error) {
	now := time.Now().Unix()

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)

	claims := map[string]interface{}{
		"iss":   clientEmail,
		"sub":   subEmail,
		"scope": "https://www.googleapis.com/auth/gmail.readonly",
		"aud":   audience,
		"iat":   now,
		"exp":   now + 3600,
	}
	claimsJSON, _ := json.Marshal(claims)
	claimsEnc := base64.RawURLEncoding.EncodeToString(claimsJSON)

	sigBase := headerEnc + "." + claimsEnc

	// Parse PEM private key
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("no PEM block found in private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("private key is not RSA")
	}

	h := sha256.Sum256([]byte(sigBase))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, 0, h[:])
	if err != nil {
		return "", err
	}
	_ = big.NewInt(0) // keep import
	sigEnc := base64.RawURLEncoding.EncodeToString(sig)
	return sigBase + "." + sigEnc, nil
}
