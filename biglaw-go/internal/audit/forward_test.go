// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for the env-configured audit forwarding (forward.go).

package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// envMap returns a getenv func backed by a map (no global env mutation needed).
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadForwardersFromEnv_NoneConfigured(t *testing.T) {
	if fwds := loadForwardersFromEnv(envMap(nil)); len(fwds) != 0 {
		t.Fatalf("expected no forwarders, got %d", len(fwds))
	}
}

func TestLoadForwardersFromEnv_SplunkRequiresToken(t *testing.T) {
	fwds := loadForwardersFromEnv(envMap(map[string]string{
		"AUDIT_SPLUNK_HEC_URL": "https://splunk.example.com:8088",
	}))
	if len(fwds) != 0 {
		t.Fatalf("Splunk without token must be skipped, got %d forwarders", len(fwds))
	}
}

func TestLoadForwardersFromEnv_InvalidURLSkipped(t *testing.T) {
	fwds := loadForwardersFromEnv(envMap(map[string]string{
		"AUDIT_WEBHOOK_URL": "not a url",
	}))
	if len(fwds) != 0 {
		t.Fatalf("invalid URL must be skipped, got %d forwarders", len(fwds))
	}
}

func TestWebhookForwarding(t *testing.T) {
	type captured struct {
		auth        string
		contentType string
		body        []byte
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{
			auth:        r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fwds := loadForwardersFromEnv(envMap(map[string]string{
		"AUDIT_WEBHOOK_URL":   srv.URL,
		"AUDIT_WEBHOOK_TOKEN": "sekrit",
	}))
	if len(fwds) != 1 || fwds[0].name != "webhook" {
		t.Fatalf("expected one webhook forwarder, got %+v", fwds)
	}

	entry := AuditEntry{
		ID:      "e-1",
		TS:      "2026-06-11T12:00:00Z",
		Event:   "task.created",
		ActorID: ActorSystem,
		Data:    map[string]interface{}{"k": "v"},
	}
	if err := fwds[0].send(entry); err != nil {
		t.Fatalf("send: %v", err)
	}

	c := <-got
	if c.auth != "Bearer sekrit" {
		t.Errorf("Authorization = %q, want Bearer sekrit", c.auth)
	}
	if c.contentType != "application/json" {
		t.Errorf("Content-Type = %q", c.contentType)
	}
	var decoded AuditEntry
	if err := json.Unmarshal(c.body, &decoded); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if decoded.ID != "e-1" || decoded.Event != "task.created" {
		t.Errorf("forwarded entry mismatch: %+v", decoded)
	}
}

func TestWebhookForwarding_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	fwds := loadForwardersFromEnv(envMap(map[string]string{"AUDIT_WEBHOOK_URL": srv.URL}))
	if len(fwds) != 1 {
		t.Fatalf("expected one forwarder, got %d", len(fwds))
	}
	err := fwds[0].send(AuditEntry{ID: "e-2"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected status 502 error, got %v", err)
	}
}

func TestSplunkForwarding_Envelope(t *testing.T) {
	type captured struct {
		path    string
		auth    string
		channel string
		body    []byte
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{
			path:    r.URL.Path,
			auth:    r.Header.Get("Authorization"),
			channel: r.Header.Get("X-Splunk-Request-Channel"),
			body:    body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fwds := loadForwardersFromEnv(envMap(map[string]string{
		"AUDIT_SPLUNK_HEC_URL":   srv.URL,
		"AUDIT_SPLUNK_HEC_TOKEN": "hec-token",
		"AUDIT_SPLUNK_INDEX":     "audit-idx",
	}))
	if len(fwds) != 1 || fwds[0].name != "splunk" {
		t.Fatalf("expected one splunk forwarder, got %+v", fwds)
	}
	if err := fwds[0].send(AuditEntry{ID: "e-3", TS: "2026-06-11T12:00:00Z", Event: "task.complete"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	c := <-got
	if c.path != "/services/collector/event" {
		t.Errorf("path = %q", c.path)
	}
	if c.auth != "Splunk hec-token" {
		t.Errorf("Authorization = %q", c.auth)
	}
	if c.channel != "audit-idx" {
		t.Errorf("X-Splunk-Request-Channel = %q", c.channel)
	}
	var envelope struct {
		Time       float64    `json:"time"`
		SourceType string     `json:"sourcetype"`
		Event      AuditEntry `json:"event"`
	}
	if err := json.Unmarshal(c.body, &envelope); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if envelope.SourceType != "big_michael:audit" || envelope.Event.ID != "e-3" || envelope.Time == 0 {
		t.Errorf("HEC envelope mismatch: %+v", envelope)
	}
}
