// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"strconv"
	"strings"
)

// comment is one Word comment a Walk adds: its id (from the revision factory, so it never collides
// with a comment or change already in the document) and its body.
type comment struct {
	id   int
	text string
}

// comment registers a comment and returns its id.
func (w *walker) comment(text string) int {
	c := comment{w.rv.id(), text}
	w.comments = append(w.comments, c)
	return c.id
}

// addComments writes the comments to word/comments.xml (created with its relationship and content
// type on first use, extended otherwise), each as one paragraph by the walk's author and date.
func (d *Document) addComments(cs []comment, rv *Revisions) {
	part, ok := d.Part("word/comments.xml")
	if !ok {
		part = xmlHeader + `<w:comments xmlns:w="` + wml + `"></w:comments>`
		d.relate("comments.xml", "http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments", "application/vnd.openxmlformats-officedocument.wordprocessingml.comments+xml")
	}
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(`<w:comment w:id="` + strconv.Itoa(c.id) + `" ` + rv.who() + ` w:initials="` + Escape(initials(rv.Author)) + `">`)
		for i, line := range strings.Split(c.text, "\n") {
			ref := ""
			if i == 0 {
				ref = `<w:r><w:annotationRef/></w:r>`
			}
			b.WriteString("<w:p>" + ref + runXML(line, "", false) + "</w:p>")
		}
		b.WriteString("</w:comment>")
	}
	i := strings.LastIndex(part, "</w:comments>")
	if i < 0 {
		return
	}
	d.setPart("word/comments.xml", part[:i]+b.String()+part[i:])
}

// initials are the author's: the first letter of each word, at most three.
func initials(author string) string {
	var b strings.Builder
	for _, w := range strings.Fields(author) {
		if b.Len() >= 3 {
			break
		}
		for _, r := range w {
			b.WriteRune(r)
			break
		}
	}
	return b.String()
}
