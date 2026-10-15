// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Contract test: the JSON Schemas in docs/integration/schemas/intake/v1 —
// not shared code — are the integration surface with the client portal.
// Requests the portal sends must validate against the request schemas, and
// every response BigLaw actually produces on the portal routes must validate
// against the response schemas. The tolerant-reader rules (unknown fields
// ignored, unknown enum values mapped to safe defaults) are pinned here too.

package intake

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/discover-legal/biglaw-go/internal/crm"
)

const schemaDir = "../../../docs/integration/schemas/intake/v1"
const schemaBase = "https://schemas.discover.legal/biglaw/intake/v1/"

func loadSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.schema.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no schemas in %s: %v", schemaDir, err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(fh)
		fh.Close()
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := c.AddResource(schemaBase+filepath.Base(f), doc); err != nil {
			t.Fatalf("add %s: %v", f, err)
		}
	}
	out := map[string]*jsonschema.Schema{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".schema.json")
		sch, err := c.Compile(schemaBase + filepath.Base(f))
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
		out[name] = sch
	}
	return out
}

func validate(t *testing.T, schemas map[string]*jsonschema.Schema, name string, raw []byte) error {
	t.Helper()
	sch, ok := schemas[name]
	if !ok {
		t.Fatalf("no schema %q", name)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: response is not JSON: %v (%s)", name, err, raw)
	}
	return sch.Validate(v)
}

func mustValidate(t *testing.T, schemas map[string]*jsonschema.Schema, name string, raw []byte) {
	t.Helper()
	if err := validate(t, schemas, name, raw); err != nil {
		t.Fatalf("%s does not satisfy %s.schema.json: %v\n%s", "payload", name, err, raw)
	}
}

// Example requests as the portal sends them (the contract doc's examples,
// plus the v1 additions).
var exampleSubmission = []byte(`{
  "externalId": "doc-4711",
  "client": {"externalId": "auth0|abc123", "email": "client@example.com", "name": "Dana Client"},
  "title": "Affidavit of Dana Client",
  "documentType": "affidavit",
  "matterType": "DIVORCE",
  "jurisdiction": "US-CA",
  "practiceArea": "Family",
  "noslegal": {"areaOfLaw": "Family", "workType": "Litigious"},
  "adverseParties": [{"name": "Robert Client", "role": "spouse"}],
  "summary": "Client seeks dissolution",
  "content": "I, Dana Client, declare...",
  "facts": [{"category": "goal", "predicate": "custody", "value": "Primary custody of both children"}],
  "metadata": {"portalVersion": "5.1.0"}
}`)

func TestContractRequestExamplesValidate(t *testing.T) {
	schemas := loadSchemas(t)
	mustValidate(t, schemas, "submission-request", exampleSubmission)
	mustValidate(t, schemas, "submission-request",
		[]byte(`{"externalId":"d","client":{"externalId":"auth0|x"},"content":"x"}`)) // minimal
	mustValidate(t, schemas, "proposals-request",
		[]byte(`{"client":{"email":"a@b.c","name":"A"},"facts":[{"category":"contact","predicate":"phone","value":"+1 555 0100","note":""}]}`))
	mustValidate(t, schemas, "decision-request",
		[]byte(`{"clientExternalId":"auth0|abc123","decision":"approve","note":""}`))
	// Tolerant reader: unknown fields and unknown categories are valid input.
	mustValidate(t, schemas, "submission-request",
		[]byte(`{"externalId":"d","client":{"externalId":"x","futureField":1},"content":"x","facts":[{"category":"brand_new_category","value":"v"}],"v2Only":{"a":1}}`))
	// ...but the schemas still catch real breakage.
	for _, bad := range []string{
		`{"externalId":"d","client":{"externalId":"x"}}`,                    // no content
		`{"externalId":"d","content":"x"}`,                                   // no client
		`{"externalId":"d","client":{"externalId":"x"},"content":"x","adverseParties":[{"role":"spouse"}]}`, // party w/o name
	} {
		if err := validate(t, schemas, "submission-request", []byte(bad)); err == nil {
			t.Errorf("schema accepted a broken request: %s", bad)
		}
	}
	if err := validate(t, schemas, "decision-request", []byte(`{"clientExternalId":"x","decision":"maybe"}`)); err == nil {
		t.Error("schema accepted an unknown decision")
	}
}

// contractServer mounts the module over a real (in-memory) store with
// knowledge ingest, exactly as the composition root does.
func contractServer(t *testing.T) (*gin.Engine, *crm.Service, *Service) {
	t.Helper()
	svc, crmSvc, cs, _ := newTestIntake(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewHTTP(svc, crmSvc, cs, nil, HTTPConfig{HMACSecret: testIntakeSecret}).Mount(r, fakeHost{})
	if _, err := cs.Create("Existing Client Robert", "C-9", nil, ""); err != nil {
		t.Fatal(err)
	}
	return r, crmSvc, svc
}

func signedDo(r http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Intake-Timestamp", ts)
	req.Header.Set("X-Intake-Signature", Sign(testIntakeSecret, method, path, ts, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestContractResponsesValidate(t *testing.T) {
	schemas := loadSchemas(t)
	r, crmSvc, _ := contractServer(t)

	// 1. A clean submission (the doc example, plus unknown fields).
	var body map[string]interface{}
	_ = json.Unmarshal(exampleSubmission, &body)
	body["someV2Field"] = map[string]interface{}{"x": 1}
	raw, _ := json.Marshal(body)
	w := signedDo(r, http.MethodPost, "/intake/submissions", raw)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	mustValidate(t, schemas, "submission-response", w.Body.Bytes())
	var created struct {
		Submission struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"submission"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Submission.Status != StatusReceived {
		t.Fatalf("clean submission status = %q", created.Submission.Status)
	}

	// 2. A held submission (adverse party is an existing client) — the
	// prospect has no client number / document yet, and still validates.
	held := []byte(`{"externalId":"doc-9","client":{"externalId":"auth0|pat","name":"Pat Prospect"},"content":"draft","adverseParties":[{"name":"Existing Client Robert"}]}`)
	w = signedDo(r, http.MethodPost, "/intake/submissions", held)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"conflict_hold"`) {
		t.Fatalf("held submit: %d %s", w.Code, w.Body.String())
	}
	mustValidate(t, schemas, "submission-response", w.Body.Bytes())

	// 3. Reads.
	w = signedDo(r, http.MethodGet, "/intake/submissions/"+created.Submission.ID, nil)
	mustValidate(t, schemas, "submission-response", w.Body.Bytes())
	w = signedDo(r, http.MethodGet, "/intake/clients/auth0%7Cabc123/submissions", nil)
	mustValidate(t, schemas, "submission-list-response", w.Body.Bytes())
	w = signedDo(r, http.MethodGet, "/intake/clients/auth0%7Cpat/submissions", nil)
	mustValidate(t, schemas, "submission-list-response", w.Body.Bytes())

	// 4. Client proposals (with an unknown category → coerced to "note").
	w = signedDo(r, http.MethodPost, "/intake/clients/auth0%7Cabc123/proposals",
		[]byte(`{"client":{"email":"client@example.com","name":"Dana Client"},"facts":[{"category":"contact","predicate":"phone","value":"+1 555 0100"},{"category":"brand_new_category","value":"tolerated"}]}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("proposals: %d %s", w.Code, w.Body.String())
	}
	mustValidate(t, schemas, "proposals-response", w.Body.Bytes())
	if !strings.Contains(w.Body.String(), `"category":"note"`) {
		t.Fatalf("unknown category not mapped to note: %s", w.Body.String())
	}

	// 5. A lawyer proposal decided by the client through the portal.
	ctx := sysCtx()
	profile, _, _ := crmSvc.GetByExternalRef(ctx, "auth0|abc123")
	props, err := crmSvc.Propose(ctx, profile.ID, crm.Actor{Role: crm.RoleLawyer, ID: "lawyer-1"}, "lawyer",
		[]crm.FactInput{{Category: "matter", Predicate: "court", Value: "Superior Court, Los Angeles"}})
	if err != nil {
		t.Fatal(err)
	}
	w = signedDo(r, http.MethodPost, "/intake/proposals/"+props[0].ID+"/decision",
		[]byte(`{"clientExternalId":"auth0|abc123","decision":"approve","note":""}`))
	if w.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", w.Code, w.Body.String())
	}
	mustValidate(t, schemas, "decision-response", w.Body.Bytes())

	// 6. Profile view.
	w = signedDo(r, http.MethodGet, "/intake/clients/auth0%7Cabc123/profile", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	mustValidate(t, schemas, "profile-response", w.Body.Bytes())
	w = signedDo(r, http.MethodGet, "/intake/clients/auth0%7Cpat/profile", nil) // prospect
	mustValidate(t, schemas, "profile-response", w.Body.Bytes())

	// 7. Errors.
	for _, w := range []*httptest.ResponseRecorder{
		signedDo(r, http.MethodGet, "/intake/submissions/nope", nil),
		signedDo(r, http.MethodGet, "/intake/clients/auth0%7Cnobody/profile", nil),
		signedDo(r, http.MethodPost, "/intake/submissions", []byte(`{"externalId":"x"}`)),
		signedDo(r, http.MethodPost, "/intake/proposals/nope/decision", []byte(`{"clientExternalId":"x","decision":"approve"}`)),
	} {
		if w.Code < 400 {
			t.Fatalf("expected an error, got %d %s", w.Code, w.Body.String())
		}
		mustValidate(t, schemas, "error", w.Body.Bytes())
	}
}
