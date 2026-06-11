// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package csvutil provides CSV field escaping shared by every CSV emitter,
// including neutralization of spreadsheet formula injection.
package csvutil

import "strings"

// Escape returns the value as a quoted CSV field, with embedded double quotes
// doubled per RFC 4180.
//
// It also neutralizes spreadsheet formula injection: a field beginning with
// = + - @ (or a leading tab/CR) is executed as a formula by Excel, Sheets and
// LibreOffice when the CSV is opened, so such values are prefixed with a
// single quote. Several exported fields (descriptions, names, tabulate cells)
// carry LLM- or user-supplied content.
func Escape(s string) string {
	if len(s) > 0 {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			s = "'" + s
		}
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
