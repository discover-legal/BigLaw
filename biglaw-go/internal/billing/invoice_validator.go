// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// InvoiceValidator — OCG-backed invoice review: mechanical rate/block checks
// then Haiku semantic check, with optional Sonnet dispute letter generation.

package billing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

var taskVerbRE = regexp.MustCompile(`(?i)\b(review|draft|research|analyz|prepar|attend|correspond|negotiate|revise|edit|call|confer|meet|discuss|investigat|file|respond|communicat|strateg)\b`)
var rateRE = regexp.MustCompile(`(?i)\$?([\d,]+)\s*(?:per|/)?\s*hour`)
var classRE = regexp.MustCompile(`(?i)\b(partner|associate|counsel|paralegal|senior|junior)\b`)

// InvoiceValidator validates invoice line items against an OCG document.
type InvoiceValidator struct {
	provider providers.Provider
	haiku    string
	sonnet   string
}

// NewInvoiceValidator creates an InvoiceValidator.
func NewInvoiceValidator(provider providers.Provider, haikuModel, sonnetModel string) *InvoiceValidator {
	return &InvoiceValidator{provider: provider, haiku: haikuModel, sonnet: sonnetModel}
}

// ValidateOpts controls validation behaviour.
type ValidateOpts struct {
	ClientID              string
	SubmittedByFirm       string
	MatterNumber          string
	GenerateDisputeLetter bool
	TaskID                string
}

// Validate checks invoice items against an OCG document.
// Pass nil ocgDoc to skip OCG-specific checks.
func (v *InvoiceValidator) Validate(
	items []types.InvoiceLineItem,
	ocgDoc *types.OcgDocument,
	opts ValidateOpts,
) *types.InvoiceValidationResult {
	if len(items) == 0 {
		return &types.InvoiceValidationResult{
			ID:          uuid.New().String(),
			ClientID:    opts.ClientID,
			ValidatedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}

	var violations []types.InvoiceViolation

	rateCaps := extractRateCaps(ocgDoc)
	var blockBillingRuleID, blockBillingRuleText string
	if ocgDoc != nil {
		for _, r := range ocgDoc.Rules {
			if r.Category == types.OcgCategoryBillingIncrements {
				blockBillingRuleID = r.ID
				blockBillingRuleText = r.Text
				break
			}
		}
	}

	// Pass 1 — Mechanical
	for _, item := range items {
		if v2 := checkVague(item); v2 != nil {
			violations = append(violations, *v2)
		}
		if v2 := checkBlockBilling(item, blockBillingRuleID, blockBillingRuleText); v2 != nil {
			violations = append(violations, *v2)
		}
		cls := strings.ToLower(item.TimekeeperClass)
		cap, ok := rateCaps[cls]
		if !ok {
			cap, ok = rateCaps["default"]
		}
		if ok {
			if v2 := checkRateCap(item, cap); v2 != nil {
				violations = append(violations, *v2)
			}
		}
	}

	// Pass 2 — Semantic (Haiku)
	semantic := v.semanticCheck(items, ocgDoc, opts.TaskID)
	violations = append(violations, semantic...)

	// Deduplicate by lineId+type
	seen := map[string]bool{}
	var deduped []types.InvoiceViolation
	for _, viol := range violations {
		key := viol.LineID + "::" + viol.Type
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, viol)
		}
	}

	totalOriginal := 0.0
	for _, item := range items {
		if item.Amount != nil {
			totalOriginal += *item.Amount
		}
	}
	totalReduction := 0.0
	hardCount := 0
	for _, viol := range deduped {
		if viol.SuggestedReduction != nil {
			totalReduction += *viol.SuggestedReduction
		}
		if viol.Severity == "hard" {
			hardCount++
		}
	}

	var disputeLetter string
	if opts.GenerateDisputeLetter && hardCount > 0 {
		disputeLetter = v.generateDisputeLetter(items, deduped, ocgDoc, opts.SubmittedByFirm, opts.MatterNumber, opts.TaskID)
	}

	return &types.InvoiceValidationResult{
		ID:                      uuid.New().String(),
		ClientID:                opts.ClientID,
		SubmittedByFirm:         opts.SubmittedByFirm,
		MatterNumber:            opts.MatterNumber,
		TotalOriginalAmount:     round2(totalOriginal),
		TotalSuggestedReduction: round2(totalReduction),
		TotalApprovedAmount:     round2(totalOriginal - totalReduction),
		LineCount:               len(items),
		ViolationCount:          len(deduped),
		HardViolationCount:      hardCount,
		Violations:              deduped,
		DisputeLetter:           disputeLetter,
		ValidatedAt:             time.Now().UTC().Format(time.RFC3339),
	}
}

// ledesHeaderNormRE mirrors the TS header normalizer: uppercase, then any
// character outside [A-Z0-9_/] becomes "_".
var ledesHeaderNormRE = regexp.MustCompile(`[^A-Z0-9_/]`)

// ParseLEDES parses LEDES 1998B pipe-delimited invoice text into line items.
func ParseLEDES(text string) []types.InvoiceLineItem {
	var items []types.InvoiceLineItem

	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(strings.ToUpper(l), "LEDES") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return items
	}

	// Column map: lineNum, date, tkName, tkClass, taskCode, actCode, desc,
	// qty, rate, total. Default to the standard LEDES column order; if the
	// first line is a column header, resolve indices by name instead of
	// trusting position. Accept the LEDES1998B aliases
	// LINE_ITEM_NUMBER_OF_UNITS / LINE_ITEM_AM_BILLED so a file produced by
	// ExportLedes1998B round-trips without silently losing units and amount.
	colMap := []int{8, 10, 15, 16, 11, 13, 17, 19, 18, 21}
	startIdx := 0
	firstLower := strings.ToLower(lines[0])
	if strings.Contains(firstLower, "line_item") || strings.Contains(firstLower, "timekeeper") || strings.Contains(firstLower, "invoice") {
		rawHeaders := splitPipe(lines[0])
		headers := make([]string, len(rawHeaders))
		for i, h := range rawHeaders {
			headers[i] = ledesHeaderNormRE.ReplaceAllString(strings.ToUpper(h), "_")
		}
		col := func(names ...string) int {
			for _, n := range names {
				for i, h := range headers {
					if h == n {
						return i
					}
				}
			}
			return -1
		}
		colMap = []int{
			col("LINE_ITEM_NUMBER"),
			col("LINE_ITEM_DATE"),
			col("TIMEKEEPER_NAME"),
			col("TIMEKEEPER_CLASSIFICATION"),
			col("LINE_ITEM_TASK_CODE"),
			col("LINE_ITEM_ACTIVITY_CODE"),
			col("LINE_ITEM_DESCRIPTION"),
			col("LINE_ITEM_QUANTITY", "LINE_ITEM_NUMBER_OF_UNITS"),
			col("LINE_ITEM_UNIT_COST"),
			col("LINE_ITEM_TOTAL", "LINE_ITEM_AM_BILLED"),
		}
		startIdx = 1
	}

	for _, line := range lines[startIdx:] {
		fields := splitPipe(line)
		if len(fields) < 3 {
			continue
		}
		g := func(idx int) string {
			if idx >= 0 && idx < len(fields) {
				return strings.TrimSpace(fields[idx])
			}
			return ""
		}
		lineID := g(colMap[0])
		if lineID == "" {
			lineID = uuid.New().String()
		}
		item := types.InvoiceLineItem{
			LineID:          lineID,
			Date:            g(colMap[1]),
			TimekeeperName:  g(colMap[2]),
			TimekeeperClass: g(colMap[3]),
			TaskCode:        g(colMap[4]),
			ActivityCode:    g(colMap[5]),
			Description:     g(colMap[6]),
		}
		if h, err := parseFloat(g(colMap[7])); err == nil {
			item.Hours = &h
		}
		if r, err := parseFloat(g(colMap[8])); err == nil {
			item.Rate = &r
		}
		if a, err := parseFloat(g(colMap[9])); err == nil {
			item.Amount = &a
		}
		if item.Description != "" || item.Hours != nil {
			items = append(items, item)
		}
	}
	return items
}

// ─── Mechanical checkers ──────────────────────────────────────────────────────

func extractRateCaps(ocgDoc *types.OcgDocument) map[string]float64 {
	caps := map[string]float64{}
	if ocgDoc == nil {
		return caps
	}
	for _, r := range ocgDoc.Rules {
		if r.Category != types.OcgCategoryRateLimits {
			continue
		}
		m := rateRE.FindStringSubmatch(r.Text)
		if m == nil {
			continue
		}
		capVal, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil || capVal <= 0 {
			continue
		}
		cm := classRE.FindStringSubmatch(r.Text)
		key := "default"
		if cm != nil {
			key = strings.ToLower(cm[1])
		}
		caps[key] = capVal
	}
	return caps
}

func checkVague(item types.InvoiceLineItem) *types.InvoiceViolation {
	if item.Description == "" {
		return &types.InvoiceViolation{
			LineID:          item.LineID,
			Type:            "vague_description",
			Severity:        "soft",
			Message:         "Billing entry has no description",
			SuggestedAction: "request_detail",
		}
	}
	words := strings.Fields(item.Description)
	if len(words) < 5 {
		return &types.InvoiceViolation{
			LineID:          item.LineID,
			Type:            "vague_description",
			Severity:        "soft",
			Message:         fmt.Sprintf("Billing description is too vague (%d words): %q", len(words), item.Description),
			SuggestedAction: "request_detail",
		}
	}
	return nil
}

func checkBlockBilling(item types.InvoiceLineItem, ruleID, ruleText string) *types.InvoiceViolation {
	if item.Description == "" {
		return nil
	}
	matches := taskVerbRE.FindAllString(item.Description, -1)
	unique := map[string]bool{}
	for _, m := range matches {
		unique[strings.ToLower(m)] = true
	}
	if len(unique) < 3 {
		return nil
	}
	var reduction *float64
	if item.Amount != nil {
		v := *item.Amount * 0.2
		v = round2(v)
		reduction = &v
	}
	desc := item.Description
	if len(desc) > 120 {
		desc = strutil.Truncate(desc, 120) + "..."
	}
	return &types.InvoiceViolation{
		LineID:             item.LineID,
		RuleID:             ruleID,
		RuleText:           ruleText,
		Type:               "block_billing",
		Severity:           "hard",
		Message:            fmt.Sprintf("Block billing detected: %d distinct tasks combined in one entry (%q)", len(unique), desc),
		SuggestedAction:    "request_detail",
		SuggestedReduction: reduction,
	}
}

func checkRateCap(item types.InvoiceLineItem, maxRate float64) *types.InvoiceViolation {
	if item.Rate == nil || *item.Rate <= maxRate {
		return nil
	}
	hours := 1.0
	if item.Hours != nil {
		hours = *item.Hours
	}
	red := round2((*item.Rate - maxRate) * hours)
	cls := item.TimekeeperClass
	if cls == "" {
		cls = "this classification"
	}
	return &types.InvoiceViolation{
		LineID:             item.LineID,
		Type:               "rate_exceeded",
		Severity:           "hard",
		Message:            fmt.Sprintf("Timekeeper rate $%.0f/hr exceeds OCG cap of $%.0f/hr for %s", *item.Rate, maxRate, cls),
		SuggestedAction:    "reduce",
		SuggestedReduction: &red,
	}
}

// ─── Semantic check (Haiku) ───────────────────────────────────────────────────

func (v *InvoiceValidator) semanticCheck(items []types.InvoiceLineItem, ocgDoc *types.OcgDocument, taskID string) []types.InvoiceViolation {
	if len(items) == 0 {
		return nil
	}
	const batchSize = 15
	var allViolations []types.InvoiceViolation

	ocgRules := "(no OCG rules provided)"
	if ocgDoc != nil {
		var ruleLines []string
		for _, r := range ocgDoc.Rules {
			if r.Category == types.OcgCategoryBillingIncrements || r.Category == types.OcgCategoryRateLimits {
				continue
			}
			ruleLines = append(ruleLines, fmt.Sprintf("[%s] (%s, %s) %s", r.ID, r.Category, r.Severity, r.Text))
			if len(ruleLines) >= 15 {
				break
			}
		}
		if len(ruleLines) > 0 {
			ocgRules = strings.Join(ruleLines, "\n")
		}
	}

	for i := 0; i < len(items); i += batchSize {
		end := i + batchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		var sb strings.Builder
		for _, item := range batch {
			hours := "?"
			if item.Hours != nil {
				hours = fmt.Sprintf("%.2f", *item.Hours)
			}
			rate := "?"
			if item.Rate != nil {
				rate = fmt.Sprintf("%.2f", *item.Rate)
			}
			amt := "?"
			if item.Amount != nil {
				amt = fmt.Sprintf("%.2f", *item.Amount)
			}
			fmt.Fprintf(&sb, "[LINE-%s] Date:%s Timekeeper:%s (%s) | TaskCode:%s ActivityCode:%s | Hours:%s Rate:%s Amt:%s | Desc:%q\n",
				item.LineID, item.Date, item.TimekeeperName, item.TimekeeperClass,
				item.TaskCode, item.ActivityCode, hours, rate, amt, item.Description)
		}

		systemPrompt := fmt.Sprintf(`You are an in-house legal billing auditor reviewing outside counsel invoices against OCG (Outside Counsel Guidelines).

For each billing line item, identify semantic violations NOT already caught by mechanical checks (block billing, rate caps, vague descriptions).

Look for:
- Unauthorised task types (tasks not in scope per the engagement letter or OCG)
- Excessive hours relative to the described task
- Inappropriate staffing (senior timekeeper performing clerical/junior work)
- Internal firm administrative tasks billed to client
- Duplicate entries for the same work
- Research that should have been done as part of another billable task

OCG RULES IN FORCE:
%s

For each violation found, return JSON:
{"lineId":"LINE-xxx","type":"unauthorized_task|excessive_hours|staffing_violation|other","severity":"hard|soft","message":"brief explanation","suggestedAction":"reject|reduce|request_detail","suggestedReduction":USD_or_null}

Return a JSON array of violations. Return [] if none found.`, ocgRules)

		start := time.Now()
		resp, err := v.provider.Chat(providers.ChatParams{
			Model:       v.haiku,
			MaxTokens:   1024,
			System:      systemPrompt,
			CacheSystem: true,
			Messages:    []providers.Message{{Role: "user", Content: "Invoice line items:\n" + sb.String()}},
		})
		if err != nil {
			slog.Warn("InvoiceValidator semantic check failed", "error", err)
			continue
		}
		dms := time.Since(start).Milliseconds()
		costUSD := cost.CalcCostUSD(v.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
		cost.Default.Record(cost.RecordRequest{
			Model: v.haiku, Provider: "anthropic",
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CostUSD: costUSD, DurationMs: dms,
			Context: "invoice_validation", TaskID: taskID,
		})

		raw := ""
		for _, blk := range resp.Content {
			if blk.Type == providers.BlockText {
				raw = blk.Text
				break
			}
		}
		raw = strings.ReplaceAll(raw, "```json", "")
		raw = strings.ReplaceAll(raw, "```", "")
		raw = strings.TrimSpace(raw)
		s := strings.Index(raw, "[")
		e := strings.LastIndex(raw, "]")
		if s < 0 || e <= s {
			continue
		}
		var parsed []map[string]interface{}
		if err := json.Unmarshal([]byte(raw[s:e+1]), &parsed); err != nil {
			continue
		}
		for _, p := range parsed {
			lineID, _ := p["lineId"].(string)
			lineID = strings.TrimPrefix(lineID, "LINE-")
			if lineID == "" {
				continue
			}
			typ, _ := p["type"].(string)
			sev, _ := p["severity"].(string)
			msg, _ := p["message"].(string)
			action, _ := p["suggestedAction"].(string)
			if sev == "" {
				sev = "soft"
			}
			if action == "" {
				action = "request_detail"
			}
			v2 := types.InvoiceViolation{
				LineID:          lineID,
				Type:            typ,
				Severity:        sev,
				Message:         msg,
				SuggestedAction: action,
			}
			if red, ok := p["suggestedReduction"].(float64); ok && red > 0 {
				r2 := round2(red)
				v2.SuggestedReduction = &r2
			}
			allViolations = append(allViolations, v2)
		}
	}
	return allViolations
}

// ─── Dispute letter (Sonnet) ──────────────────────────────────────────────────

func (v *InvoiceValidator) generateDisputeLetter(
	_ []types.InvoiceLineItem,
	violations []types.InvoiceViolation,
	ocgDoc *types.OcgDocument,
	submittedByFirm, matterNumber, taskID string,
) string {
	totalReduction := 0.0
	for _, viol := range violations {
		if viol.Severity == "hard" && viol.SuggestedReduction != nil {
			totalReduction += *viol.SuggestedReduction
		}
	}

	var sb strings.Builder
	for _, viol := range violations {
		if viol.Severity != "hard" {
			continue
		}
		red := ""
		if viol.SuggestedReduction != nil {
			red = fmt.Sprintf(" ($%.2f reduction)", *viol.SuggestedReduction)
		}
		fmt.Fprintf(&sb, "- Line %s: [%s] %s → %s%s\n", viol.LineID, viol.Type, viol.Message, viol.SuggestedAction, red)
	}

	ocgTitle := "our Outside Counsel Guidelines"
	if ocgDoc != nil {
		ocgTitle = ocgDoc.Title
	}
	if submittedByFirm == "" {
		submittedByFirm = "Outside Counsel"
	}
	if matterNumber == "" {
		matterNumber = "as referenced in invoice"
	}

	prompt := fmt.Sprintf(`Draft a professional but firm dispute letter to outside counsel billing department.

Recipient firm: %s
Matter: %s
Total suggested reduction: $%.2f
Governing OCG: %s

Violations to dispute:
%s
Requirements:
- Professional tone; factual, not adversarial
- Cite the specific OCG provisions violated
- Request revised invoice or detailed justification within 14 business days
- Sign off as "[Senior Billing Counsel]"
- Under 400 words`,
		submittedByFirm, matterNumber, totalReduction, ocgTitle, sb.String())

	start := time.Now()
	resp, err := v.provider.Chat(providers.ChatParams{
		Model:     v.sonnet,
		MaxTokens: 600,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("InvoiceValidator dispute letter failed", "error", err)
		return ""
	}
	dms := time.Since(start).Milliseconds()
	costUSD := cost.CalcCostUSD(v.sonnet, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	cost.Default.Record(cost.RecordRequest{
		Model: v.sonnet, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "invoice_validation", TaskID: taskID,
	})
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func splitPipe(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			if inQuotes && i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuotes = !inQuotes
			}
		} else if ch == '|' && !inQuotes {
			fields = append(fields, strings.TrimSpace(cur.String()))
			cur.Reset()
		} else {
			cur.WriteByte(ch)
		}
	}
	fields = append(fields, strings.TrimSpace(cur.String()))
	return fields
}

func parseFloat(s string) (float64, error) {
	s = strings.ReplaceAll(s, ",", ".")
	s = strings.TrimFunc(s, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' && r != '-' })
	return strconv.ParseFloat(s, 64)
}

func round2(f float64) float64 {
	// math.Round, not int(f*100+0.5): truncation flips the sign of rounding
	// for negative amounts (credit line items) and overflows on large f.
	return math.Round(f*100) / 100
}
