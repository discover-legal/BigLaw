// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// OCIStore stores each attachment as an OCI artifact in any registry that
// implements the Open Container Initiative distribution spec — self-hosted Zot
// or Distribution, Harbor, GHCR, etc. A genuinely open, vendor-neutral object
// store: blobs are content-addressed, each keyed by a tag.
type OCIStore struct {
	repo *remote.Repository
}

const (
	ociArtifactType = "application/vnd.discoverlegal.biglaw.attachment"
	ociLayerType    = "application/octet-stream"
)

func NewOCIStore(ref, user, pass string, plainHTTP bool) (*OCIStore, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("blob: oci backend needs BLOB_OCI_REF (registry/repository)")
	}
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("blob: oci ref %q: %w", ref, err)
	}
	repo.PlainHTTP = plainHTTP
	if user != "" || pass != "" {
		host := ref
		if i := strings.IndexByte(ref, '/'); i >= 0 {
			host = ref[:i]
		}
		repo.Client = &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(host, auth.Credential{Username: user, Password: pass}),
		}
	}
	return &OCIStore{repo: repo}, nil
}

func (o *OCIStore) Backend() string { return "oci" }

func ctx60() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// ociTag maps a "/"-delimited blob key to a valid OCI tag
// ([a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}). Our keys are "<uuid>/<uuid>", so "/"→"_".
func ociTag(key string) string {
	t := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(strings.TrimLeft(key, "/"))
	if len(t) > 128 {
		t = t[:128]
	}
	return t
}

func (o *OCIStore) Put(key string, data []byte) error {
	ctx, cancel := ctx60()
	defer cancel()

	layer := content.NewDescriptorFromBytes(ociLayerType, data)
	if err := o.repo.Push(ctx, layer, bytes.NewReader(data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return fmt.Errorf("blob: oci push layer %s: %w", key, err)
	}
	manifestDesc, err := oras.PackManifest(ctx, o.repo, oras.PackManifestVersion1_1, ociArtifactType,
		oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}})
	if err != nil {
		return fmt.Errorf("blob: oci pack %s: %w", key, err)
	}
	if err := o.repo.Tag(ctx, manifestDesc, ociTag(key)); err != nil {
		return fmt.Errorf("blob: oci tag %s: %w", key, err)
	}
	return nil
}

func (o *OCIStore) Get(key string) ([]byte, error) {
	ctx, cancel := ctx60()
	defer cancel()

	manifestDesc, err := o.repo.Resolve(ctx, ociTag(key))
	if err != nil {
		return nil, fmt.Errorf("blob: oci resolve %s: %w", key, err)
	}
	manifestBytes, err := content.FetchAll(ctx, o.repo, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("blob: oci fetch manifest %s: %w", key, err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("blob: oci manifest %s: %w", key, err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("blob: oci artifact %s has no layers", key)
	}
	return content.FetchAll(ctx, o.repo, m.Layers[0])
}

func (o *OCIStore) Delete(key string) error {
	ctx, cancel := ctx60()
	defer cancel()

	manifestDesc, err := o.repo.Resolve(ctx, ociTag(key))
	if errors.Is(err, errdef.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("blob: oci resolve %s: %w", key, err)
	}
	if err := o.repo.Delete(ctx, manifestDesc); err != nil && !errors.Is(err, errdef.ErrNotFound) {
		return fmt.Errorf("blob: oci delete %s: %w", key, err)
	}
	return nil
}
