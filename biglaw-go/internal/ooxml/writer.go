// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
)

// A4 page geometry in twentieths of a point (twips).
const (
	pageShortEdge = 11906 // 210 mm
	pageLongEdge  = 16838 // 297 mm
	pageMargin    = 1440  // 1 inch on every side
)

// Builder accumulates the body of a new WordprocessingML document and
// assembles it into a minimal, conformant .docx archive. Formatting is
// applied directly on runs and paragraphs so no styles.xml or numbering.xml
// part is required.
type Builder struct {
	body      strings.Builder
	landscape bool
}

// NewBuilder returns an empty portrait-orientation document builder.
func NewBuilder() *Builder { return &Builder{} }

// SetLandscape switches the document section to landscape orientation.
func (b *Builder) SetLandscape(on bool) { b.landscape = on }

// Landscape reports the current orientation.
func (b *Builder) Landscape() bool { return b.landscape }

// pageWidth returns the page width in twips for the current orientation.
func (b *Builder) pageWidth() int {
	if b.landscape {
		return pageLongEdge
	}
	return pageShortEdge
}

// usableWidth is the page width minus the left and right margins.
func (b *Builder) usableWidth() int { return b.pageWidth() - 2*pageMargin }

// textRun renders one run. halfPoints sets the font size in half-points
// (0 inherits); xml:space="preserve" keeps leading/trailing spaces.
func textRun(text string, bold bool, halfPoints int) string {
	var props strings.Builder
	if bold || halfPoints > 0 {
		props.WriteString("<w:rPr>")
		if bold {
			props.WriteString("<w:b/>")
		}
		if halfPoints > 0 {
			props.WriteString(`<w:sz w:val="` + strconv.Itoa(halfPoints) + `"/>`)
		}
		props.WriteString("</w:rPr>")
	}
	return "<w:r>" + props.String() + `<w:t xml:space="preserve">` + Escape(text) + "</w:t></w:r>"
}

// paragraph wraps runs in a paragraph with spacing before/after in twips.
func paragraph(runs string, before, after int) string {
	return `<w:p><w:pPr><w:spacing w:before="` + strconv.Itoa(before) +
		`" w:after="` + strconv.Itoa(after) + `"/></w:pPr>` + runs + "</w:p>"
}

// Heading emits a bold heading. Levels are clamped to 1-3 and rendered at
// 16pt / 13pt / 12pt respectively.
func (b *Builder) Heading(level int, text string) {
	size := 24 // level 3: 12pt
	switch {
	case level <= 1:
		size = 32 // 16pt
	case level == 2:
		size = 26 // 13pt
	}
	b.body.WriteString(paragraph(textRun(text, true, size), 240, 120))
}

// Paragraph emits a normal 11pt prose paragraph.
func (b *Builder) Paragraph(text string) {
	b.body.WriteString(paragraph(textRun(text, false, 22), 0, 120))
}

// Bullet emits a bulleted line. The bullet is a literal glyph so no
// numbering-definitions part is needed.
func (b *Builder) Bullet(text string) {
	b.body.WriteString(paragraph(textRun("•  "+text, false, 22), 0, 60))
}

// PageBreak starts a new page.
func (b *Builder) PageBreak() {
	b.body.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
}

// Spacer emits an empty paragraph. Word also requires a paragraph between a
// table and whatever follows it (including the closing section properties).
func (b *Builder) Spacer() { b.body.WriteString("<w:p/>") }

// Table emits a bordered table: one bold header row plus the given body rows.
// Every row is normalised to the header's cell count. A table with no headers
// is skipped.
func (b *Builder) Table(headers []string, rows [][]string) {
	cols := len(headers)
	if cols == 0 {
		return
	}
	colWidth := b.usableWidth() / cols

	var t strings.Builder
	t.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/><w:tblBorders>`)
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		t.WriteString("<w:" + edge + ` w:val="single" w:sz="4" w:space="0" w:color="auto"/>`)
	}
	t.WriteString("</w:tblBorders></w:tblPr><w:tblGrid>")
	for i := 0; i < cols; i++ {
		t.WriteString(`<w:gridCol w:w="` + strconv.Itoa(colWidth) + `"/>`)
	}
	t.WriteString("</w:tblGrid>")

	writeRow := func(cells []string, bold bool) {
		t.WriteString("<w:tr>")
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			t.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(colWidth) + `" w:type="dxa"/></w:tcPr>`)
			t.WriteString("<w:p>" + textRun(cell, bold, 22) + "</w:p>")
			t.WriteString("</w:tc>")
		}
		t.WriteString("</w:tr>")
	}
	writeRow(headers, true)
	for _, row := range rows {
		writeRow(row, false)
	}
	t.WriteString("</w:tbl>")
	b.body.WriteString(t.String())
}

// sectionProperties renders the document-level section: page size (with
// orientation) and one-inch margins.
func (b *Builder) sectionProperties() string {
	if b.landscape {
		return `<w:sectPr><w:pgSz w:w="` + strconv.Itoa(pageLongEdge) + `" w:h="` + strconv.Itoa(pageShortEdge) +
			`" w:orient="landscape"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`
	}
	return `<w:sectPr><w:pgSz w:w="` + strconv.Itoa(pageShortEdge) + `" w:h="` + strconv.Itoa(pageLongEdge) +
		`"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>`
}

// Bytes assembles the three required OPC parts into a .docx ZIP archive.
func (b *Builder) Bytes() ([]byte, error) {
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
<w:body>` + b.body.String() + b.sectionProperties() + `</w:body>
</w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range []struct{ name, data string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rels},
		{"word/document.xml", document},
	} {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.data)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
