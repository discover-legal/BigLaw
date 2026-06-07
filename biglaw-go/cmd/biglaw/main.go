// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// biglaw — Go port of the Big Michael multi-agent legal AI platform.
// Targets Raspberry Pi / ARM64 SBCs (4 GB RAM).
//
// Run modes (BIG_MICHAEL_MODE env var):
//   auto       — own the DB + REST if no other process is running, else MCP client
//   backend    — own DB + REST, never MCP
//   mcp        — pure MCP server (no REST, no DB ownership)
//   standalone — own DB + REST + MCP on stdio

package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/api"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/mcp"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/queue"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/tools"
)

func main() {
	// Load .env if present (silently ignore missing file).
	_ = godotenv.Load()

	cfg := config.Load()

	// Initialise audit logger.
	audit.Init(cfg.Audit.LogFile, cfg.Audit.Enabled)

	// Build provider registry.
	provReg := providers.NewRegistry(cfg)

	// Build cost store.
	costStore := cost.Default
	if err := costStore.Init(cfg.Persistence.CostFile); err != nil {
		fmt.Fprintf(os.Stderr, "cost init: %v\n", err)
		os.Exit(1)
	}

	// Build embeddings client.
	embedC := embeddings.NewClient(cfg)

	// Build agent registry.
	agentReg := agents.NewRegistry(embedC, cfg.VectorDB.DataDir)

	// Build inter-round memory store.
	memStore := memory.NewInterRound(embedC)

	// Build knowledge store.
	knowledgeStore := knowledge.NewStore(embedC)

	// Build template store and load from filesystem.
	templatesStore := templates.NewStore()
	_ = templatesStore.Load("templates", "workflows/mikeoss", "workflows/laverne")

	// Build settings, profiles, clients, time stores.
	settingsStore := settings.NewSettingsStore(cfg.Persistence.SettingsFile)
	profileStore := auth.NewProfileStore(cfg)
	clientStore := clients.NewClientStore()
	timeStore := timekeeping.NewTimeStore()

	// Build learning engine.
	learningEngine := learning.Default

	// Build tool registry.
	toolReg := tools.NewRegistry(cfg, provReg, costStore)

	// Build knowledge and memory adapters (bridge to agents.KnowledgeStore / agents.MemoryStore).
	knowledgeAdapter := knowledge.NewAdapter(knowledgeStore)
	memAdapter := memory.NewAdapter(memStore)

	// Wire the tool registry's knowledge/memory dependencies via the agents package interface.
	// (The tool registry uses these indirectly via agent tool calls.)
	_ = knowledgeAdapter
	_ = memAdapter
	_ = toolReg

	// Build orchestrator.
	orch := orchestrator.New(
		cfg,
		provReg,
		costStore,
		embedC,
		agentReg,
		memStore,
		knowledgeStore,
		templatesStore,
		settingsStore,
		profileStore,
		clientStore,
		timeStore,
		learningEngine,
		agents.ROOT_ORCHESTRATOR,
	)

	if err := orch.Init(agents.ALL_AGENT_DEFINITIONS); err != nil {
		fmt.Fprintf(os.Stderr, "orchestrator init: %v\n", err)
		os.Exit(1)
	}

	// Build the LPM service (daily status-report spine) when enabled. It owns a
	// durable queue, a daily scheduler, and a background worker.
	var lpmSvc *lpm.Service
	if cfg.LPM.Enabled {
		lpmQueue := queue.New(cfg.Persistence.JobsFile)
		if err := lpmQueue.Init(); err != nil {
			fmt.Fprintf(os.Stderr, "lpm queue init: %v\n", err)
		}
		model := cfg.LPM.Model
		if model == "" {
			// Route to the low-power tier (Haiku / Ollama / local) for the box.
			model = routing.SelectModel(cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
		}
		if prov, err := provReg.Get(model); err != nil {
			fmt.Fprintf(os.Stderr, "lpm provider: %v\n", err)
		} else {
			gen := lpm.NewGenerator(prov, model)
			corpus := lpm.NewCorpus(cfg.LPM.CorpusFile)
			data := newLPMDataProvider(orch, timeStore)
			lpmSvc = lpm.NewService(cfg.LPM, gen, corpus, data, lpmQueue, nil)

			// Phase 2: email intake + matter routing when a mail provider is set.
			if cfg.Email.Graph.Enabled || cfg.Email.Gmail.Enabled {
				routed := lpm.NewRoutedStore(cfg.LPM.RoutedFile)
				if err := routed.Init(); err != nil {
					fmt.Fprintf(os.Stderr, "lpm routed store init: %v\n", err)
				}
				router := lpm.NewRouter(prov, model, cfg.LPM.RouteMinConf)
				intake := lpm.NewIntake(lpm.IntakeConfig{
					IntakeMode:  cfg.LPM.IntakeMode,
					SharedInbox: cfg.LPM.SharedInbox,
					IntervalMin: cfg.LPM.PollIntervalM,
				}, nil, router, routed, data.MatterOptions)
				lpmSvc.WithEmailIntake(intake, routed)
			}

			// Outbound drafting (email-write-mode), guard-enforced. Default "off".
			transport := lpm.NewTransport(
				cfg.Email.Graph.Enabled, cfg.Email.Gmail.Enabled,
				cfg.Email.Graph.UserEmail, cfg.Email.Gmail.UserEmail,
			)
			lpmSvc.WithDrafting(cfg.LPM.EmailWriteMode, cfg.LPM.AllowedDomains, transport, nil)

			lpmSvc.Start()
			defer lpmSvc.Stop()
		}
	}

	// makeAPI builds the REST server and attaches optional LPM routes.
	makeAPI := func() *api.Server {
		srv := api.New(cfg, orch, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore)
		srv.AttachLPM(lpmSvc)
		return srv
	}

	mode := os.Getenv("BIG_MICHAEL_MODE")
	if mode == "" {
		mode = "auto"
	}

	var wg sync.WaitGroup
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	switch mode {
	case "mcp":
		// Pure MCP mode — serve stdio only.
		mcpSrv := mcp.New(orch, knowledgeStore, agentReg)
		if err := mcpSrv.Serve(); err != nil {
			fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
			os.Exit(1)
		}

	case "backend":
		// REST API only.
		addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)
		apiSrv := makeAPI()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := apiSrv.Run(addr); err != nil {
				fmt.Fprintf(os.Stderr, "api run: %v\n", err)
			}
		}()
		<-shutdown

	case "standalone":
		// REST API + MCP stdio.
		addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)
		apiSrv := makeAPI()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := apiSrv.Run(addr); err != nil {
				fmt.Fprintf(os.Stderr, "api run: %v\n", err)
			}
		}()
		mcpSrv := mcp.New(orch, knowledgeStore, agentReg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mcpSrv.Serve(); err != nil {
				fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
			}
		}()
		<-shutdown

	default: // "auto"
		// Default: run REST API (ARM devices are always "backend").
		addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)
		apiSrv := makeAPI()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := apiSrv.Run(addr); err != nil {
				fmt.Fprintf(os.Stderr, "api run: %v\n", err)
			}
		}()
		// If stdin is not a TTY, also serve MCP on stdio.
		fi, _ := os.Stdin.Stat()
		if fi.Mode()&os.ModeCharDevice == 0 {
			mcpSrv := mcp.New(orch, knowledgeStore, agentReg)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := mcpSrv.Serve(); err != nil {
					fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
				}
			}()
		}
		<-shutdown
	}

	wg.Wait()
	fmt.Println("biglaw: shutdown complete")
}
