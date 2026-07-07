// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"strconv"
	"time"
)

// Revisions issues monotonically increasing revision ids and renders the
// WordprocessingML tracked-change wrappers. Every insertion or deletion
// carries the author name, an ISO-8601 timestamp and a fresh id, so the
// output opens in Word / LibreOffice as reviewable redlines.
type Revisions struct {
	Author string
	When   time.Time
	nextID int
}

// NewRevisions returns a revision factory for the given author. The
// timestamp is normalised to UTC.
func NewRevisions(author string, when time.Time) *Revisions {
	return &Revisions{Author: author, When: when.UTC(), nextID: 1}
}

// attrs consumes the next revision id and renders the shared attributes.
func (rv *Revisions) attrs() string {
	id := rv.nextID
	rv.nextID++
	return `w:id="` + strconv.Itoa(id) + `" w:author="` + Escape(rv.Author) +
		`" w:date="` + rv.When.Format(time.RFC3339) + `"`
}

// Insertion renders text as an inserted run (<w:ins>) carrying the given
// run-properties block (pass "" for default formatting).
func (rv *Revisions) Insertion(text, runProps string) string {
	return "<w:ins " + rv.attrs() + "><w:r>" + runProps +
		`<w:t xml:space="preserve">` + Escape(text) + "</w:t></w:r></w:ins>"
}

// Deletion renders text as a deleted run (<w:del>); deleted characters live
// in <w:delText> rather than <w:t>.
func (rv *Revisions) Deletion(text, runProps string) string {
	return "<w:del " + rv.attrs() + "><w:r>" + runProps +
		`<w:delText xml:space="preserve">` + Escape(text) + "</w:delText></w:r></w:del>"
}
