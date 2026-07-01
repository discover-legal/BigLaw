// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package ooxml is a minimal, dependency-free reader/writer for the subset of
// ECMA-376 WordprocessingML that BigLaw's document tools need. A .docx file is
// a ZIP archive (Open Packaging Conventions) whose required parts are
// [Content_Types].xml, _rels/.rels and word/document.xml.
//
// The package supports two directions of travel:
//
//   - Builder writes new documents: headings (levels 1-3), prose paragraphs,
//     bullet items, bordered tables, explicit page breaks, and a portrait or
//     landscape page section.
//   - Document round-trips existing files: it unzips a .docx, exposes the
//     visible text of word/document.xml, performs run-level tracked-change
//     surgery (<w:ins> / <w:del> with author, ISO-8601 date and monotonic
//     revision ids), and re-zips the archive preserving every other part and
//     the original part order.
package ooxml

import "strings"

// escaper escapes the five predefined XML entities.
var escaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

// unescaper is the inverse of escaper. strings.Replacer runs a single pass
// over the input, so already-decoded output is never rescanned ("&amp;lt;"
// decodes to "&lt;", not "<").
var unescaper = strings.NewReplacer(
	"&lt;", "<",
	"&gt;", ">",
	"&quot;", `"`,
	"&apos;", "'",
	"&amp;", "&",
)

// Escape escapes the five predefined XML entities in s.
func Escape(s string) string { return escaper.Replace(s) }

// Unescape decodes the five predefined XML entities in s.
func Unescape(s string) string { return unescaper.Replace(s) }
