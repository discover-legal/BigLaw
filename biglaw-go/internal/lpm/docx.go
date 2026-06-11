// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Minimal, dependency-free OOXML (.docx) writer. A .docx is a ZIP of XML parts;
// this builds the three parts a conformant reader requires ([Content_Types].xml,
// _rels/.rels, word/document.xml) and supports headings, paragraphs, bullets and
// bold "label: value" runs — enough for a clean stakeholder status report without
// pulling a heavyweight Office library onto the low-power box.
package lpm

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
)

// docxBuilder accumulates body paragraphs as OOXML fragments.
type docxBuilder struct {
	body strings.Builder
}

// xmlEscape escapes the five XML predefined entities.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

// run renders a single text run, optionally bold, at the given half-point size
// (0 = inherit). xml:space="preserve" keeps leading/trailing spaces intact.
func run(text string, bold bool, halfPt int) string {
	var rpr strings.Builder
	if bold || halfPt > 0 {
		rpr.WriteString("<w:rPr>")
		if bold {
			rpr.WriteString("<w:b/>")
		}
		if halfPt > 0 {
			rpr.WriteString("<w:sz w:val=\"" + strconv.Itoa(halfPt) + "\"/>")
		}
		rpr.WriteString("</w:rPr>")
	}
	return "<w:r>" + rpr.String() + "<w:t xml:space=\"preserve\">" + xmlEscape(text) + "</w:t></w:r>"
}

// para wraps runs in a paragraph with optional spacing-before/after (twips).
func para(runs string, before, after int) string {
	return "<w:p><w:pPr><w:spacing w:before=\"" + strconv.Itoa(before) +
		"\" w:after=\"" + strconv.Itoa(after) + "\"/></w:pPr>" + runs + "</w:p>"
}

// Heading adds a bold heading. level 1→16pt, 2→13pt, 3→12pt.
func (d *docxBuilder) Heading(level int, text string) {
	sz := 24 // level 3 / default (12pt)
	switch level {
	case 1:
		sz = 32
	case 2:
		sz = 26
	}
	d.body.WriteString(para(run(text, true, sz), 200, 80))
}

// Para adds a normal body paragraph (11pt).
func (d *docxBuilder) Para(text string) {
	d.body.WriteString(para(run(text, false, 22), 0, 80))
}

// Bullet adds a simple bulleted line (rendered with a literal bullet glyph to
// avoid a numbering definition part).
func (d *docxBuilder) Bullet(text string) {
	d.body.WriteString(para(run("•  "+text, false, 22), 0, 40))
}

// Labeled adds a "label: value" paragraph with the label in bold.
func (d *docxBuilder) Labeled(label, value string) {
	d.body.WriteString(para(run(label+": ", true, 22)+run(value, false, 22), 0, 40))
}

// Bytes assembles the parts into a .docx ZIP archive.
func (d *docxBuilder) Bytes() ([]byte, error) {
	const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

	const rels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + d.body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body>
</w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rels},
		{"word/document.xml", document},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
