// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// docxBuilder is a thin façade over internal/ooxml's Builder: it keeps the
// package-local surface the LPM renderers were written against (Heading,
// Para, Bullet, Labeled, Bytes) while delegating all OOXML XML and ZIP
// assembly to the shared writer.
package lpm

import "github.com/discover-legal/biglaw-go/internal/ooxml"

// docxBuilder accumulates the body of a stakeholder status report.
type docxBuilder struct {
	b ooxml.Builder
}

// Heading adds a bold heading. level 1→16pt, 2→13pt, 3→12pt.
func (d *docxBuilder) Heading(level int, text string) { d.b.Heading(level, text) }

// Para adds a normal body paragraph (11pt).
func (d *docxBuilder) Para(text string) { d.b.Paragraph(text) }

// Bullet adds a simple bulleted line.
func (d *docxBuilder) Bullet(text string) { d.b.Bullet(text) }

// Labeled adds a "label: value" paragraph with the label in bold.
func (d *docxBuilder) Labeled(label, value string) { d.b.Labeled(label, value) }

// Bytes assembles the document into a .docx ZIP archive.
func (d *docxBuilder) Bytes() ([]byte, error) { return d.b.Bytes() }
