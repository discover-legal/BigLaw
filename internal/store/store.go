// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package store is BigLaw's only persistence layer: PostgreSQL through
// database/sql and github.com/lib/pq. All state, including uploaded and
// generated files, lives here. There is no pluggable backend.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	// ConnectTimeout bounds the first connection in Open. Waiting for another
	// process's migration lock is not bounded by it (see MigrateFS).
	ConnectTimeout = 30 * time.Second
	// PutFile holds a connection for as long as its reader runs, so at most
	// MaxConcurrentUploads of the MaxOpenConns run at once and the rest stay
	// for /health and reads. Only the caller can end a stalled reader (the
	// upload route's read deadline, internal/docs/http.go); until it returns,
	// its slot stays taken.
	MaxOpenConns         = 20
	MaxConcurrentUploads = MaxOpenConns - 4
	// MaxConcurrentReads bounds GetFile (each holds a whole file, ≤ MaxFileSize, about twice that
	// at peak with the default GOGC, so 4 stay near 512 MiB, inside a 4 GB Pi) and ReadFile
	// (one chunk each).
	MaxConcurrentReads = 4
)

// Store wraps the connection pool.
type Store struct {
	db      *sql.DB
	uploads chan struct{} // PutFile slots (MaxConcurrentUploads)
	reads   chan struct{} // GetFile slots (MaxConcurrentReads)
}

// Open checks dsn (CheckDSN), connects, and applies every pending embedded
// migration under an advisory lock (MigrateFS). The returned error never
// contains the DSN's password, so it is safe to log. log may be nil.
func Open(ctx context.Context, dsn string, log *slog.Logger) (*Store, error) {
	if err := CheckDSN(dsn); err != nil {
		return nil, fmt.Errorf("store: database URL %w", err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("store: invalid database URL")
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	err = db.PingContext(pctx)
	cancel()
	if err != nil {
		db.Close()
		if ctx.Err() == nil && errors.Is(pctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("store: could not connect within %s: %w", ConnectTimeout, hideDSN(err, dsn))
		}
		return nil, fmt.Errorf("store: connect: %w", hideDSN(err, dsn))
	}
	if _, err := db.ExecContext(ctx, createVector); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: the pgvector extension (vector) is missing and could not be created: use a Postgres with pgvector "+
			"(the pgvector/pgvector images) or have a superuser run CREATE EXTENSION vector in this database (README § Database): %w", hideDSN(err, dsn))
	}
	if err := Migrate(ctx, db, log); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, uploads: make(chan struct{}, MaxConcurrentUploads), reads: make(chan struct{}, MaxConcurrentReads)}, nil
}

// CheckDSN reports whether lib/pq will read dsn the way it looks, without
// connecting. Its errors never contain the password. It refuses:
//   - a URL scheme that is not exactly "postgres://" or "postgresql://"
//     (lib/pq matches the prefix case-sensitively and would read
//     "POSTGRES://…" as a key=value string and dial a different server);
//   - an "@" after the host part, the sign of an unescaped "/", "?" or "#"
//     in the password (Go's URL parser would take part of the password for
//     the host and resolve it in DNS). An "@" in a database name or query
//     value is refused too, because it cannot be told apart; %40 works;
//   - anything lib/pq itself cannot parse, from the same single parse it
//     connects with (so a DSN that names its user needs no passwd entry for
//     the process's uid, as under docker run --user or Kubernetes runAsUser).
func CheckDSN(dsn string) error {
	if scheme, rest, ok := strings.Cut(dsn, "://"); ok {
		switch {
		case strings.EqualFold(scheme, "postgres") || strings.EqualFold(scheme, "postgresql"):
			if scheme != strings.ToLower(scheme) {
				return errors.New("must start with lowercase postgres:// or postgresql:// (lib/pq reads any other spelling as a key=value string and would connect somewhere else)")
			}
		default:
			return errors.New("must be a postgres:// URL or a key=value string (value not shown: it may contain a password)")
		}
		if i := strings.IndexAny(rest, "/?#"); i >= 0 && strings.Contains(rest[i:], "@") {
			return errors.New(`is not accepted: it has an "@" after the host part (in the password, the database name or a query value); ` +
				"percent-encode that @ as %40, and / ? # % in a password as %2F %3F %23 %25. The value is not shown because it may contain a password")
		}
	}
	if _, err := pq.NewConfig(dsn); err != nil {
		if noDefaultUser(err) {
			return fmt.Errorf("names no user, and this process has no user name to default to (%w); "+
				"name the user in the database URL (postgres://USER@host/db) or set PGUSER", hideDSN(err, dsn))
		}
		return fmt.Errorf("is not valid: %w", hideDSN(err, dsn))
	}
	return nil
}

// noDefaultUser reports whether err is lib/pq failing to find a default user
// name: the DSN names none and os/user cannot resolve the uid (no passwd entry
// and no $USER, as under docker run --user or Kubernetes runAsUser). Every
// os/user error starts with "user: " ("user: Current requires cgo or $USER
// set in environment" without cgo, "user: unknown userid N" with it).
func noDefaultUser(err error) bool {
	var unknown user.UnknownUserIdError
	return errors.As(err, &unknown) || strings.HasPrefix(err.Error(), "user: ")
}

// hideDSN replaces an error that echoes the DSN (lib/pq quotes the whole URL
// in parse errors, password included, and a key=value fragment in others)
// with one that does not.
func hideDSN(err error, dsn string) error {
	var ue *url.Error
	if errors.As(err, &ue) || (dsn != "" && strings.Contains(err.Error(), dsn)) || strings.Contains(err.Error(), "connection info string") {
		return errors.New("the database URL could not be parsed (not shown: it may contain a password). In a URL, percent-encode reserved " +
			"characters in the user name or password: @ → %40, / → %2F, ? → %3F, # → %23, % → %25, space → %20; in key=value form, quote a value with spaces in single quotes")
	}
	return err
}

// DescribeDSN renders what lib/pq will use for dsn without any secret: only
// host, port, user, dbname and sslmode from lib/pq's own parse (PG* defaults
// included), whether a password is set, and counts. Nothing is copied from
// the DSN's text, so no quoting in it can bring a password fragment out. A
// DSN that fails CheckDSN is not described.
func DescribeDSN(dsn string) string {
	if CheckDSN(dsn) != nil {
		return "(set, not shown: invalid, see below)"
	}
	c, err := pq.NewConfig(dsn)
	if err != nil {
		return "(set, not shown: invalid)"
	}
	pw, ssl := "unset", strconv.Quote(string(c.SSLMode))
	if c.Password != "" {
		pw = "set, hidden"
	}
	if c.SSLMode == "" {
		ssl = "(unset: lib/pq uses TLS, as require)"
	}
	out := fmt.Sprintf("host=%q port=%d user=%q dbname=%q sslmode=%s password=(%s)", c.Host, c.Port, c.User, c.Database, ssl, pw)
	if n := len(c.Multi); n > 0 {
		out += fmt.Sprintf(" (+%d more hosts)", n)
	}
	if n := len(c.Runtime); n > 0 {
		out += fmt.Sprintf(" (+%d other parameters, not shown)", n)
	}
	return out
}

// Close releases the pool.
func (s *Store) Close() error { return s.db.Close() }

// DB is the pool, for packages that own their tables (docs). Files go through PutFile/GetFile.
func (s *Store) DB() *sql.DB { return s.db }

// Uploads and Reads are how many PutFile and GetFile/ReadFile calls hold a slot now.
func (s *Store) Uploads() int { return len(s.uploads) }
func (s *Store) Reads() int   { return len(s.reads) }

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// InTx runs fn inside one transaction. It commits when fn returns nil and
// rolls back otherwise (including when fn panics, in which case the panic is
// re-raised after the rollback).
func (s *Store) InTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func orDiscard(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return log
}

// createVector creates the pgvector extension migration 0003 needs. Extensions are database-wide,
// so this runs outside the per-schema migrations, under a lock (two concurrent CREATE EXTENSION IF
// NOT EXISTS can collide); one query string is one transaction. An existing one needs no privilege.
const createVector = `SELECT pg_advisory_xact_lock(hashtext('biglaw.extension.vector')); CREATE EXTENSION IF NOT EXISTS vector`
