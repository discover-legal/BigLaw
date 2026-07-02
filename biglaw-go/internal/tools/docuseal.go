// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// DocuSeal tools — electronic signature for generated legal documents, ported
// from src/tools/docuseal.ts. DocuSeal (https://www.docuseal.com) is a fully
// open-source, self-hostable e-signature platform (MIT licence).
//
// Self-host: docker run -d -p 3000:3000 docuseal/docuseal
// API key:   Settings → API in the DocuSeal admin panel.
//
// Three tools:
//
//	docuseal_list_templates    — list available templates (for template reuse)
//	docuseal_send_for_signing  — upload a PDF, create template + submission, return signing URLs
//	docuseal_submission_status — check whether all parties have signed
//
// Typical agent workflow:
//  1. pdf_generate → produces /path/to/brief.pdf
//  2. docuseal_send_for_signing(path, signers) → signing URLs per party
//  3. docuseal_submission_status(id) → check status / get signed PDF URL
//
// Unconfigured (no API key) → structured {"error": …} map, never a failure.

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

const docusealMaxFileBytes = 100 * 1024 * 1024 // 100 MB

func docusealNotConfigured() map[string]interface{} {
	return map[string]interface{}{
		"error": "not configured: DOCUSEAL_API_KEY is not set — DocuSeal unavailable",
	}
}

// docusealEnabled reports whether the integration is usable and the endpoint
// URL is a sane http(s) URL.
func (r *Registry) docusealEnabled() bool {
	if !r.cfg.DocuSeal.Enabled || r.cfg.DocuSeal.APIKey == "" {
		return false
	}
	u, err := url.Parse(r.cfg.DocuSeal.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		slog.Warn("DocuSeal URL invalid — DocuSeal disabled", "url", r.cfg.DocuSeal.URL)
		return false
	}
	return true
}

// docusealFetch issues an authenticated request against the DocuSeal API and
// decodes the JSON response. contentType is "" for GET requests.
func (r *Registry) docusealFetch(method, path, contentType string, body io.Reader) (interface{}, error) {
	req, err := http.NewRequest(method, strings.TrimRight(r.cfg.DocuSeal.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Token", r.cfg.DocuSeal.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := string(data)
		if len(text) > 400 {
			text = strutil.Truncate(text, 400)
		}
		return nil, fmt.Errorf("DocuSeal %s %s failed (%d): %s", method, path, resp.StatusCode, text)
	}
	var out interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("failed to decode DocuSeal response: %w", err)
	}
	return out, nil
}

// ─── register ─────────────────────────────────────────────────────────────────

func (r *Registry) registerDocusealTools() {
	r.Register(r.docusealListTemplatesTool())
	r.Register(r.docusealSendForSigningTool())
	r.Register(r.docusealSubmissionStatusTool())
}

// ─── docuseal_list_templates ──────────────────────────────────────────────────

func (r *Registry) docusealListTemplatesTool() *ToolImpl {
	return &ToolImpl{
		Name: "docuseal_list_templates",
		Schema: providers.ToolParam{
			Name: "docuseal_list_templates",
			Description: "List available DocuSeal signing templates. " +
				"Use this to find an existing template ID before calling docuseal_send_for_signing.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"limit": map[string]interface{}{"type": "number", "description": "Maximum templates to return (default 20)"},
				},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if !r.docusealEnabled() {
				return docusealNotConfigured(), nil
			}
			limit := intInput(input, "limit", 20)
			if limit < 1 {
				limit = 1
			}
			if limit > 100 {
				limit = 100
			}
			data, err := r.docusealFetch("GET", fmt.Sprintf("/api/templates?limit=%d", limit), "", nil)
			if err != nil {
				return map[string]interface{}{"error": err.Error()}, nil
			}
			if m, ok := data.(map[string]interface{}); ok {
				if inner, ok := m["data"]; ok {
					return map[string]interface{}{"templates": inner}, nil
				}
			}
			return map[string]interface{}{"templates": data}, nil
		},
	}
}

// ─── docuseal_send_for_signing ────────────────────────────────────────────────

func (r *Registry) docusealSendForSigningTool() *ToolImpl {
	return &ToolImpl{
		Name: "docuseal_send_for_signing",
		Schema: providers.ToolParam{
			Name: "docuseal_send_for_signing",
			Description: "Send a legal document for electronic signing via DocuSeal. " +
				"Either upload a PDF from a local path (generated by pdf_generate) or reference " +
				"an existing template by ID. Returns a submission ID and per-signer signing URLs.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pdfPath":      map[string]interface{}{"type": "string", "description": "Absolute path to the PDF file to send for signing (from pdf_generate output)."},
					"templateId":   map[string]interface{}{"type": "number", "description": "Existing DocuSeal template ID (alternative to uploading a new PDF)."},
					"documentName": map[string]interface{}{"type": "string", "description": "Human-readable name for the document, e.g. 'Mutual NDA 2026'."},
					"signers": map[string]interface{}{
						"type":        "array",
						"description": "List of parties who must sign. At least one required.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"role":  map[string]interface{}{"type": "string", "description": "Signer role, e.g. 'Client', 'Counsel', 'Counterparty'"},
								"name":  map[string]interface{}{"type": "string", "description": "Signer's full name"},
								"email": map[string]interface{}{"type": "string", "description": "Signer's email address"},
							},
						},
					},
					"sendEmail": map[string]interface{}{"type": "boolean", "description": "Whether DocuSeal should email signing links directly. Default false (return URLs instead)."},
					"message":   map[string]interface{}{"type": "string", "description": "Optional custom message included in the signing invitation email."},
				},
				"required": []string{"signers"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if !r.docusealEnabled() {
				return docusealNotConfigured(), nil
			}

			templateID := intInput(input, "templateId", 0)
			sendEmail, _ := input["sendEmail"].(bool)
			documentName := strInput(input, "documentName")
			if documentName == "" {
				documentName = "Legal Document"
			}
			rawSigners, _ := input["signers"].([]interface{})
			if len(rawSigners) > 50 {
				rawSigners = rawSigners[:50]
			}

			// ── Upload PDF as new template if no templateId provided ─────────
			if templateID == 0 {
				pdfPath := strInput(input, "pdfPath")
				if pdfPath == "" {
					return nil, fmt.Errorf("either pdfPath or templateId must be provided")
				}
				safePath, err := r.safeReadPath(pdfPath)
				if err != nil {
					return nil, err
				}
				info, err := os.Stat(safePath)
				if err != nil {
					return map[string]interface{}{"error": fmt.Sprintf("could not read file: %s", err)}, nil
				}
				if info.Size() > docusealMaxFileBytes {
					return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), docusealMaxFileBytes)
				}
				fileBytes, err := os.ReadFile(safePath)
				if err != nil {
					return map[string]interface{}{"error": fmt.Sprintf("could not read file: %s", err)}, nil
				}

				var form bytes.Buffer
				mw := multipart.NewWriter(&form)
				if err := mw.WriteField("name", documentName); err != nil {
					return nil, err
				}
				fw, err := mw.CreateFormFile("file", filepath.Base(safePath))
				if err != nil {
					return nil, err
				}
				if _, err := fw.Write(fileBytes); err != nil {
					return nil, err
				}
				if err := mw.Close(); err != nil {
					return nil, err
				}
				slog.Debug("Uploading PDF to DocuSeal", "pdfPath", safePath)
				tmplRaw, err := r.docusealFetch("POST", "/api/templates", mw.FormDataContentType(), &form)
				if err != nil {
					return map[string]interface{}{"error": err.Error()}, nil
				}
				if m, ok := tmplRaw.(map[string]interface{}); ok {
					if id, ok := m["id"].(float64); ok {
						templateID = int(id)
					}
				}
				if templateID == 0 {
					return map[string]interface{}{"error": "DocuSeal template upload returned no id"}, nil
				}
				slog.Debug("DocuSeal template created", "templateId", templateID)
			}

			// ── Create submission ─────────────────────────────────────────────
			submitters := make([]map[string]interface{}, 0, len(rawSigners))
			for _, rs := range rawSigners {
				s, _ := rs.(map[string]interface{})
				if s == nil {
					continue
				}
				role := strInput(s, "role")
				if role == "" {
					role = "Signer"
				}
				submitters = append(submitters, map[string]interface{}{
					"role":  role,
					"name":  strInput(s, "name"),
					"email": strInput(s, "email"),
				})
			}
			payload := map[string]interface{}{
				"template_id": templateID,
				"send_email":  sendEmail,
				"submitters":  submitters,
			}
			if msg := strInput(input, "message"); msg != "" {
				payload["message"] = msg
			}
			body, _ := json.Marshal(payload)
			subRaw, err := r.docusealFetch("POST", "/api/submissions", "application/json", bytes.NewReader(body))
			if err != nil {
				return map[string]interface{}{"error": err.Error()}, nil
			}

			sub, _ := subRaw.(map[string]interface{})
			if sub == nil {
				// Some DocuSeal versions return the submitter list directly.
				if arr, ok := subRaw.([]interface{}); ok {
					sub = map[string]interface{}{"submitters": arr}
				} else {
					return map[string]interface{}{"error": "unexpected DocuSeal submission response"}, nil
				}
			}
			var signingLinks []map[string]interface{}
			if subs, ok := sub["submitters"].([]interface{}); ok {
				for _, rs := range subs {
					s, _ := rs.(map[string]interface{})
					if s == nil {
						continue
					}
					var signingURL interface{}
					if v, ok := s["embed_src"]; ok {
						signingURL = v
					}
					signingLinks = append(signingLinks, map[string]interface{}{
						"name":       s["name"],
						"email":      s["email"],
						"role":       s["role"],
						"status":     s["status"],
						"signingUrl": signingURL,
					})
				}
			}
			slog.Info("DocuSeal submission created", "submissionId", sub["id"], "signers", len(signingLinks))
			return map[string]interface{}{
				"submissionId": sub["id"],
				"status":       sub["status"],
				"templateId":   templateID,
				"signingLinks": signingLinks,
			}, nil
		},
	}
}

// ─── docuseal_submission_status ───────────────────────────────────────────────

func (r *Registry) docusealSubmissionStatusTool() *ToolImpl {
	return &ToolImpl{
		Name: "docuseal_submission_status",
		Schema: providers.ToolParam{
			Name: "docuseal_submission_status",
			Description: "Check the signing status of a DocuSeal submission. " +
				"Returns per-signer status and, when fully signed, the URL of the completed document.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"submissionId": map[string]interface{}{"type": "number", "description": "DocuSeal submission ID returned by docuseal_send_for_signing"},
				},
				"required": []string{"submissionId"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if !r.docusealEnabled() {
				return docusealNotConfigured(), nil
			}
			id := intInput(input, "submissionId", 0)
			if id <= 0 {
				return nil, fmt.Errorf("submissionId must be a positive integer")
			}
			raw, err := r.docusealFetch("GET", fmt.Sprintf("/api/submissions/%d", id), "", nil)
			if err != nil {
				return map[string]interface{}{"error": err.Error()}, nil
			}
			data, _ := raw.(map[string]interface{})
			if data == nil {
				return map[string]interface{}{"error": "unexpected DocuSeal response"}, nil
			}

			allSigned := true
			var signers []map[string]interface{}
			if subs, ok := data["submitters"].([]interface{}); ok {
				for _, rs := range subs {
					s, _ := rs.(map[string]interface{})
					if s == nil {
						continue
					}
					status, _ := s["status"].(string)
					if status != "completed" {
						allSigned = false
					}
					signers = append(signers, map[string]interface{}{
						"name":        s["name"],
						"email":       s["email"],
						"role":        s["role"],
						"status":      s["status"],
						"completedAt": s["completed_at"],
					})
				}
			} else {
				allSigned = false
			}
			docs := data["documents"]
			if docs == nil {
				docs = []interface{}{}
			}
			return map[string]interface{}{
				"submissionId":    data["id"],
				"status":          data["status"],
				"allSigned":       allSigned,
				"completedAt":     data["completed_at"],
				"signers":         signers,
				"signedDocuments": docs,
			}, nil
		},
	}
}
