// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import "context"

// Identity is the security principal a repository call runs as. Postgres
// projects it into transaction-local GUCs; local stores enforce it directly.
type Identity struct {
	ProfileID string
	IsPartner bool
	System    bool
}

type identityKey struct{}

// WithIdentity tags ctx as a specific lawyer. Use in user-facing request paths.
func WithIdentity(ctx context.Context, profileID string, isPartner bool) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{ProfileID: profileID, IsPartner: isPartner})
}

// WithSystem tags ctx as the trusted internal principal.
func WithSystem(ctx context.Context) context.Context {
	return context.WithValue(ctx, identityKey{}, Identity{System: true})
}

// IdentityFrom extracts the principal; ok is false when none was set.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// CanAccessOwner applies artifact ownership consistently in local backends and
// caches. Ownerless legacy rows remain visible only to privileged callers.
func CanAccessOwner(ctx context.Context, ownerID string) bool {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return ownerID == ""
	}
	return id.System || id.IsPartner || (ownerID != "" && ownerID == id.ProfileID)
}
