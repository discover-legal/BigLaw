// SPDX-License-Identifier: Apache-2.0
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
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/api"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
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
	"github.com/discover-legal/biglaw-go/internal/rag"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/secrets"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/tools"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func main() {
	// `biglaw demo` — self-contained 60-second showcase (see demo.go). Runs
	// its own minimal wiring and exits; the normal server path is untouched.
	if demoRequested(os.Args) {
		os.Exit(runDemo())
	}

	// Load .env if present (silently ignore missing file), then overlay
	// Infisical-managed secrets (mirrors the TS entry point: dotenv →
	// Infisical → config). No-op when INFISICAL_* vars are absent.
	_ = godotenv.Load()
	if addr := os.Getenv("PPROF"); addr != "" {
		if err := startLocalPprof(addr); err != nil {
			slog.Warn("pprof disabled", "error", err)
		}
	}
	secrets.Load()

	cfg := config.Load()

	// Self-imposed vendor breaker: refuse to start if the config couples the
	// system to Anthropic or AWS without an explicit opt-in (ALLOW_ANTHROPIC /
	// ALLOW_AWS). Using those services must be a deliberate, active effort.
	if err := config.GuardVendors(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if disarmed := config.DisarmedVendorBreakers(); len(disarmed) > 0 {
		slog.Warn("vendor breaker DISARMED by operator — this build otherwise ships free of these vendors",
			"overrides", disarmed)
	}

	if err := config.ValidateSecurity(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
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
	defer costStore.Close() // flush the queued cost ledger on shutdown

	// Build embeddings client.
	embedC := embeddings.NewClient(cfg)

	// Build agent registry.
	agentReg := agents.NewRegistry(embedC, cfg.VectorDB.DataDir)

	// Build inter-round memory store.
	memStore := memory.NewInterRound(embedC)

	// Build the durable document repository (SQLite by default; Postgres when
	// DATABASE_URL is set) and wire it into the knowledge store, then hydrate
	// the in-memory vector index from persisted documents.
	docRepo, err := store.Open(cfg)
	if err != nil {
		slog.Error("failed to open document store", "err", err)
		os.Exit(1)
	}
	defer docRepo.Close()
	knowledgeStore := knowledge.NewStoreWithRepo(embedC, docRepo)
	if err := knowledgeStore.Load(); err != nil {
		slog.Warn("knowledge store load failed; continuing empty", "err", err)
	}

	// Build template store and load from filesystem. Lavern workflow files
	// have their own shape — use the adapter loader rather than parsing them
	// as raw TaskTemplates.
	templatesStore := templates.NewStore()
	_ = templatesStore.Load("templates")
	if ts, err := adapters.LoadLavernWorkflows("workflows/laverne"); err == nil {
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
	settingsStore := settings.NewSettingsStore(cfg, cfg.Persistence.SettingsFile)
	profileStore := auth.NewProfileStore(cfg)
	clientStore := clients.NewClientStore()
	timeStore := timekeeping.NewTimeStore()

	// Build learning engine.
	learningEngine := learning.Default

	// Build the hybrid RAG retriever (section chunking + dense/question/BM25 +
	// HyDE + RRF) over an in-process chunk store, and index every ingested
	// document into it. doc2query/HyDE use the light local tier.
	ragModel := routing.SelectModel(cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
	var ragGen rag.Generator
	if prov, perr := provReg.Get(ragModel); perr == nil {
		bare := routing.ResolveModelID(ragModel)
		temp := cfg.LLMTemperature
		ragGen = rag.GeneratorFunc(func(system, user string, maxTokens int) (string, error) {
			resp, err := prov.Chat(providers.ChatParams{
				Model:       bare,
				MaxTokens:   maxTokens,
				System:      system,
				Messages:    []providers.Message{{Role: "user", Content: user}},
				Temperature: temp,
			})
			if err != nil {
				return "", err
			}
			for _, b := range resp.Content {
				if b.Type == providers.BlockText {
					return b.Text, nil
				}
			}
			return "", nil
		})
	}
	ragSvc := rag.New(rag.NewMemStore(), embedC, ragGen)
	// Ingest synchronously: chunking + dense embeds + BM25 are fast and finish
	// within an upload's timeout, so retrieval is ready the moment a doc lands
	// (pre-indexed). IngestDoc spawns the slow doc2query enrichment in the
	// background itself, so it still doesn't block the upload.
	knowledgeStore.SetOnIngest(ragSvc.IngestDoc)

	// Build tool registry. Every built-in DocRepository backend also
	// implements ReviewRepository, so the same handle persists tabular-review
	// matrices.
	reviewRepo, _ := docRepo.(store.ReviewRepository)
	toolReg := tools.NewRegistry(cfg, provReg, costStore, ragSvc, reviewRepo)

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

	// Client-voice store (Remy / CNTXT advocacy briefs + matter notifications).
	clientVoiceStore := clientvoice.New(cfg.Persistence.ClientVoiceFile)
	if err := clientVoiceStore.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "client voice init: %v\n", err)
	}
	orch.SetClientVoiceStore(clientVoiceStore)

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
			data := newLPMDataProvider(orch, timeStore, clientStore)
			channelPoster := newMatterChannelPoster(cfg)
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

				// Phase 4: historical backfill grinds older mail on cheap compute.
				if cfg.LPM.BackfillEnabled {
					backfill := lpm.NewBackfill(lpm.BackfillConfig{
						WindowDays: cfg.LPM.BackfillWindowDays,
						StepDays:   cfg.LPM.BackfillStepDays,
						MaxPerStep: cfg.LPM.BackfillMaxPerStep,
						PauseMs:    cfg.LPM.BackfillPauseMs,
						CursorFile: cfg.LPM.BackfillCursorFile,
					}, nil, router, routed, data.MatterOptions)
					lpmSvc.WithBackfill(backfill)
				}
			}

			// Outbound drafting (email-write-mode), guard-enforced. Default "off".
			transport := lpm.NewTransport(
				cfg.Email.Graph.Enabled, cfg.Email.Gmail.Enabled,
				cfg.Email.Graph.UserEmail, cfg.Email.Gmail.UserEmail,
			)
			lpmSvc.WithDrafting(cfg.LPM.EmailWriteMode, cfg.LPM.AllowedDomains, transport, channelPoster)

			// send_gate pending-drafts store (queryable + approvable by ID).
			pending := lpm.NewPendingStore(cfg.LPM.PendingFile)
			if err := pending.Init(); err != nil {
				fmt.Fprintf(os.Stderr, "lpm pending store init: %v\n", err)
			}
			lpmSvc.WithPendingDrafts(pending)

			// Phase 3: 0600 portfolio briefing.
			lpmSvc.WithPortfolio(lpm.NewPortfolioBriefer(prov, model))

			lpmSvc.Start()
			defer lpmSvc.Stop()
		}
	}

	// Firm-wide background monitors (budget alerts, dockets, regulatory pulse).
	monitors := startMonitors(cfg, orch, timeStore, clientStore, knowledgeStore, provReg)
	defer monitors.Stop()

	// makeAPI builds the REST server and attaches optional LPM + docket routes.
	makeAPI := func() *api.Server {
		srv := api.New(cfg, orch, provReg, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore, reviewRepo)
		srv.AttachLPM(lpmSvc)
		srv.AttachDockets(monitors.Dockets)
		srv.AttachRegulatory(monitors.Regulatory)
		return srv
	}

	mode := os.Getenv("BIG_MICHAEL_MODE")
	if mode == "" {
		mode = "auto"
	}

	// ctx is cancelled on Ctrl+C / SIGTERM. The API server shuts down
	// gracefully via api.Server.Serve; wg tracks it so main can wait for
	// in-flight requests before the deferred cleanups (monitors.Stop,
	// costStore.Close) run. The MCP stdio server is deliberately NOT in
	// wg: it blocks reading stdin and cannot be interrupted — it ends when
	// the parent process closes the pipe or this process exits.
	var wg sync.WaitGroup
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveAPI := func() {
		addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)
		apiSrv := makeAPI()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := apiSrv.Serve(ctx, addr); err != nil {
				fmt.Fprintf(os.Stderr, "api serve: %v\n", err)
			}
		}()
	}
	serveMCP := func() {
		mcpSrv := mcp.New(orch, knowledgeStore, agentReg, pluginReg, timeStore)
		go func() {
			if err := mcpSrv.Serve(); err != nil {
				fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
			}
		}()
	}

	switch mode {
	case "mcp":
		// Pure MCP mode — serve stdio only, in the foreground.
		mcpSrv := mcp.New(orch, knowledgeStore, agentReg, pluginReg, timeStore)
		if err := mcpSrv.Serve(); err != nil {
			fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
			os.Exit(1)
		}

	case "backend":
		// REST API only.
		serveAPI()
		<-ctx.Done()

	case "standalone":
		// REST API + MCP stdio.
		serveAPI()
		serveMCP()
		<-ctx.Done()

	default: // "auto"
		// Default: run REST API (ARM devices are always "backend").
		serveAPI()
		// If stdin is not a TTY, also serve MCP on stdio.
		fi, _ := os.Stdin.Stat()
		if fi.Mode()&os.ModeCharDevice == 0 {
			serveMCP()
		}
		<-ctx.Done()
	}

	wg.Wait()
	fmt.Println("biglaw: shutdown complete")
}

func startLocalPprof(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("PPROF must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("PPROF may only bind a loopback address")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		mux.Handle("/debug/pprof/"+name, pprof.Handler(name))
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("pprof server stopped", "error", err)
		}
	}()
	return nil
}
