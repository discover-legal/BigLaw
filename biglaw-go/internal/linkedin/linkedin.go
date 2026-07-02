// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// LinkedIn data-export parser — RFC 4180 CSV + minimal ZIP reader.
// Zip-bomb guard: decompressed output per entry is capped at 50 MB.
// ParseLinkedInExport never throws — malformed input returns an empty slice.

package linkedin

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"strings"
)

const maxInflatedBytes = 50 * 1024 * 1024 // 50 MB zip-bomb guard

// ParseCSV parses RFC 4180 CSV, handling quoted fields and embedded newlines.
func ParseCSV(text string) [][]string {
	var rows [][]string
	var row []string
	var field strings.Builder
	inQuote := false

	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inQuote {
			if ch == '"' && i+1 < len(text) && text[i+1] == '"' {
				field.WriteByte('"')
				i++
			} else if ch == '"' {
				inQuote = false
			} else {
				field.WriteByte(ch)
			}
		} else {
			switch ch {
			case '"':
				inQuote = true
			case ',':
				row = append(row, field.String())
				field.Reset()
			case '\r':
				if i+1 < len(text) && text[i+1] == '\n' {
					i++
				}
				row = append(row, field.String())
				field.Reset()
				rows = append(rows, row)
				row = nil
			case '\n':
				row = append(row, field.String())
				field.Reset()
				rows = append(rows, row)
				row = nil
			default:
				field.WriteByte(ch)
			}
		}
	}
	if field.Len() > 0 || len(row) > 0 {
		row = append(row, field.String())
		rows = append(rows, row)
	}
	return rows
}

func extractPostsFromCSV(csv string) []string {
	rows := ParseCSV(csv)
	if len(rows) < 2 {
		return nil
	}
	headers := rows[0]
	colIdx := -1
	for i, h := range headers {
		lower := strings.ToLower(strings.TrimSpace(h))
		if strings.Contains(lower, "commentary") || lower == "post" || lower == "share commentary" {
			colIdx = i
			break
		}
	}
	if colIdx < 0 {
		return nil
	}
	var posts []string
	for _, row := range rows[1:] {
		if colIdx >= len(row) {
			continue
		}
		t := strings.TrimSpace(row[colIdx])
		if len(t) > 20 {
			posts = append(posts, t)
		}
	}
	return posts
}

// ParseLinkedInExport parses a LinkedIn data export buffer (ZIP or CSV).
// Returns post text samples. Never returns an error.
func ParseLinkedInExport(buf []byte) []string {
	// Detect ZIP by magic bytes PK\x03\x04
	if len(buf) >= 4 && buf[0] == 0x50 && buf[1] == 0x4b && buf[2] == 0x03 && buf[3] == 0x04 {
		r, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
		if err != nil {
			return nil
		}
		for _, f := range r.File {
			name := strings.ToLower(filepath.Base(f.Name))
			if !strings.Contains(name, "shares") && !strings.Contains(name, "posts") {
				continue
			}
			if f.UncompressedSize64 > maxInflatedBytes {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, maxInflatedBytes))
			rc.Close()
			if err != nil {
				continue
			}
			if posts := extractPostsFromCSV(string(data)); len(posts) > 0 {
				return posts
			}
		}
		return nil
	}
	// Treat as raw CSV
	return extractPostsFromCSV(string(buf))
}
