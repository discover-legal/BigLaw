// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// A queued / failed / just-submitted task has no rounds, findings or gates
// yet. Go holds those as nil slices; the wire contract is that they still
// arrive as [] — the workbench crashed on `null.length` before this.
func TestResponsesNeverEncodeNilSlicesAsNull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/task", func(c *gin.Context) {
		c.JSON(http.StatusOK, &types.Task{ID: "t1", Status: types.TaskStatusFailed})
	})
	r.GET("/headnotes", func(c *gin.Context) { c.JSON(http.StatusOK, types.HeadnoteReport{}) })
	r.GET("/list", func(c *gin.Context) {
		var none []types.Task
		c.JSON(http.StatusOK, none)
	})
	r.GET("/wrapped", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"results": []types.Task(nil)})
	})

	get := func(path string) []byte {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		return w.Body.Bytes()
	}

	var task map[string]any
	if err := json.Unmarshal(get("/task"), &task); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"rounds", "findings", "pendingGates", "activeAgentIds", "documentIds"} {
		if _, ok := task[f].([]any); !ok {
			t.Errorf("task.%s = %#v, want []", f, task[f])
		}
	}

	var hn map[string]any
	if err := json.Unmarshal(get("/headnotes"), &hn); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"headnotes", "practiceAreas", "relatedPrinciples"} {
		if _, ok := hn[f].([]any); !ok {
			t.Errorf("headnotes.%s = %#v, want []", f, hn[f])
		}
	}

	if got := string(get("/list")); got != "[]" {
		t.Errorf("nil list body = %s, want []", got)
	}
	if got := string(get("/wrapped")); got != `{"results":[]}` {
		t.Errorf("wrapped nil list = %s", got)
	}
}
