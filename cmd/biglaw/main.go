// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Command biglaw is the BigLaw 2 server: `serve [--allow-no-auth]` and `version [--config]` (see
// usage); later PRs add mcp, token and import-v1 (README § What is on disk).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

// version is overridden at release time with -ldflags "-X main.version=…".
var version = "2.0.0-dev"

const usage = `usage: biglaw <command>

  serve [--allow-no-auth]  run the server on BIGLAW_ADDR (migrates the database
                           first). There is no authentication yet, so serve
                           refuses a non-loopback BIGLAW_ADDR unless
                           --allow-no-auth says something else (such as a
                           loopback-only port publish) limits who can reach it.
  version [--config]       print the version; --config also prints the
                           configuration (secrets hidden) and checks it
`

func main() {
	docs.MaybeRunChild() // the PDF reader child (never returns when this process is one)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, environ []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch cmd, flags := args[0], args[1:]; cmd {
	case "version":
		if len(flags) > 1 || len(flags) == 1 && flags[0] != "--config" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		fmt.Fprintf(stdout, "biglaw %s (%s)\n", version, runtime.Version())
		if len(flags) == 1 {
			fmt.Fprint(stdout, Describe(getenv, environ))
			if _, err := Load(getenv); err != nil {
				fmt.Fprintf(stderr, "biglaw: configuration is not valid:\n%v\n", err)
				return 2
			}
		}
		return 0
	case "serve":
		allowNoAuth := len(flags) == 1 && flags[0] == "--allow-no-auth"
		if len(flags) > 1 || len(flags) == 1 && !allowNoAuth {
			fmt.Fprint(stderr, usage)
			return 2
		}
		log := slog.New(slog.NewTextHandler(stderr, nil))
		cfg, err := Load(getenv)
		if err != nil {
			fmt.Fprintf(stderr, "biglaw: refusing to start:\n%v\n", err)
			return 2
		}
		if !isLoopback(cfg.Addr) && !allowNoAuth {
			fmt.Fprintf(stderr, "biglaw: refusing to start: BIGLAW_ADDR %s is not a loopback address and this build has no authentication "+
				"(it lands in PR 8). Listen on 127.0.0.1, or pass --allow-no-auth if something else limits who can reach the port "+
				"(docker-compose.yml publishes it on the host's loopback only). See README § Exposure.\n", shown("BIGLAW_ADDR", cfg.Addr))
			return 2
		}
		if !isLoopback(cfg.Addr) {
			log.Warn("serving WITHOUT authentication on a non-loopback address (--allow-no-auth)", "addr", shown("BIGLAW_ADDR", cfg.Addr))
		}
		if names := UnknownVars(environ); len(names) > 0 {
			log.Warn("ignoring unknown BIGLAW_ variables (typo?)", "names", strings.Join(names, ","))
		}
		if names := NotYetRead(getenv); len(names) > 0 {
			log.Warn("these variables are set but not read by this build yet", "names", strings.Join(names, ","))
		}
		if cfg.ModelKey != "" && strings.HasPrefix(strings.ToLower(cfg.ModelURL), "http:") {
			log.Warn("BIGLAW_MODEL_KEY will be sent over plain http; use https unless the endpoint is on this machine", "endpoint", shown("BIGLAW_MODEL_URL", cfg.ModelURL))
		}
		for _, role := range roleNames {
			if id, err := cfg.Roles.Model(role); err == nil { // a role implies BIGLAW_MODEL_URL (Load)
				price := "unpriced: cost unknown, the USD budget cannot count it (set BIGLAW_PRICES)"
				if r, _, _ := cfg.Prices.Lookup(id); r != nil {
					price = fmt.Sprintf("$%g in / $%g out per 1M tokens", r.In, r.Out)
				}
				log.Info("model pricing", "role", role, "model", id, "price", price)
			}
		}
		if cfg.AllowAnthropic || cfg.AllowAWS {
			log.Warn("vendor breaker disarmed", "ALLOW_ANTHROPIC", cfg.AllowAnthropic, "ALLOW_AWS", cfg.AllowAWS)
		}
		st, err := store.Open(ctx, cfg.DatabaseURL, log)
		if err != nil {
			if ctx.Err() != nil {
				err = fmt.Errorf("stopped by a signal before startup finished: %w", err)
			}
			fmt.Fprintf(stderr, "biglaw: %v\n", err)
			return 1
		}
		defer st.Close()
		// Listen only once the database is ready, so a probe that can connect
		// can also be answered.
		ln, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			fmt.Fprintf(stderr, "biglaw: %v\n", err)
			return 1
		}
		d := docs.New(st)
		if cfg.Roles.Embed != "" || cfg.Roles.Vision != "" {
			client, err := model.New(model.Options{URL: cfg.ModelURL, Key: cfg.ModelKey, Roles: cfg.Roles, Prices: cfg.Prices, LocalWatts: cfg.LocalWatts, Log: log})
			if err != nil {
				fmt.Fprintf(stderr, "biglaw: %v\n", err)
				return 1
			}
			if cfg.Roles.Embed != "" {
				d.UseEmbedder(client, cfg.Roles.Embed)
			}
			if cfg.Roles.Vision != "" { // images go to this role only, never to a text model
				d.UseVision(client, cfg.Roles.Vision, cfg.ModelKey, log)
			}
		}
		if err := serve(ctx, st, d, ln, log); err != nil {
			fmt.Fprintf(stderr, "biglaw: %v\n", err)
			return 1
		}
		log.Info("biglaw stopped")
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "biglaw: unknown command %q\n%s", args[0], usage)
	return 2
}

type pinger interface{ Ping(context.Context) error }

// shutdownGrace is how long a stop waits for requests in flight before cancelling them (an ingest
// then removes its file and answers 503), and again for them to end before closing connections.
var shutdownGrace, jobEvery = 10 * time.Second, 15 * time.Minute

// serve serves HTTP on ln, and runs the embedding backfill, until ctx ends; then it shuts down gracefully.
func serve(ctx context.Context, st *store.Store, d *docs.Service, ln net.Listener, log *slog.Logger) error {
	if n, err := d.SweepOrphanFiles(ctx); err != nil || n > 0 {
		log.Warn("orphan files (uploads interrupted by a crash) swept", "files", n, "err", err)
	}
	backfill, stopBackfill := context.WithCancel(ctx)
	backfilled := make(chan struct{})
	defer func() { stopBackfill(); <-backfilled }()
	go func() {
		defer close(backfilled)
		if n, failed, err := d.Backfill(backfill); err != nil || failed > 0 { // model errors are fixed text
			log.Warn("embedding backfill: some documents stay lexical only (EmbedNote)", "embedded", n, "failed", failed, "err", err)
		} else if n > 0 {
			log.Info("embedding backfill", "embedded", n)
		}
		if n, err := d.LexicalOnly(backfill); n > 0 || err != nil {
			log.Warn("documents searched lexically only until a backfill embeds them (README § Documents)", "documents", n, "err", err)
		}
		for { // transcription jobs, at startup and every 15 minutes (a job whose lease ran out is taken again)
			if n, failed, err := d.TranscribePending(backfill); n+failed > 0 || err != nil {
				log.Info("transcription jobs", "transcribed", n, "failed", failed, "err", err)
			}
			select {
			case <-backfill.Done():
				return
			case <-time.After(jobEvery):
			}
		}
	}()
	base, stopRequests := context.WithCancelCause(context.Background())
	defer stopRequests(nil)
	srv := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return base },
		Handler:           routes(st, d, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("biglaw listening", "addr", ln.Addr().String(), "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil { // r2 N6: cancel what still runs, wait again, then close
		stopRequests(docs.ErrStopping)
		log.Warn("stopping: requests still running after the grace period were cancelled", "grace", shutdownGrace)
		again, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if srv.Shutdown(again) != nil {
			srv.Close()
		}
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// routes is GET /health and, with d, the document file routes (docs.Routes). Everything else is
// 404/405 until the REST API lands (PR 4b).
func routes(db pinger, d *docs.Service, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	if d != nil {
		d.Routes(mux, log)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		status, code, search := "ok", http.StatusOK, "lexical" // lexical only: no embed role (README § Documents)
		if d != nil && d.Hybrid() {
			search = "hybrid"
		}
		if err := db.Ping(ctx); err != nil {
			// Logged for the operator, not echoed: /health is unauthenticated.
			log.Warn("health check: database unavailable", "err", err)
			status, code = "database unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status, "version": version, "search": search})
	})
	return mux
}
