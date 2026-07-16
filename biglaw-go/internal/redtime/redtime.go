// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package redtime is the redline timeline — the same contract tracked across
// negotiation rounds. Every version of a document (drafts we sent, marked-up
// drafts received, uploads) registers into a lineage; consecutive versions
// are compared; the result is a per-clause timeline answering how the
// language evolved, who moved when, which counters were accepted, rejected,
// or silently modified, and how far the current draft sits from the firm's
// playbook position (drift).
//
// This file is the registration half: RegisterVersion is idempotent by
// content hash, assigns round = parent.round + 1, and extracts plain text
// (insertions-accepted visible text for a .docx) so timelines and diffs never
// depend on the file still being on disk. timeline.go builds the timeline.

package redtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/store"
)

// Version sources — which side of the table a version came from.
const (
	SourceOurs   = "ours"
	SourceTheirs = "theirs"
	SourceUpload = "upload"
)

var (
	// ErrUnavailable means the configured durable store does not support
	// version history (a nil VersionRepository) — Redtime features degrade
	// gracefully behind it.
	ErrUnavailable = errors.New("version tracking unavailable: the configured document store does not support version history")
	// ErrNotFound means no lineage matched the requested identifier.
	ErrNotFound = errors.New("no document version lineage found")
)

// RegisterOpts parameterises one version registration.
type RegisterOpts struct {
	// OwnerID and MatterNumber scope the lineage to its legal owner/matter.
	OwnerID      string
	MatterNumber string
	// Path is the document on disk. Text is extracted from it when Text is
	// empty (.docx → insertions-accepted visible text; .txt/.md → contents).
	Path string
	// Text is the pre-extracted plain text; optional when Path is readable.
	Text string
	// ParentID names the explicit parent version (wins over LineageID).
	ParentID string
	// LineageID joins an existing lineage; the latest version becomes the
	// parent. Empty with no ParentID starts a new lineage.
	LineageID string
	// Source is "ours", "theirs", or "upload" (default).
	Source string
	// Author is the person or side that produced this version.
	Author string
	// Decisions optionally attaches the respond_to_redline decision summary
	// for the round that produced this version (marshalled to JSON).
	Decisions interface{}
	// CreatedAt defaults to now.
	CreatedAt time.Time
}

// RegisterVersion records one document version into its lineage, idempotently
// by content hash: re-registering identical content into the same lineage
// returns the existing version (attaching Decisions if it had none) instead
// of growing the lineage.
func RegisterVersion(ctx context.Context, repo store.VersionRepository, opts RegisterOpts) (*store.DocumentVersion, error) {
	if repo == nil {
		return nil, ErrUnavailable
	}

	text := opts.Text
	var fileBytes []byte
	if opts.Path != "" {
		if data, err := os.ReadFile(opts.Path); err == nil {
			fileBytes = data
			if text == "" {
				text = extractText(opts.Path, data)
			}
		}
	}
	hash := contentHash(fileBytes, text)
	if hash == "" {
		return nil, fmt.Errorf("redtime: nothing to register — no readable file at %q and no text supplied", opts.Path)
	}

	// Resolve the parent: explicit ParentID first, else the latest version of
	// the requested lineage.
	var parent *store.DocumentVersion
	switch {
	case opts.ParentID != "":
		p, found, err := repo.GetVersion(ctx, opts.ParentID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("redtime: parent version %q not found", opts.ParentID)
		}
		parent = p
	case opts.LineageID != "":
		vs, err := repo.ListLineage(ctx, opts.LineageID)
		if err != nil {
			return nil, err
		}
		if len(vs) > 0 {
			parent = &vs[len(vs)-1]
		}
	}

	lineageID := opts.LineageID
	ownerID := strings.TrimSpace(opts.OwnerID)
	matterNumber := strings.TrimSpace(opts.MatterNumber)
	if parent != nil {
		lineageID = parent.LineageID
		if ownerID != "" && parent.OwnerID != "" && ownerID != parent.OwnerID {
			return nil, fmt.Errorf("redtime: owner does not match parent lineage")
		}
		if matterNumber != "" && parent.MatterNumber != "" && matterNumber != parent.MatterNumber {
			return nil, fmt.Errorf("redtime: matter does not match parent lineage")
		}
		ownerID = parent.OwnerID
		matterNumber = parent.MatterNumber
	}

	// Idempotency: identical content already in the target lineage (or in any
	// lineage when none was requested) is the same version.
	if existing, found, err := repo.FindVersionByHash(ctx, hash); err != nil {
		return nil, err
	} else if found && existing.OwnerID == ownerID && existing.MatterNumber == matterNumber &&
		(lineageID == "" || existing.LineageID == lineageID) {
		if opts.Decisions != nil && len(existing.Decisions) == 0 {
			if b, merr := json.Marshal(opts.Decisions); merr == nil {
				existing.Decisions = b
				if perr := repo.PutVersion(ctx, *existing); perr != nil {
					return nil, perr
				}
			}
		}
		return existing, nil
	}

	if lineageID == "" {
		lineageID = uuid.NewString()
	}
	round, parentID := 1, ""
	if parent != nil {
		round, parentID = parent.Round+1, parent.ID
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	var decisions []byte
	if opts.Decisions != nil {
		decisions, _ = json.Marshal(opts.Decisions)
	}

	v := store.DocumentVersion{
		ID:           uuid.NewString(),
		OwnerID:      ownerID,
		MatterNumber: matterNumber,
		LineageID:    lineageID,
		ParentID:     parentID,
		Round:        round,
		Source:       normalizeSource(opts.Source),
		Author:       strings.TrimSpace(opts.Author),
		CreatedAt:    created,
		Path:         opts.Path,
		ContentHash:  hash,
		Text:         text,
		Decisions:    decisions,
	}
	if err := repo.PutVersion(ctx, v); err != nil {
		return nil, err
	}
	return &v, nil
}

// HashBytes returns the content hash Redtime uses for idempotent registration
// — exported so hook sites can look versions up by the hash of a file they
// already hold.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// contentHash prefers the raw file bytes (byte-exact identity for uploads and
// saved drafts) and falls back to the extracted text.
func contentHash(fileBytes []byte, text string) string {
	if len(fileBytes) > 0 {
		return HashBytes(fileBytes)
	}
	if text != "" {
		return HashBytes([]byte(text))
	}
	return ""
}

// extractText pulls the plain text out of a document's bytes: for a .docx the
// insertions-accepted visible text (what a reader sees with markup shown as
// final); for plain-text formats the contents; empty otherwise.
func extractText(path string, data []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx":
		doc, err := ooxml.Open(data)
		if err != nil {
			return ""
		}
		return doc.Text()
	case ".txt", ".md":
		return string(data)
	default:
		return ""
	}
}

func normalizeSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SourceOurs:
		return SourceOurs
	case SourceTheirs:
		return SourceTheirs
	default:
		return SourceUpload
	}
}
