// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/regulatory"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// /regulatory/status is readable by any signed-in user (it says whether the
// pulse is on and whether the caller may read alerts) and has no side
// effects: a Server with no orchestrator must not be touched.
func TestRegulatoryStatusForAnyRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	get := func(s *Server, role types.LawyerRole) map[string]bool {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(ctxUserKey, &types.SessionUser{ProfileID: "u", Role: role}); c.Next() })
		r.GET("/regulatory/status", s.handleRegulatoryStatus)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/regulatory/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("role %s: status %d %s", role, w.Code, w.Body)
		}
		var out map[string]bool
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	t.Setenv("REG_PULSE_ENABLED", "")
	off := &Server{cfg: &config.Config{}}
	if got := get(off, types.RoleLawyer); got["enabled"] || got["canView"] {
		t.Errorf("lawyer, pulse off = %v", got)
	}

	t.Setenv("REG_PULSE_ENABLED", "true")
	t.Setenv("TAVILY_API_KEY", "k")
	on := &Server{cfg: &config.Config{}, regulatory: regulatory.New(nil, "light")}
	if got := get(on, types.RoleLawyer); !got["enabled"] || got["canView"] {
		t.Errorf("lawyer, pulse on = %v", got)
	}
	if got := get(on, types.RolePartner); !got["enabled"] || !got["canView"] {
		t.Errorf("partner, pulse on = %v", got)
	}
	opsStatesMu.Lock()
	_, built := opsStates[on]
	opsStatesMu.Unlock()
	if built {
		t.Error("status call built the ops monitor state")
	}
}
