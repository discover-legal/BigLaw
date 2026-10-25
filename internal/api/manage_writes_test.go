// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/engine"
)

// TestManageWriteRoutes pins every Manage write route (PR #59 r1 L4, from the review's
// differential probe): a viewer is refused 403 forbidden and a cross-site write 403 cross_origin
// before anything is read; a body that is not exactly one object of the route's fields (unknown
// field, malformed, empty, not an object, data after it, over 4 MiB) is 400; none of that is
// stored; and each accepted write is in the change log.
func TestManageWriteRoutes(t *testing.T) {
	t.Parallel()
	manageWrites(t, manageServer(t, func(*http.Request) Principal { return Principal{ID: "v", Name: "Viewer"} }), manageServer(t, local))
}

// manageWrites drives every Manage write route as viewer (refused) and as s, an admin; it returns
// the change log after the admin's writes.
func manageWrites(t *testing.T, viewer, s *server) []engine.Version {
	t.Helper()
	skill := ""
	for _, it := range engine.Builtins() {
		if it.Section == "skills" {
			skill = it.ID
			break
		}
	}
	if skill == "" {
		t.Skip("no built-in skill ships in this build")
		return nil
	}
	send := func(srv *server, path, body string, hdr ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := srv.client().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	routes := []struct {
		path string
		body bool // the route decodes a body
	}{
		{"/manage/items/prompt.lead/versions", true}, {"/manage/items/prompt.lead/revert", true}, {"/manage/items/" + skill + "/delete", true},
		{"/manage/secrets", true}, {"/manage/items/server.x/import", true}, {"/manage/items/tool.x/test", true},
		{"/manage/secrets/x/delete", false}, {"/manage/items/server.x/discover", false},
	}
	for _, r := range routes {
		if code, body := send(viewer, r.path, `{}`); code != 403 || !strings.Contains(body, `"errorType":"forbidden"`) {
			t.Errorf("%s as a viewer: %d %s", r.path, code, body)
		}
		for _, h := range [][]string{{"Origin", "https://evil.example"}, {"Sec-Fetch-Site", "cross-site"}} {
			if code, body := send(s, r.path, `{}`, h...); code != 403 || !strings.Contains(body, `"errorType":"cross_origin"`) {
				t.Errorf("%s with %s: %d %s", r.path, h[0], code, body)
			}
		}
		if !r.body {
			continue
		}
		for _, b := range []struct{ name, body, want string }{
			{"unknown field", `{"zzz":1}`, `unknown field \"zzz\"`}, {"malformed", `{`, "unexpected EOF"}, {"empty", ``, "EOF"},
			{"not an object", `[]`, "cannot unmarshal array"}, {"trailing data", `{} {"zzz":1}`, "more after the object"},
			{"oversize", `{"note":"` + strings.Repeat("a", 4<<20+10) + `"}`, "request body too large"},
		} {
			code, body := send(s, r.path, b.body)
			if code != 400 || !strings.Contains(body, `"errorType":"bad_request"`) || !strings.Contains(body, "the body is not the expected JSON object") || !strings.Contains(body, b.want) {
				t.Errorf("%s, %s: %d %s", r.path, b.name, code, body)
			}
		}
	}
	for _, srv := range []*server{viewer, s} {
		if code, body := srv.do("GET", "/manage/changes", nil); code != 200 || body != "[]\n" {
			t.Fatalf("a refused write was stored: %d %s", code, body)
		}
	}

	files, _ := json.Marshal(leadFiles(t, s, "\nWrites."))
	for _, w := range []struct {
		path, body string
		code       int
	}{
		{"/manage/items/prompt.lead/versions", `{"base":0,"files":` + string(files) + `,"note":"n"}`, 201},
		{"/manage/items/prompt.lead/revert", `{"to":0,"base":1,"note":"r"}`, 201},
		{"/manage/items/" + skill + "/delete", `{"base":0,"note":"d"}`, 201},
		{"/manage/secrets", `{"name":"k1","value":"a-long-value","origin":"https://api.example.com"}`, 201},
		{"/manage/secrets/k1/delete", ``, 200},
	} {
		if code, body := send(s, w.path, w.body); code != w.code {
			t.Fatalf("%s: %d %s", w.path, code, body)
		}
	}
	_, body := s.do("GET", "/manage/changes", nil)
	var vs []engine.Version
	if err := json.Unmarshal([]byte(body), &vs); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vs {
		got[v.Action] = v.Item
	}
	for action, item := range map[string]string{"save": "prompt.lead", "revert": "prompt.lead", "delete": skill, "secret set": "secret.k1", "secret deleted": "secret.k1"} {
		if got[action] != item {
			t.Errorf("the change log has no %s of %s: %s", action, item, body)
		}
	}
	return vs
}
