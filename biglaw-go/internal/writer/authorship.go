// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Authorship / QA layer: the deterministic pass that turns "a case file exploded onto
// paper" into a memo. Everything here is mechanical Go — no model call decides whether
// process language leaks, whether a truncated sentence ships, whether a respondent's
// exposure entry is missing, or whether arithmetic is right. The model writes; this
// layer enforces.

package writer

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ─── Process-language detection ─────────────────────────────────────────────────

// reProcessTell matches the fact-checker tells that must never reach a client
// deliverable: extraction/verification to-do language ("These must be extracted from
// the full referral notice", "should be verified to determine if detail gap exists")
// and the agent-stage placeholder conclusion ("Evidence on point for this matter; see
// the quoted source."). Matched per sentence (prose) or per conclusion (findings).
var reProcessTell = regexp.MustCompile(`(?i)(` +
	`\b(must|should|need(s)?\s+to|remain(s)?\s+to|have\s+to)\s+be\s+(extracted|verified|cross-?referenced|confirmed|obtained|determined)\b` +
	`|\bverified to determine\b` +
	`|\bdetail gap\b` +
	`|\bsee the quoted source\b` +
	`|\bevidence on point for this matter\b` +
	`|\bnot provided in (your|the|this) (message|prompt|input)\b` +
	`|\bcurrent draft section was not provided\b` +
	`)`)

// isProcessConclusion reports whether a finding's conclusion is process language rather
// than substance — used to swap in the finding's verbatim evidence (or drop it) before
// anything can render it.
func isProcessConclusion(s string) bool {
	return reProcessTell.MatchString(s)
}

// stripProcessSentences removes process-tell sentences from a prose paragraph while
// keeping the substantive ones. Bullets, tables, and headings pass through untouched.
func stripProcessSentences(par string) string {
	t := strings.TrimSpace(par)
	if t == "" || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") ||
		strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") {
		return par
	}
	if !reProcessTell.MatchString(par) {
		return par
	}
	var keep []string
	for _, s := range splitSentences(par) {
		if reProcessTell.MatchString(s) {
			continue
		}
		keep = append(keep, s)
	}
	return strings.TrimSpace(strings.Join(keep, " "))
}

// splitSentences splits prose on sentence terminators (decimal- and quote-aware enough
// for QA purposes; over-splitting an abbreviation is harmless here).
func splitSentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '.' && s[i] != '!' && s[i] != '?' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == '"' || s[j] == '\'' || s[j] == ')') {
			j++
		}
		if j < len(s) && s[j] != ' ' && s[j] != '\n' {
			continue // mid-token (a decimal, "No.4", a citation) — not a boundary
		}
		if sent := strings.TrimSpace(s[start:j]); sent != "" {
			out = append(out, sent)
		}
		start = j
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

// ─── Section polish: orphans, truncation, ledger runs, duplicate headings ──────

// reLedgerAmountRow matches an exhibit/ledger row pasted as a line: optional label text
// then a bare trailing $ amount ("Owner Distribution - K.Ostrowski - May 2022 $15,000.00").
var reLedgerAmountRow = regexp.MustCompile(`^(?:[-*]\s+)?(.{0,90}?)\s*(\$\d[\d,]*(?:\.\d{1,2})?)\s*$`)

// numericRow reports whether a line is a raw stats/table row (many numeric fields, no
// sentence shape) — e.g. "August 2022 130 56 43.1% 38 26 68.4%".
func numericRow(s string) bool {
	fields := strings.Fields(s)
	if len(fields) < 5 {
		return false
	}
	num := 0
	for _, f := range fields {
		if strings.ContainsAny(f, "0123456789") {
			num++
		}
	}
	return num*10 >= len(fields)*6 // ≥60% numeric-bearing fields
}

func isLedgerLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "#") {
		return false
	}
	if endsSentence(t) {
		return false
	}
	if m := reLedgerAmountRow.FindStringSubmatch(t); m != nil {
		return true
	}
	return numericRow(t)
}

// collapseLedgerRuns converts runs of ≥3 consecutive ledger-like lines into a markdown
// table — data rendered as data, not as consecutive orphan bullets.
func collapseLedgerRuns(lines []string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		if !isLedgerLine(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && isLedgerLine(lines[j]) {
			j++
		}
		if j-i < 3 { // too short to be a table — leave for the orphan filter
			out = append(out, lines[i:j]...)
			i = j
			continue
		}
		run := lines[i:j]
		allAmount := true
		for _, ln := range run {
			if reLedgerAmountRow.FindStringSubmatch(strings.TrimSpace(ln)) == nil {
				allAmount = false
				break
			}
		}
		if allAmount {
			out = append(out, "| Entry | Amount |", "| --- | --- |")
			for _, ln := range run {
				m := reLedgerAmountRow.FindStringSubmatch(strings.TrimSpace(ln))
				label := strings.Trim(strings.TrimSpace(m[1]), "-–—:| ")
				if label == "" {
					label = "—"
				}
				out = append(out, fmt.Sprintf("| %s | %s |", label, m[2]))
			}
		} else {
			out = append(out, "| Source record |", "| --- |")
			for _, ln := range run {
				out = append(out, "| "+strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-* "))+" |")
			}
		}
		i = j
	}
	return out
}

// endsSentence reports whether a line ends like a complete sentence (terminal
// punctuation, allowing closing quotes/parens/emphasis).
func endsSentence(s string) bool {
	t := strings.TrimRight(strings.TrimSpace(s), ")\"'”’*]")
	return strings.HasSuffix(t, ".") || strings.HasSuffix(t, "!") || strings.HasSuffix(t, "?")
}

// isOrphanLine flags a bare fragment pasted as its own line ("Chief Compliance
// Officer", "Code of Ethics", a stray cite) — short, unterminated, not a bullet,
// heading, table row, or lead-in.
func isOrphanLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "#") ||
		strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") {
		return false
	}
	if endsSentence(t) || strings.HasSuffix(t, ":") {
		return false
	}
	return len(strings.Fields(t)) <= 8
}

// lastSentenceEnd returns the index just past the last sentence terminator in s
// (quote/paren-aware, decimal-safe), or -1 if none.
func lastSentenceEnd(s string) int {
	best := -1
	for i := 0; i < len(s); i++ {
		if s[i] != '.' && s[i] != '!' && s[i] != '?' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == '"' || s[j] == '\'' || s[j] == ')') {
			j++
		}
		if j >= len(s) || s[j] == ' ' || s[j] == '\n' {
			best = j
		}
	}
	return best
}

// trimTruncatedTail guarantees the section never ends mid-sentence: if the final prose
// line lacks a sentence terminator, it is cut back to its last complete sentence, or
// dropped entirely when it contains none ("…documented in trading logs and" → gone).
// Bullets, tables, headings, lead-ins, and ledger rows are left alone.
func trimTruncatedTail(s string) string {
	s = strings.TrimRight(s, " \t\n")
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "#") ||
			strings.HasPrefix(t, "|") || endsSentence(t) || strings.HasSuffix(t, ":") || isLedgerLine(t) {
			return s
		}
		if k := lastSentenceEnd(t); k > 0 {
			lines[i] = strings.TrimSpace(t[:k])
			return strings.Join(lines[:i+1], "\n")
		}
		return strings.TrimRight(strings.Join(lines[:i], "\n"), " \t\n")
	}
	return s
}

// polishSection is the per-section authorship pass, applied after sanitizeDraft:
//  1. drop a leading line that merely repeats the section title,
//  2. collapse pasted ledger/stat runs into tables,
//  3. drop orphan fragment lines,
//  4. strip process-tell sentences from prose,
//  5. never end mid-sentence.
func polishSection(title, s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	// (1) leading duplicate title
	for len(lines) > 0 {
		first := strings.Trim(strings.TrimSpace(lines[0]), "#* ")
		if first != "" && strings.EqualFold(first, strings.TrimSpace(title)) {
			lines = lines[1:]
			continue
		}
		break
	}
	// (2) ledger runs → tables
	lines = collapseLedgerRuns(lines)
	// (3) orphans + (4) process sentences
	var keep []string
	for _, ln := range lines {
		if isOrphanLine(ln) {
			continue
		}
		ln = stripProcessSentences(ln)
		if strings.TrimSpace(ln) == "" && len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
			continue
		}
		keep = append(keep, ln)
	}
	out := strings.TrimSpace(strings.Join(keep, "\n"))
	// (5) sentence-boundary tail
	return strings.TrimSpace(trimTruncatedTail(out))
}

// ─── Document-level duplicate suppression ───────────────────────────────────────

const paraKeyLen = 160

// paraKey normalizes a paragraph/line to its leading alphanumerics for dedup.
func paraKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= paraKeyLen {
				break
			}
		}
	}
	return b.String()
}

// dedupeDocBlocks removes wholesale-repeated blocks across the assembled document:
// duplicate paragraphs (first occurrence wins) and repeated figure bullets (the same
// "Key figures" line pasted under two sections). Headings and short lines are exempt.
func dedupeDocBlocks(doc string) string {
	paras := strings.Split(doc, "\n\n")
	seenPara := map[string]bool{}
	seenLine := map[string]bool{}
	var out []string
	for _, p := range paras {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "#") && len(t) >= 80 {
			k := paraKey(t)
			if seenPara[k] {
				continue
			}
			seenPara[k] = true
		}
		// Line-level: dedupe repeated figure bullets document-wide.
		var keep []string
		for _, ln := range strings.Split(p, "\n") {
			lt := strings.TrimSpace(ln)
			if strings.HasPrefix(lt, "- ") && len(lt) > 40 && strings.ContainsAny(lt, "$%") {
				k := paraKey(lt)
				if seenLine[k] {
					continue
				}
				seenLine[k] = true
			}
			keep = append(keep, ln)
		}
		block := strings.TrimSpace(strings.Join(keep, "\n"))
		// A figure-list header whose bullets all deduped away is an empty shell — drop it.
		if strings.HasPrefix(block, "**Key figures:**") && !strings.Contains(block, "\n") {
			continue
		}
		if block != "" {
			out = append(out, block)
		}
	}
	return strings.Join(out, "\n\n")
}

// ─── Mechanical figure roll-ups ─────────────────────────────────────────────────

var reMoney = regexp.MustCompile(`\$\d[\d,]*(?:\.\d{1,2})?(?:\s*(?:million|billion))?`)
var rePct = regexp.MustCompile(`\d+(?:\.\d+)?%`)

// parseMoneyCents parses a canonical $ string (with optional million/billion suffix)
// into integer cents. Returns ok=false on anything it cannot parse exactly.
func parseMoneyCents(s string) (int64, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	switch {
	case strings.HasSuffix(t, "billion"):
		mult, t = 1e9, strings.TrimSpace(strings.TrimSuffix(t, "billion"))
	case strings.HasSuffix(t, "million"):
		mult, t = 1e6, strings.TrimSpace(strings.TrimSuffix(t, "million"))
	}
	t = strings.ReplaceAll(strings.TrimPrefix(t, "$"), ",", "")
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return int64(v*mult*100 + 0.5), true
}

type moneyFig struct {
	cents  int64
	canon  string // the figure exactly as grounded — presented verbatim
	entity string
}

// collectMoney extracts the distinct grounded dollar amounts from the fact ledger
// (deduped by value; first-seen canonical string wins), largest first, capped.
func collectMoney(facts []Fact) []moneyFig {
	const maxAmounts = 30
	seen := map[int64]bool{}
	var out []moneyFig
	for _, f := range facts {
		for _, m := range reMoney.FindAllString(f.Line, -1) {
			c, ok := parseMoneyCents(m)
			if !ok || c < 100_000 || seen[c] { // ignore sub-$1,000 noise
				continue
			}
			seen[c] = true
			out = append(out, moneyFig{cents: c, canon: m, entity: f.Entity})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].cents > out[j].cents })
	if len(out) > maxAmounts {
		out = out[:maxAmounts]
	}
	return out
}

// computeRollups finds grounded aggregates that are EXACT sums of other grounded
// amounts and renders each with its components — "$7,800,000 + $438,000 = $8,238,000"
// — computed in Go, presented with the canonical values verbatim. Never model-typed
// arithmetic: a total is only asserted when the grounded components actually sum to a
// grounded aggregate.
func computeRollups(facts []Fact) []string {
	figs := collectMoney(facts)
	var out []string
	for i, t := range figs {
		if t.cents < 1_000_000 { // totals below $10,000 aren't memo roll-ups
			continue
		}
		comps := figs[i+1:]
		if expr, ok := findSum(t, comps); ok {
			out = append(out, expr)
		}
		if len(out) >= 4 {
			break
		}
	}
	return out
}

// findSum searches 2- then 3-combinations of strictly-smaller grounded amounts that sum
// exactly to the total, returning the rendered expression.
func findSum(total moneyFig, comps []moneyFig) (string, bool) {
	render := func(parts ...moneyFig) string {
		names := make([]string, len(parts))
		for i, p := range parts {
			names[i] = p.canon
		}
		return fmt.Sprintf("- %s + %s = %s (components on the grounded record sum to the stated aggregate)",
			names[0], strings.Join(names[1:], " + "), total.canon)
	}
	for a := 0; a < len(comps); a++ {
		for b := a + 1; b < len(comps); b++ {
			if comps[a].cents+comps[b].cents == total.cents {
				return render(comps[a], comps[b]), true
			}
		}
	}
	for a := 0; a < len(comps); a++ {
		for b := a + 1; b < len(comps); b++ {
			if comps[a].cents+comps[b].cents >= total.cents {
				continue
			}
			for c := b + 1; c < len(comps); c++ {
				if comps[a].cents+comps[b].cents+comps[c].cents == total.cents {
					return render(comps[a], comps[b], comps[c]), true
				}
			}
		}
	}
	return "", false
}

// ─── Respondent roster enforcement ──────────────────────────────────────────────

// corpSuffix marks the word AFTER a surname that means the mention is the FIRM, not the
// person ("Whitmore Capital Advisors" must not count as covering Gerald Whitmore).
var corpSuffix = map[string]bool{
	"capital": true, "advisors": true, "advisers": true, "llc": true, "llp": true,
	"lp": true, "inc": true, "inc.": true, "corp": true, "corp.": true, "fund": true,
	"partners": true, "trading": true, "company": true, "holdings": true, "group": true,
	"management": true, "securities": true,
}

// lastNameToken returns the person's surname token (last alphabetic word ≥ 3 chars).
func lastNameToken(name string) string {
	fields := strings.Fields(name)
	for i := len(fields) - 1; i >= 0; i-- {
		t := strings.Trim(fields[i], ".,")
		if len(t) >= 3 {
			return t
		}
	}
	return strings.TrimSpace(name)
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// nameCovered reports whether text mentions the PERSON (surname on a word boundary,
// not immediately followed by a corporate suffix word).
func nameCovered(text, name string) bool {
	ln := strings.ToLower(lastNameToken(name))
	if ln == "" {
		return false
	}
	lt := strings.ToLower(text)
	for i := 0; ; {
		j := strings.Index(lt[i:], ln)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(ln)
		i = end
		if j > 0 && isWordByte(lt[j-1]) {
			continue
		}
		if end < len(lt) && isWordByte(lt[end]) {
			continue
		}
		rest := strings.Fields(lt[end:])
		if len(rest) > 0 && corpSuffix[strings.Trim(rest[0], ".,;:()")] {
			continue // the firm, not the person
		}
		return true
	}
}

const rosterHeader = "**Consolidated exposure by respondent:**"

// rosterBlock renders one guaranteed exposure entry per named individual respondent:
// the respondent's grounded facts consolidated in one place (amounts, rates, roles)
// when the record has them, or an explicit gap note when it does not — never silence.
func (w *Writer) rosterBlock() string {
	if len(w.opt.Respondents) == 0 {
		return ""
	}
	var entries []string
	for _, r := range w.opt.Respondents {
		entries = append(entries, "- "+w.respondentEntry(r))
	}
	return rosterHeader + "\n" + strings.Join(entries, "\n")
}

// respondentEntry consolidates one respondent's grounded record: fact lines mentioning
// the person plus the distinct figures ($ amounts and rates) those facts carry. With no
// facts on record, it emits the explicit gap note the reviewer can act on.
func (w *Writer) respondentEntry(name string) string {
	var parts []string
	figSeen := map[string]bool{}
	var figures []string
	for _, f := range w.opt.Facts {
		if !nameCovered(f.Entity, name) && !nameCovered(f.Line, name) {
			continue
		}
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(f.Line), "- "))
		if line == "" || isProcessConclusion(line) {
			continue
		}
		if len(parts) < 6 {
			parts = append(parts, strings.TrimRight(line, "."))
		}
		for _, m := range append(reMoney.FindAllString(f.Line, -1), rePct.FindAllString(f.Line, -1)...) {
			if k := strings.ToLower(m); !figSeen[k] && len(figures) < 8 {
				figSeen[k] = true
				figures = append(figures, m)
			}
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("**%s** — No individual-exposure findings were extracted for %s — review the source directly.", name, name)
	}
	entry := fmt.Sprintf("**%s** — %s.", name, strings.Join(parts, "; "))
	if len(figures) > 0 {
		entry += " Grounded figures on record: " + strings.Join(figures, "; ") + "."
	}
	return entry
}
