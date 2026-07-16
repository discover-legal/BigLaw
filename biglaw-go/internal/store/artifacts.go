// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"time"
)

// ReviewRepository stores completed tabular-review matrices as opaque JSON.
type ReviewRepository interface {
	PutReview(ctx context.Context, id, ownerID, matterNumber string, createdAt time.Time, payload []byte) error
	GetReview(ctx context.Context, id string) ([]byte, bool, error)
}

// DocumentVersion is one owner-scoped node in a negotiated-document lineage.
type DocumentVersion struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"ownerId,omitempty"`
	MatterNumber string    `json:"matterNumber,omitempty"`
	LineageID    string    `json:"lineageId"`
	ParentID     string    `json:"parentId,omitempty"`
	Round        int       `json:"round"`
	Source       string    `json:"source"`
	Author       string    `json:"author,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	Path         string    `json:"path,omitempty"`
	ContentHash  string    `json:"contentHash"`
	Text         string    `json:"text,omitempty"`
	Decisions    []byte    `json:"decisions,omitempty"`
}

// VersionRepository stores and resolves negotiated-document lineages.
type VersionRepository interface {
	PutVersion(ctx context.Context, v DocumentVersion) error
	GetVersion(ctx context.Context, id string) (*DocumentVersion, bool, error)
	ListLineage(ctx context.Context, lineageID string) ([]DocumentVersion, error)
	FindVersionByHash(ctx context.Context, contentHash string) (*DocumentVersion, bool, error)
	FindVersionByPath(ctx context.Context, path string) (*DocumentVersion, bool, error)
}
