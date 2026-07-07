// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

// Labeled emits a "label: value" paragraph with the label rendered bold and
// the value in normal weight, both at 11pt. Used for compact metadata lines
// such as "Owner: Jane Partner" in status reports.
func (b *Builder) Labeled(label, value string) {
	b.body.WriteString(paragraph(textRun(label+": ", true, 22)+textRun(value, false, 22), 0, 60))
}
