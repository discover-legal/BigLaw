// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Best-effort async forwarding of audit entries to external log platforms,
// configured purely via environment variables (the audit package does not
// import config). Port of the TS audit sinks (src/audit/sinks/*.ts), per-entry
// rather than batched — fine for SBC-scale event rates.
//
//	OpenSearch / Elasticsearch:
//	  AUDIT_OPENSEARCH_URL       — base URL; entries indexed into the monthly
//	                               index big-michael-audit-YYYY.MM
//	  AUDIT_OPENSEARCH_API_KEY   — optional, sent as "Authorization: ApiKey …"
//	Splunk HTTP Event Collector:
//	  AUDIT_SPLUNK_HEC_URL       — base URL (POSTs to /services/collector/event)
//	  AUDIT_SPLUNK_HEC_TOKEN     — required, sent as "Authorization: Splunk …"
//	  AUDIT_SPLUNK_INDEX         — optional X-Splunk-Request-Channel header
//	Generic webhook (Datadog Logs, Azure Monitor, custom SIEM, …):
//	  AUDIT_WEBHOOK_URL          — entry POSTed verbatim as JSON
//	  AUDIT_WEBHOOK_TOKEN        — optional, sent as "Authorization: Bearer …"
//
// Forwarding is fire-and-forget: it never blocks Logger.Write, never panics,
// times out quickly, and never puts credentials in log output.

package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	forwardTimeout      = 5 * time.Second
	forwardMaxRespBytes = 64 * 1024
)

// forwarder is one configured external destination.
type forwarder struct {
	name string
	send func(entry AuditEntry) error
}

var (
	forwardOnce sync.Once
	forwarders  []forwarder

	forwardClient = &http.Client{Timeout: forwardTimeout}
)

// forwardEntry dispatches an entry to every configured destination, each in
// its own goroutine. Best-effort: failures are logged (without secrets) and
// dropped; panics are swallowed.
func forwardEntry(entry AuditEntry) {
	forwardOnce.Do(func() { forwarders = loadForwardersFromEnv(os.Getenv) })
	for _, f := range forwarders {
		go func(f forwarder) {
			defer func() { _ = recover() }()
			if err := f.send(entry); err != nil {
				slog.Warn("audit forward failed", "sink", f.name, "error", sanitizeForwardErr(err))
			}
		}(f)
	}
}

// loadForwardersFromEnv builds the forwarder list from env vars. getenv is
// injected for testability. Misconfigured destinations are skipped with a
// warning rather than failing startup (mirrors the TS sink registration).
func loadForwardersFromEnv(getenv func(string) string) []forwarder {
	var out []forwarder

	if raw := strings.TrimSpace(getenv("AUDIT_OPENSEARCH_URL")); raw != "" {
		if base, ok := forwardBaseURL(raw, "AUDIT_OPENSEARCH_URL"); ok {
			apiKey := getenv("AUDIT_OPENSEARCH_API_KEY")
			out = append(out, forwarder{name: "opensearch", send: func(e AuditEntry) error {
				return sendToOpenSearch(base, apiKey, e)
			}})
			slog.Info("Audit sink registered", "sink", "opensearch")
		}
	}

	if raw := strings.TrimSpace(getenv("AUDIT_SPLUNK_HEC_URL")); raw != "" {
		token := getenv("AUDIT_SPLUNK_HEC_TOKEN")
		if token == "" {
			slog.Warn("Audit sink skipped: AUDIT_SPLUNK_HEC_URL set but AUDIT_SPLUNK_HEC_TOKEN missing")
		} else if base, ok := forwardBaseURL(raw, "AUDIT_SPLUNK_HEC_URL"); ok {
			index := getenv("AUDIT_SPLUNK_INDEX")
			out = append(out, forwarder{name: "splunk", send: func(e AuditEntry) error {
				return sendToSplunk(base, token, index, e)
			}})
			slog.Info("Audit sink registered", "sink", "splunk")
		}
	}

	if raw := strings.TrimSpace(getenv("AUDIT_WEBHOOK_URL")); raw != "" {
		if u, ok := forwardFullURL(raw, "AUDIT_WEBHOOK_URL"); ok {
			token := getenv("AUDIT_WEBHOOK_TOKEN")
			out = append(out, forwarder{name: "webhook", send: func(e AuditEntry) error {
				return sendToWebhook(u, token, e)
			}})
			slog.Info("Audit sink registered", "sink", "webhook")
		}
	}

	return out
}

// ─── Destinations ─────────────────────────────────────────────────────────────

// sendToOpenSearch indexes the entry into the monthly audit index, keyed by
// the entry ID (idempotent on retry).
func sendToOpenSearch(base, apiKey string, entry AuditEntry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	index := fmt.Sprintf("big-michael-audit-%s", time.Now().UTC().Format("2006.01"))
	target := base + "/" + index + "/_doc/" + url.PathEscape(entry.ID)
	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "ApiKey " + apiKey
	}
	return forwardPost("PUT", target, "application/json", headers, body)
}

// sendToSplunk posts a single HEC event object.
func sendToSplunk(base, token, index string, entry AuditEntry) error {
	ts, perr := time.Parse(time.RFC3339Nano, entry.TS)
	if perr != nil {
		ts = time.Now()
	}
	body, err := json.Marshal(map[string]interface{}{
		"time":       float64(ts.UnixNano()) / 1e9,
		"sourcetype": "big_michael:audit",
		"event":      entry,
	})
	if err != nil {
		return err
	}
	headers := map[string]string{"Authorization": "Splunk " + token}
	if index != "" {
		headers["X-Splunk-Request-Channel"] = index
	}
	return forwardPost("POST", base+"/services/collector/event", "application/json", headers, body)
}

// sendToWebhook POSTs the entry verbatim as a JSON body.
func sendToWebhook(target, token string, entry AuditEntry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return forwardPost("POST", target, "application/json", headers, body)
}

// ─── HTTP plumbing ────────────────────────────────────────────────────────────

func forwardPost(method, target, contentType string, headers map[string]string, body []byte) error {
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := forwardClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain (capped) so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, forwardMaxRespBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// forwardBaseURL validates an http/https URL and returns its origin (scheme +
// host) without any path, for destinations that have fixed endpoint paths.
func forwardBaseURL(raw, envName string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		slog.Warn("Audit sink skipped: invalid URL", "env", envName)
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// forwardFullURL validates an http/https URL and returns it verbatim.
func forwardFullURL(raw, envName string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		slog.Warn("Audit sink skipped: invalid URL", "env", envName)
		return "", false
	}
	return raw, true
}

// sanitizeForwardErr strips the request URL from transport errors — webhook
// URLs and HEC endpoints can embed tokens, which must never reach the logs.
func sanitizeForwardErr(err error) string {
	if uerr, ok := err.(*url.Error); ok && uerr.Err != nil {
		return uerr.Err.Error()
	}
	return err.Error()
}
