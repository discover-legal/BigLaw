// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// biglaw — Go port of the Big Michael multi-agent legal AI platform.
// Targets Raspberry Pi / ARM64 SBCs (4 GB RAM).
//
// Run modes (BIG_MICHAEL_MODE env var). Exactly one process may own a data
// dir; ownership is the single-writer lock <data dir>/biglaw.lock.
//   auto       — take the lock if free and own DB + REST (+ MCP on stdio when
//                stdin is not a TTY); if another process holds it, run as an
//                MCP thin client of BIG_MICHAEL_API instead
//   backend    — own DB + REST, never MCP
//   mcp        — MCP thin client only: no DB, no monitors, no vector store;
//                every tool is a REST call to BIG_MICHAEL_API
//   standalone — own DB + REST + MCP on stdio

package main

import (
	"context"
	"errors"
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
	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/flavour"
	"github.com/discover-legal/biglaw-go/internal/integrations"
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

	switch cfg.Run.Mode {
	case "auto", "backend", "mcp", "standalone":
	default:
		fmt.Fprintf(os.Stderr, "biglaw: unknown BIG_MICHAEL_MODE %q (want auto | backend | mcp | standalone)\n", cfg.Run.Mode)
		os.Exit(2)
	}

	// ctx is cancelled on Ctrl+C / SIGTERM. The API server shuts down
	// gracefully via api.Server.Serve and the MCP stdio loop returns.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// mcp mode is a pure thin client: it never touches the data dir.
	if cfg.Run.Mode == "mcp" {
		os.Exit(runThinClient(ctx, cfg.Run.APIURL, cfg.Run.APIToken, 0))
	}

	// Every other mode owns the data dir. Take the single-writer lock before
	// opening anything in it; in auto mode a held lock means "attach to the
	// owner it names".
	dataLock, err := acquireDataDir(cfg)
	if errors.Is(err, backend.ErrDataDirLocked) && cfg.Run.Mode == "auto" {
		if !stdinIsPipe() {
			fmt.Fprintf(os.Stderr, "biglaw: %v\nauto mode would attach as an MCP client, but stdin is not a pipe "+
				"from an MCP host (MCP speaks JSON-RPC on stdio). Launch it from an MCP host such as Claude Code, "+
				"or point this process at its own data dir with RUVECTOR_DATA_DIR.\n", err)
			os.Exit(1)
		}
		apiURL := ownerAPIURL(cfg)
		slog.Info("data dir is owned by another process; attaching as an MCP thin client",
			"lock", err, "api", apiURL)
		// The owner may still be booting; its REST API gets a grace period.
		os.Exit(runThinClient(ctx, apiURL, cfg.Run.APIToken, ownerBootGrace))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "biglaw: %v\nStop the other process, give this one its own RUVECTOR_DATA_DIR, "+
			"or run it with BIG_MICHAEL_MODE=mcp to attach to the owner.\n", err)
		os.Exit(1)
	}
	defer dataLock.Release()

	// One-time move of pre-data-dir state (./.tasks.json, ./data/* when the
	// data dir is elsewhere, …) into the data dir, then the guarantee the
	// lock relies on: no default state path outside it. Both before any
	// store opens a file.
	if _, err := config.MigrateLegacyStateFiles(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "biglaw: refusing to start — legacy state could not be migrated into %s:\n%v\n"+
			"Each path listed was left where it was (anything else migrated is logged above). Move it there by "+
			"hand, or pin its location with the named env var, then start again.\n", cfg.VectorDB.DataDir, err)
		os.Exit(1)
	}
	if err := cfg.CheckStateLayout(); err != nil {
		fmt.Fprintf(os.Stderr, "biglaw: %v\n", err)
		os.Exit(1)
	}
	if err := integrations.DefaultClioClient.UseTokensFile(cfg.Persistence.ClioTokensFile); err != nil {
		slog.Warn("clio token store unreadable; Clio stays disconnected", "path", cfg.Persistence.ClioTokensFile, "err", err)
	}

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

	// Build cost store. Per-model rate overrides (COST_MODEL_RATES, parsed by
	// the config layer) go in first so every recorded call prices against them.
	if len(cfg.Cost.ModelRates) > 0 {
		rates := make(map[string]cost.ModelRate, len(cfg.Cost.ModelRates))
		for model, r := range cfg.Cost.ModelRates {
			rates[model] = cost.ModelRate{In: r.In, Out: r.Out}
		}
		cost.SetModelRates(rates)
	}
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

	// Practice-area flavour: trim the bench and template list to the preset
	// (FLAVOUR env → flavours/<name>.json). Applied after every agent/template
	// source has loaded so plugin and Lavern additions are filtered too.
	if fl, err := flavour.Load(".", cfg.Flavour); err != nil {
		slog.Error("flavour load failed", "flavour", cfg.Flavour, "err", err)
		os.Exit(1)
	} else if fl != nil {
		before := len(allAgents)
		allAgents = fl.FilterAgents(allAgents)
		tmplBefore := len(templatesStore.List())
		templatesStore.Replace(fl.FilterTemplates(templatesStore.List()))
		slog.Info("flavour active", "flavour", fl.ID, "name", fl.Name,
			"agents", fmt.Sprintf("%d/%d", len(allAgents), before),
			"templates", fmt.Sprintf("%d/%d", len(templatesStore.List()), tmplBefore))
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

	// Lavern prompt-only orchestrator personas: overlaid onto T0 round-goal +
	// synthesis calls for their workflow types (they are not bench agents).
	if personas, err := adapters.LoadLavernOrchestratorPrompts("agents/lavern"); err == nil && len(personas) > 0 {
		orch.SetWorkflowPersonas(personas)
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

	// The REST server; its backend.Local is the one implementation of the
	// operations REST and MCP share.
	apiSrv := api.New(cfg, orch, provReg, profileStore, clientStore, timeStore, knowledgeStore, agentReg, costStore, reviewRepo)
	apiSrv.AttachPlugins(pluginReg)
	apiSrv.AttachLPM(lpmSvc)
	apiSrv.AttachDockets(monitors.Dockets)
	apiSrv.AttachRegulatory(monitors.Regulatory)

	mode := cfg.Run.Mode
	serveMCPToo := mode == "standalone" || (mode == "auto" && stdinIsPipe())

	// The in-process MCP server acts as the local partner, or — with
	// AUTH_ENABLED — as the API_PROFILE_ID profile, exactly like a bearer
	// client of the REST API.
	var mcpPrincipal types.SessionUser
	if serveMCPToo {
		p, err := ownerMCPPrincipal(cfg, profileStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "biglaw: %v\n", err)
			os.Exit(1)
		}
		mcpPrincipal = p
	}

	// wg tracks the API server so main can wait for in-flight requests
	// before the deferred cleanups (monitors.Stop, costStore.Close) run. The
	// MCP stdio loop is not in wg: it ends when stdin closes or ctx is done.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		addr := fmt.Sprintf("%s:%d", cfg.API.Host, cfg.API.Port)
		if err := apiSrv.Serve(ctx, addr); err != nil {
			fmt.Fprintf(os.Stderr, "api serve: %v\n", err)
		}
	}()
	if serveMCPToo {
		go func() {
			if err := mcp.New(apiSrv.Backend().As(mcpPrincipal)).Serve(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "mcp serve: %v\n", err)
			}
		}()
	}
	<-ctx.Done()

	wg.Wait()
	fmt.Fprintln(os.Stderr, "biglaw: shutdown complete")
}

// acquireDataDir takes the data dir's single-writer lock, recording this
// process's PID and REST URL for whoever finds it held. BIGLAW_LOCK=off
// skips it (filesystems without advisory locks), loudly.
func acquireDataDir(cfg *config.Config) (*backend.DataLock, error) {
	if cfg.Run.LockDisabled {
		slog.Warn("BIGLAW_LOCK=off: data-dir single-writer lock DISABLED — run at most one owner per data dir",
			"dataDir", cfg.VectorDB.DataDir)
		return nil, nil
	}
	return backend.AcquireDataLock(cfg.VectorDB.DataDir, backend.LockInfo{
		PID: os.Getpid(),
		API: config.LocalAPIURL(cfg.API.Host, cfg.API.Port),
	})
}

// ownerAPIURL is where an auto-mode client finds the data-dir owner: the URL
// the owner advertised in its lock, falling back to BIG_MICHAEL_API (or its
// default) when the owner recorded none.
func ownerAPIURL(cfg *config.Config) string {
	holder := backend.ReadLockHolder(cfg.VectorDB.DataDir, 2*time.Second)
	switch {
	case holder.API == "":
		return cfg.Run.APIURL
	case cfg.Run.APIURLExplicit && holder.API != cfg.Run.APIURL:
		slog.Warn("BIG_MICHAEL_API differs from the URL the data-dir owner advertises; attaching to the owner",
			"BIG_MICHAEL_API", cfg.Run.APIURL, "owner", holder.API, "ownerPid", holder.PID)
	}
	return holder.API
}

// ownerMCPPrincipal is who an owner's in-process MCP server acts as.
func ownerMCPPrincipal(cfg *config.Config, profiles *auth.ProfileStore) (types.SessionUser, error) {
	if !cfg.Auth.Enabled {
		return auth.LocalPartner, nil
	}
	if cfg.API.ProfileID == "" {
		return types.SessionUser{}, fmt.Errorf("AUTH_ENABLED=true: the in-process MCP server acts as API_PROFILE_ID, which is unset")
	}
	p := profiles.Get(cfg.API.ProfileID)
	if p == nil {
		return types.SessionUser{}, fmt.Errorf("AUTH_ENABLED=true: API_PROFILE_ID %q (the in-process MCP principal) is not a known profile", cfg.API.ProfileID)
	}
	return backend.PrincipalFor(p), nil
}

// ownerBootGrace is how long an auto-mode thin client keeps retrying the lock
// holder's REST API (the owner may have just started).
const ownerBootGrace = 20 * time.Second

// runThinClient serves MCP on stdio over a backend.Remote — no database, no
// monitors, no vector store. The MCP server answers initialize at once; the
// backend is probed in the background (and again by the first tool call, if
// needed), so an unreachable or misconfigured backend surfaces as an
// actionable error both on stderr and in every tool result until it answers.
// Returns the process exit code.
func runThinClient(ctx context.Context, apiURL, token string, grace time.Duration) int {
	remote, err := backend.NewRemote(apiURL, token, grace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "biglaw mcp: BIG_MICHAEL_API: %v\n", err)
		return 1
	}
	go func() {
		if _, err := remote.Attach(ctx); err != nil && ctx.Err() == nil {
			slog.Error("biglaw mcp: backend not attached; tools report this until it answers", "error", err)
		}
	}()
	if err := mcp.New(remote).Serve(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "biglaw mcp: %v\n", err)
		return 1
	}
	return 0
}

// stdinIsPipe reports whether stdin can carry MCP traffic: anything but a
// character device (a TTY, or /dev/null under a service manager).
func stdinIsPipe() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice == 0
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
