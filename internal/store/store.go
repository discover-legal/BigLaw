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
	"strings"
	"time"

	"github.com/lib/pq"
)

// ConnectTimeout bounds the first connection in Open. Waiting for another
// process's migration lock is not bounded by it (see MigrateFS).
const ConnectTimeout = 30 * time.Second

// Store wraps the connection pool.
type Store struct {
	db *sql.DB
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
	db.SetMaxOpenConns(20)
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
	if err := Migrate(ctx, db, log); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

var errReserved = errors.New("is not a valid URL: percent-encode reserved characters in the user name or password " +
	"(@ → %40, / → %2F, ? → %3F, # → %23, % → %25, space → %20); the value is not shown because it may contain a password")

// CheckDSN reports whether lib/pq will read dsn the way it looks, without
// connecting. Its errors never contain the password. It refuses:
//   - a URL scheme that is not exactly "postgres://" or "postgresql://"
//     (lib/pq matches the prefix case-sensitively and would read
//     "POSTGRES://…" as a key=value string and dial a different server);
//   - an "@" after the host part, the sign of an unescaped "/", "?" or "#"
//     in the password (Go's URL parser would take part of the password for
//     the host and resolve it in DNS);
//   - anything lib/pq itself cannot parse.
func CheckDSN(dsn string) error {
	if _, err := pq.NewConfig(""); err != nil {
		return fmt.Errorf("cannot be used: the libpq environment is invalid: %w", err) // PG* variables only, no DSN involved
	}
	lower := strings.ToLower(dsn)
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		if strings.Contains(dsn, "://") {
			return errors.New("must be a postgres:// URL or a key=value string (value not shown: it may contain a password)")
		}
		if _, err := pq.NewConfig(dsn); err != nil {
			return errors.New("is not a valid key=value connection string (details not shown: they may quote the password)")
		}
		return nil
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return errors.New("must start with lowercase postgres:// or postgresql:// (lib/pq reads any other spelling as a key=value string and would connect somewhere else)")
	}
	rest := dsn[strings.Index(dsn, "://")+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 && strings.Contains(rest[i:], "@") {
		return errReserved
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return errReserved
	}
	// With the secrets replaced, lib/pq's own message is safe to show.
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "x")
	}
	q := u.Query()
	for k := range q {
		if strings.Contains(strings.ToLower(k), "password") {
			q.Set(k, "x")
		}
	}
	u.RawQuery = q.Encode()
	if _, err := pq.NewConfig(u.String()); err != nil {
		return fmt.Errorf("is not valid: %w", err)
	}
	return nil
}

// hideDSN replaces an error that echoes the DSN (lib/pq quotes the whole URL
// in parse errors, password included) with one that does not.
func hideDSN(err error, dsn string) error {
	var ue *url.Error
	if errors.As(err, &ue) || (dsn != "" && strings.Contains(err.Error(), dsn)) || strings.Contains(err.Error(), "connection info string") {
		return errors.New("the database URL could not be parsed (not shown: it may contain a password)")
	}
	return err
}

// Close releases the pool.
func (s *Store) Close() error { return s.db.Close() }

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
