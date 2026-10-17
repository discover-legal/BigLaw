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
	"net/url"
	"strings"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" driver
)

// Store wraps the connection pool.
type Store struct {
	db *sql.DB
}

// Open connects to dsn, checks the connection and applies every pending
// embedded migration (under an advisory lock, see Migrate). The returned
// error never contains the DSN, so it is safe to log.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("store: invalid database URL")
	}
	db.SetMaxOpenConns(20)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect: %w", hideDSN(err, dsn))
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// hideDSN replaces an error that echoes the DSN (lib/pq quotes the whole URL
// in parse errors, password included) with one that does not.
func hideDSN(err error, dsn string) error {
	var ue *url.Error
	if errors.As(err, &ue) || (dsn != "" && strings.Contains(err.Error(), dsn)) {
		return errors.New("the database URL could not be parsed (not shown: it may contain a password)")
	}
	return err
}

// DB exposes the pool to sibling packages that run their own queries.
func (s *Store) DB() *sql.DB { return s.db }

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
