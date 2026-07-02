// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package store is the durable-persistence seam for BigLaw. The Go port shipped
// with an in-memory document store (lost on restart); this package gives it a
// real backend behind a single interface with two implementations:
//
//	sqlite   — pure-Go (modernc.org/sqlite, no cgo), single file, Pi-friendly,
//	           the default for local installs. Row security is enforced in the
//	           application layer (SQLite has no row-level security).
//	postgres — managed/cloud (Supabase, Neon, RDS …) with database-level RLS
//	           policies layered under the same app-layer checks (defense in
//	           depth). [implemented in Phase 1b]
//
// Semantic search keeps its in-memory vector index (rebuilt from the repository
// on boot); only the durable document records live in the repository.
package store

import (
	"context"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Request identity (drives database row-level security) ──────────────────────

// Identity is the security principal a repository call runs as. It is carried
// in context.Context and, on the Postgres backend, projected into per-transaction
// session GUCs that RLS policies read. The default-deny posture: a call with NO
// identity in its context sees and writes nothing — every legitimate caller must
// declare itself either a user (WithIdentity) or the trusted system (WithSystem).
type Identity struct {
	ProfileID string // the acting lawyer's profile ID (empty for System)
	IsPartner bool   // partners see/manage all rows
	System    bool   // internal/system caller — full access (monitors, boot load, MCP)
}

type identityKey struct{}

// WithIdentity tags ctx as a specific lawyer. Use in user-facing request paths.
func WithIdentity(ctx context.Context, profileID string, isPartner bool) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{ProfileID: profileID, IsPartner: isPartner})
}

// WithSystem tags ctx as the trusted system principal (full access). Use for
// boot-time loads, background monitors, DyTopo, and other non-user callers.
func WithSystem(ctx context.Context) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{System: true})
}

// IdentityFrom extracts the principal; ok is false when none was set (deny).
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// DocRepository is durable CRUD for documents. Implementations must be safe for
// concurrent use. Every method takes a context carrying the request Identity;
// the Postgres backend enforces row-level security from it (default-deny), while
// SQLite/memory (local single-tenant) rely on the application layer.
type DocRepository interface {
	// Upsert inserts or replaces a document by ID.
	Upsert(ctx context.Context, doc types.Document) error
	// GetByID returns the document and whether it was found (and visible).
	GetByID(ctx context.Context, id string) (*types.Document, bool, error)
	// List returns all visible documents (ordered by ingestion time ascending).
	List(ctx context.Context) ([]types.Document, error)
	// Delete removes a document by ID (no error if absent or not visible).
	Delete(ctx context.Context, id string) error

	// AddAttachment persists attachment metadata (bytes live in the blob store).
	AddAttachment(ctx context.Context, att types.Attachment) error
	// ListAttachments returns the visible attachments for a document.
	ListAttachments(ctx context.Context, docID string) ([]types.Attachment, error)
	// GetAttachment returns one attachment by ID, if visible.
	GetAttachment(ctx context.Context, id string) (*types.Attachment, bool, error)
	// DeleteAttachment removes attachment metadata by ID.
	DeleteAttachment(ctx context.Context, id string) error

	// Backend names the implementation ("sqlite", "postgres", "memory").
	Backend() string
	// Close releases the underlying handle.
	Close() error
}

// ReviewRepository is durable storage for completed tabular-review matrices
// (internal/tools tabular_review). The payload is the review's full JSON
// return object, stored opaquely — a matrix is only ever read back whole, so
// cells are not normalised into columns. Every backend returned by Open
// implements it alongside DocRepository.
type ReviewRepository interface {
	// PutReview inserts or replaces a review payload by ID.
	PutReview(ctx context.Context, id string, createdAt time.Time, payload []byte) error
	// GetReview returns the stored payload and whether it was found.
	GetReview(ctx context.Context, id string) ([]byte, bool, error)
}

// Open builds the repository selected by config. Resolution order:
//
//	DATABASE_URL set (postgres:// or postgresql://) → postgres   [Phase 1b]
//	DB_BACKEND=memory                                → in-memory (no persistence)
//	otherwise                                        → sqlite at cfg.Database.SQLitePath
//
// Open never returns a nil repository on success.
func Open(cfg *config.Config) (DocRepository, error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.Database.Backend))
	url := strings.TrimSpace(cfg.Database.URL)

	switch {
	case backend == "memory":
		return NewMemoryRepo(), nil
	case backend == "postgres" || isPostgresURL(url):
		return openPostgres(cfg)
	default:
		return openSQLite(cfg.Database.SQLitePath)
	}
}

func isPostgresURL(url string) bool {
	return strings.HasPrefix(url, "postgres://") || strings.HasPrefix(url, "postgresql://")
}
