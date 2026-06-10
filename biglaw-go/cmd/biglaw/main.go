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

	"github.com/discover-legal/biglaw-go/internal/adapters"
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
	"github.com/discover-legal/biglaw-go/internal/mcp"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/tools"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func main() {
	// Load .env if present (silently ignore missing file).
	_ = godotenv.Load()

	cfg := config.Load()

	// With auth enabled, the REST API is gated by a bearer token; a profile
	// header alone is a claim, not a credential.
	if cfg.Auth.Enabled && cfg.API.APIKey == "" {
		fmt.Fprintln(os.Stderr, "config: AUTH_ENABLED=true requires API_KEY (REST bearer token) to be set")
		os.Exit(1)
	}

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

	// Build template store and load from filesystem. Lavern and MikeOSS
	// workflow files have their own shapes — use the adapter loaders rather
	// than parsing them as raw TaskTemplates.
	templatesStore := templates.NewStore()
	_ = templatesStore.Load("templates")
	if ts, err := adapters.LoadLavernWorkflows("workflows/laverne"); err == nil {
		for _, t := range ts {
			templatesStore.Add(t)
		}
	}
	if ts, err := adapters.LoadMikeOSSWorkflows("workflows/mikeoss"); err == nil {
		for _, t := range ts {
			templatesStore.Add(t)
		}
	}

	// Load external JSON plugin adapters and Lavern agent configs.
	pluginReg := adapters.New()
	if err := pluginReg.LoadDirectory("adapters/external"); err != nil {
		fmt.Fprintf(os.Stderr, "plugin adapters: %v\n", err)
	}
	for _, t := range pluginReg.TaskTemplates() {
		templatesStore.Add(t)
	}
	allAgents := append([]types.AgentDefinition{}, agents.ALL_AGENT_DEFINITIONS...)
	allAgents = append(allAgents, pluginReg.AgentDefinitions()...)
	if lavernAgents, err := adapters.LoadLavernAgents("agents/lavern"); err == nil {
		allAgents = append(allAgents, lavernAgents...)
	}

	// Build settings, profiles, clients, time stores.
	settingsStore := settings.NewSettingsStore(cfg.Persistence.SettingsFile)
	profileStore := auth.NewProfileStore(cfg)
	clientStore := clients.NewClientStore()
	timeStore := timekeeping.NewTimeStore()

	// Build learning engine.
	learningEngine := learning.Default

	// Build tool registry.
	toolReg := tools.NewRegistry(cfg, provReg, costStore)

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
		toolReg,
		agents.ROOT_ORCHESTRATOR,
	)

	if err := orch.Init(allAgents); err != nil {
		fmt.Fprintf(os.Stderr, "orchestrator init: %v\n", err)
		os.Exit(1)
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
		apiSrv := api.New(cfg, orch, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore)
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
		apiSrv := api.New(cfg, orch, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore)
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
		apiSrv := api.New(cfg, orch, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore)
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
