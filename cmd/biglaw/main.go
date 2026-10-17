// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Command biglaw is the BigLaw 2 server.
//
//	biglaw serve [--allow-no-auth]  run the HTTP server (migrates the database first)
//	biglaw version [--config]       print the version (and the configuration)
//
// Later PRs add mcp, token and import-v1 (README § What is on disk).
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
		if err := serve(ctx, st, ln, log); err != nil {
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

// serve serves HTTP on ln until ctx ends, then shuts down gracefully.
func serve(ctx context.Context, db pinger, ln net.Listener, log *slog.Logger) error {
	srv := &http.Server{
		Handler:           routes(db, log),
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
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// routes is the PR 1 surface: only GET /health. Everything else is 404/405.
func routes(db pinger, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		status, code := "ok", http.StatusOK
		if err := db.Ping(ctx); err != nil {
			// Logged for the operator, not echoed: /health is unauthenticated.
			log.Warn("health check: database unavailable", "err", err)
			status, code = "database unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status, "version": version})
	})
	return mux
}
