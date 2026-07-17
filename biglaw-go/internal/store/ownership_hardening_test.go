// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestLocalRepositoriesEnforceOwnership(t *testing.T) {
	factories := map[string]func(*testing.T) DocRepository{
		"memory": func(t *testing.T) DocRepository { return NewMemoryRepo() },
		"sqlite": func(t *testing.T) DocRepository {
			r, err := openSQLite(filepath.Join(t.TempDir(), "ownership.db"))
			if err != nil {
				t.Fatal(err)
			}
			return r
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			r := factory(t)
			defer r.Close()
			system := WithSystem(context.Background())
			alice := WithIdentity(context.Background(), "alice", false)
			bob := WithIdentity(context.Background(), "bob", false)
			for _, doc := range []types.Document{{ID: "a", OwnerID: "alice"}, {ID: "b", OwnerID: "bob"}, {ID: "legacy"}} {
				if err := r.Upsert(system, doc); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := r.GetByID(alice, "b"); err != nil || ok {
				t.Fatalf("alice read bob document: ok=%v err=%v", ok, err)
			}
			if _, ok, err := r.GetByID(alice, "legacy"); err != nil || ok {
				t.Fatalf("alice read ownerless document: ok=%v err=%v", ok, err)
			}
			if docs, err := r.List(alice); err != nil || len(docs) != 1 || docs[0].ID != "a" {
				t.Fatalf("alice list = %#v, err=%v", docs, err)
			}
			if err := r.Upsert(alice, types.Document{ID: "b", OwnerID: "alice"}); err == nil {
				t.Fatal("alice overwrote bob document")
			}
			if err := r.Upsert(alice, types.Document{ID: "x", OwnerID: "bob"}); err == nil {
				t.Fatal("alice created bob-owned document")
			}
			if err := r.AddAttachment(alice, types.Attachment{ID: "att", DocID: "b", OwnerID: "alice"}); err == nil {
				t.Fatal("alice attached to bob document")
			}
			if _, ok, err := r.GetByID(bob, "b"); err != nil || !ok {
				t.Fatalf("bob could not read own document: ok=%v err=%v", ok, err)
			}
		})
	}
}
