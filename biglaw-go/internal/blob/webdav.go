// SPDX-License-Identifier: Apache-2.0
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

// WebDAVStore stores blobs on any WebDAV server (RFC 4918) — Nextcloud,
// ownCloud, Apache mod_dav, sabre/dav, etc. An open, self-hostable file
// standard with no vendor SDK.
type WebDAVStore struct {
	base   string // base URL without trailing slash
	user   string
	pass   string
	client *http.Client
}

func NewWebDAVStore(baseURL, user, pass string) (*WebDAVStore, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("blob: webdav backend needs BLOB_WEBDAV_URL")
	}
	return &WebDAVStore{
		base:   strings.TrimRight(baseURL, "/"),
		user:   user,
		pass:   pass,
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (w *WebDAVStore) Backend() string { return "webdav" }

func (w *WebDAVStore) url(key string) string {
	return w.base + "/" + strings.TrimLeft(key, "/")
}

func (w *WebDAVStore) do(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if w.user != "" || w.pass != "" {
		req.SetBasicAuth(w.user, w.pass)
	}
	return w.client.Do(req)
}

// ensureParents MKCOLs each parent collection of key (WebDAV PUT fails if the
// containing collection does not exist). Existing collections (405/301) are fine.
func (w *WebDAVStore) ensureParents(key string) {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	if len(parts) < 2 {
		return
	}
	path := ""
	for _, p := range parts[:len(parts)-1] {
		path += "/" + p
		resp, err := w.do("MKCOL", w.base+path, nil)
		if err == nil {
			resp.Body.Close()
		}
	}
}

func (w *WebDAVStore) Put(key string, data []byte) error {
	w.ensureParents(key)
	resp, err := w.do(http.MethodPut, w.url(key), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("blob: webdav put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("blob: webdav put %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}

func (w *WebDAVStore) Get(key string) ([]byte, error) {
	resp, err := w.do(http.MethodGet, w.url(key), nil)
	if err != nil {
		return nil, fmt.Errorf("blob: webdav get %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("blob: webdav get %s: HTTP %d", key, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (w *WebDAVStore) Delete(key string) error {
	resp, err := w.do(http.MethodDelete, w.url(key), nil)
	if err != nil {
		return fmt.Errorf("blob: webdav delete %s: %w", key, err)
	}
	defer resp.Body.Close()
	// 2xx = deleted, 404 = already gone — both fine.
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("blob: webdav delete %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}
