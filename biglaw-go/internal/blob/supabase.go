// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package blob

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SupabaseStore stores blobs in Supabase Storage via its native REST API
// (/storage/v1/object/...), not the S3 protocol. Supabase is open-source and
// self-hostable.
type SupabaseStore struct {
	base   string // project URL without trailing slash
	bucket string
	key    string // service-role or storage API key (Bearer)
	client *http.Client
}

func NewSupabaseStore(projectURL, bucket, apiKey string) (*SupabaseStore, error) {
	if strings.TrimSpace(projectURL) == "" || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("blob: supabase backend needs BLOB_SUPABASE_URL and BLOB_SUPABASE_BUCKET")
	}
	return &SupabaseStore{
		base:   strings.TrimRight(projectURL, "/"),
		bucket: bucket,
		key:    apiKey,
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (s *SupabaseStore) Backend() string { return "supabase" }

func (s *SupabaseStore) objectURL(key string) string {
	return s.base + "/storage/v1/object/" + s.bucket + "/" + strings.TrimLeft(key, "/")
}

func (s *SupabaseStore) auth(req *http.Request) {
	if s.key != "" {
		req.Header.Set("Authorization", "Bearer "+s.key)
		req.Header.Set("apikey", s.key)
	}
}

func (s *SupabaseStore) Put(key string, data []byte) error {
	// POST with x-upsert creates or overwrites.
	req, err := http.NewRequest(http.MethodPost, s.objectURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	s.auth(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-upsert", "true")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: supabase put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("blob: supabase put %s: HTTP %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *SupabaseStore) Get(key string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.objectURL(key), nil)
	if err != nil {
		return nil, err
	}
	s.auth(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: supabase get %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("blob: supabase get %s: HTTP %d", key, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (s *SupabaseStore) Delete(key string) error {
	req, err := http.NewRequest(http.MethodDelete, s.objectURL(key), nil)
	if err != nil {
		return err
	}
	s.auth(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: supabase delete %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("blob: supabase delete %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}
