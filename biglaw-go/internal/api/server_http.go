// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxJSONBody      = 16 << 20
	maxMultipartBody = 32 << 20
)

func requestBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := int64(maxJSONBody)
		if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
			limit = maxMultipartBody
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// Run starts the HTTP server on addr (e.g. ":3101").
func (s *Server) Run(addr string) error {
	return s.httpServer(addr).ListenAndServe()
}

func (s *Server) httpServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      0, // SSE streams are intentionally long-lived.
		IdleTimeout:       60 * time.Second,
	}
}

// Serve runs the API on addr until ctx is cancelled, then shuts down
// gracefully. In-flight requests get a grace period; long-lived SSE streams
// (/tasks/:id/stream, /audit/stream) never end on their own, so when the
// grace period expires the remaining connections are force-closed — without
// that, shutdown would hang for as long as a browser tab stays open.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := s.httpServer(addr)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(grace); err != nil {
			return srv.Close()
		}
		return nil
	}
}
