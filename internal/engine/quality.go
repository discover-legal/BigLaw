// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"regexp"
	"strings"
	"unicode"
)

// The authorship checks carried from v1's writer (authorship.go, dedup.go): process language
// never reaches the client, a section never ends mid-sentence, orphan fragments and repeated
// blocks are dropped.

var processRe = regexp.MustCompile(`(?i)(` +
	`\b(must|should|need(s)?\s+to|remain(s)?\s+to|have\s+to)\s+be\s+(extracted|verified|cross-?referenced|confirmed|obtained|determined)\b` +
	`|\bverified to determine\b|\bdetail gap\b|\bsee the quoted source\b|\bevidence on point for this matter\b` +
	`|\bnot provided in (your|the|this) (message|prompt|input)\b|\bcurrent draft section was not provided\b` +
	`|^\s*let me\b|^\s*now (that\s+)?i have\b|^\s*i (will|'ll|can(not)?|need to|want to|am going to|appreciate)\b` +
	`|^\s*please confirm\b|^\s*you'?ve asked me\b|\bclarify my role\b|\bthe ledger\b|\btool call\b)`)

var sentenceEnd = regexp.MustCompile(`[.!?]["”’)\]]*(\s+|$)`)

// splitSentences splits prose after a terminator followed by white space; decimals ("3.5") and
// abbreviations inside a word stay whole.
func splitSentences(s string) []string {
	var out []string
	last := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(s, -1) {
		if t := strings.TrimSpace(s[last:loc[1]]); t != "" {
			out = append(out, t)
		}
		last = loc[1]
	}
	if t := strings.TrimSpace(s[last:]); t != "" {
		out = append(out, t)
	}
	return out
}

// stripProcessSentences drops process-tell sentences from a prose line; bullets, tables and
// headings pass untouched.
func stripProcessSentences(par string) string {
	t := strings.TrimSpace(par)
	if t == "" || strings.ContainsAny(t[:1], "-*#|") || !processRe.MatchString(par) {
		return par
	}
	var keep []string
	for _, s := range splitSentences(par) {
		if !processRe.MatchString(s) {
			keep = append(keep, s)
		}
	}
	return strings.Join(keep, " ")
}

// orphan is a short line with no verb-like ending: a dangling label ("Code of Ethics").
func orphan(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.ContainsAny(t[:1], "-*#|>0123456789") || strings.ContainsAny(t, ".:;!?|[") {
		return false
	}
	return len(strings.Fields(t)) <= 5
}

// polishSection is the per-section authorship pass: no leading line repeating the title, no
// orphan fragments, no process sentences, no truncated tail, no trailing lead-in.
func polishSection(title, s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for len(lines) > 0 && strings.EqualFold(strings.Trim(strings.TrimSpace(lines[0]), "#* "), strings.TrimSpace(title)) {
		lines = lines[1:]
	}
	var keep []string
	for _, ln := range lines {
		if orphan(ln) {
			continue
		}
		ln = stripProcessSentences(ln)
		if strings.TrimSpace(ln) == "" && (len(keep) == 0 || strings.TrimSpace(keep[len(keep)-1]) == "") {
			continue
		}
		keep = append(keep, ln)
	}
	for len(keep) > 0 { // a tail that stops mid-sentence, or a lead-in with nothing after it
		t := strings.TrimSpace(keep[len(keep)-1])
		if t == "" || strings.HasSuffix(t, ":") {
			keep = keep[:len(keep)-1]
			continue
		}
		if !strings.ContainsAny(t[:1], "-*#|>") && !sentenceEnd.MatchString(t[len(t)-1:]+" ") && !strings.HasSuffix(t, ")") && !strings.HasSuffix(t, "]") {
			ss := splitSentences(t)
			if len(ss) <= 1 {
				keep = keep[:len(keep)-1]
				continue
			}
			keep[len(keep)-1] = strings.Join(ss[:len(ss)-1], " ")
		}
		break
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

func paraKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			if b.Len() >= 160 {
				break
			}
		}
	}
	return b.String()
}

// dedupeDocBlocks drops a paragraph (80+ characters) or figure bullet already seen in the
// document; headings stay.
func dedupeDocBlocks(doc string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(doc, "\n\n") {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") && len(t) >= 80 {
			if seen[paraKey(t)] {
				continue
			}
			seen[paraKey(t)] = true
		}
		var keep []string
		for _, ln := range strings.Split(t, "\n") {
			if lt := strings.TrimSpace(ln); strings.HasPrefix(lt, "- ") && len(lt) > 40 && strings.ContainsAny(lt, "$%") {
				if seen["-"+paraKey(lt)] {
					continue
				}
				seen["-"+paraKey(lt)] = true
			}
			keep = append(keep, ln)
		}
		if b := strings.Join(keep, "\n"); !(strings.HasPrefix(b, "**Key figures:**") && !strings.Contains(b, "\n")) {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n\n")
}
