// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// REST API server for BigLaw Go — wraps all subsystems behind a Gin router.
// Auth: when cfg.Auth.Enabled is false every request is treated as LocalPartner.
// When enabled, browser sessions and bearer credentials are bound to profiles.

package api

import (
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/blob"
	"github.com/discover-legal/biglaw-go/internal/budget"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/dockets"
	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/regulatory"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/gin-gonic/gin"
)

const ctxUserKey = "user"

// Server holds all subsystem references and owns the Gin engine.
type Server struct {
	cfg        *config.Config
	orch       *orchestrator.Orchestrator
	provReg    *providers.Registry
	profiles   *auth.ProfileStore
	clients    *clients.ClientStore
	time       *timekeeping.TimeStore
	knowledge  *knowledge.Store
	registry   *agents.Registry
	costs      *cost.Store
	reviews    store.ReviewRepository // durable tabular-review matrices; nil if unavailable
	blobs      blob.Store             // attachment bytes (disk now, object-store later); nil if unavailable
	graph      *graph.Client
	budget     *budget.Monitor     // read-only burn for bot commands
	dockets    *dockets.Monitor    // set by AttachDockets; nil when disabled
	regulatory *regulatory.Monitor // set by AttachRegulatory; nil when disabled
	lpm        *lpm.Service        // set by AttachLPM; nil when LPM is disabled
	router     *gin.Engine
	started    time.Time
}

// New creates a Server, registers all routes, and returns it.
// Call Run to start listening.
func New(
	cfg *config.Config,
	orch *orchestrator.Orchestrator,
	provReg *providers.Registry,
	profiles *auth.ProfileStore,
	clientStore *clients.ClientStore,
	timeStore *timekeeping.TimeStore,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	costStore *cost.Store,
	reviewRepo store.ReviewRepository,
) *Server {
	s := &Server{
		cfg:       cfg,
		orch:      orch,
		provReg:   provReg,
		profiles:  profiles,
		clients:   clientStore,
		time:      timeStore,
		knowledge: knowledgeStore,
		registry:  registry,
		costs:     costStore,
		reviews:   reviewRepo,
		blobs:     newBlobStore(cfg),
		graph:     graph.New(),
		started:   time.Now(),
	}
	s.budget = budget.NewMonitor(apiBudgetTime{timeStore}, apiBudgetClients{clientStore}, nil)

	// Push the current roster into the TypeDB conflict graph if the sidecar
	// is up. Best-effort: the substring conflict check works without it.
	go s.syncGraph()

	s.router = s.newRouter()

	return s
}
