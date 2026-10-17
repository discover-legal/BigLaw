// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Command biglaw is the BigLaw 2 server.
//
//	biglaw serve              run the HTTP server (migrates the database first)
//	biglaw version [--config] print the version (and the resolved configuration)
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

  serve              run the server on BIGLAW_ADDR (migrates the database first)
  version [--config] print the version; --config also prints the resolved
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
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "biglaw %s (%s)\n", version, runtime.Version())
		if len(args) > 1 && args[1] == "--config" {
			fmt.Fprint(stdout, Describe(getenv))
			if _, err := Load(getenv); err != nil {
				fmt.Fprintf(stderr, "biglaw: configuration is not valid:\n%v\n", err)
				return 2
			}
		} else if len(args) > 1 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return 0
	case "serve":
		if len(args) > 1 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		log := slog.New(slog.NewTextHandler(stderr, nil))
		cfg, err := Load(getenv)
		if err != nil {
			fmt.Fprintf(stderr, "biglaw: refusing to start:\n%v\n", err)
			return 2
		}
		if unknown := UnknownVars(environ); len(unknown) > 0 {
			log.Warn("ignoring unknown BIGLAW_ variables (typo?)", "names", strings.Join(unknown, ","))
		}
		if cfg.AllowAnthropic {
			log.Warn("vendor breaker disarmed by ALLOW_ANTHROPIC")
		}
		ln, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			fmt.Fprintf(stderr, "biglaw: %v\n", err)
			return 1
		}
		if err := serve(ctx, cfg, ln, log); err != nil {
			fmt.Fprintf(stderr, "biglaw: %v\n", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "biglaw: unknown command %q\n%s", args[0], usage)
	return 2
}

// serve opens (and migrates) the store, then serves HTTP on ln until ctx ends.
func serve(ctx context.Context, cfg Config, ln net.Listener, log *slog.Logger) error {
	defer ln.Close()
	openCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	st, err := store.Open(openCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{
		Handler:           routes(st),
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

type pinger interface{ Ping(context.Context) error }

// routes is the PR 1 surface: only GET /health. Everything else is 404.
func routes(db pinger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		status, code := "ok", http.StatusOK
		if err := db.Ping(ctx); err != nil {
			// The cause is not echoed: /health is unauthenticated.
			status, code = "database unavailable", http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"status": status, "version": version})
	})
	return mux
}
