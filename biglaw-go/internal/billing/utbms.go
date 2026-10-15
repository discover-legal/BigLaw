// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// UTBMS code classifier — assigns task and activity codes to time entries via Haiku.

package billing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// taskCodes is the ABA/UTBMS Litigation Code Set (L-series) task list.
var taskCodes = []string{
	"L110 Fact Investigation/Development",
	"L120 Analysis/Strategy",
	"L130 Experts/Consultants",
	"L140 Document/File Management",
	"L150 Budgeting",
	"L160 Settlement/Non-Binding ADR",
	"L190 Other Case Assessment, Development and Administration",
	"L210 Pleadings",
	"L220 Preliminary Injunctions/Provisional Remedies",
	"L230 Court Mandated Conferences",
	"L240 Dispositive Motions",
	"L250 Other Written Motions and Submissions",
	"L260 Class Action Certification and Notice",
	"L310 Written Discovery",
	"L320 Document Production",
	"L330 Depositions",
	"L340 Expert Discovery",
	"L350 Discovery Motions",
	"L390 Other Discovery",
	"L410 Fact Witnesses",
	"L420 Expert Witnesses",
	"L430 Written Motions and Submissions",
	"L440 Other Trial Preparation and Support",
	"L450 Trial and Hearing Attendance",
	"L460 Post-Trial Motions and Submissions",
	"L470 Enforcement",
	"L510 Appellate Motions and Submissions",
	"L520 Appellate Briefs",
	"L530 Oral Argument",
}

// activityCodes is the UTBMS activity (A-series) list.
var activityCodes = []string{
	"A101 Plan and prepare for",
	"A102 Research",
	"A103 Draft/revise",
	"A104 Review/analyze",
	"A105 Communicate (in firm)",
	"A106 Communicate (with client)",
	"A107 Communicate (other outside counsel)",
	"A108 Communicate (other external)",
	"A109 Appear for/attend",
	"A110 Manage data/files",
	"A111 Other",
}

var validTaskCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range taskCodes {
		m[c[:4]] = true
	}
	return m
}()

var validActivityCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range activityCodes {
		m[c[:4]] = true
	}
	return m
}()

// UTBMSClassifier assigns UTBMS codes to time entries.
type UTBMSClassifier struct {
	provider providers.Provider
	haiku    string
}

// NewUTBMSClassifier creates a UTBMSClassifier.
func NewUTBMSClassifier(provider providers.Provider, haikuModel string) *UTBMSClassifier {
	return &UTBMSClassifier{provider: provider, haiku: haikuModel}
}

// Classify assigns task and activity codes to a time entry description.
func (c *UTBMSClassifier) Classify(description, eventType string) (taskCode, activityCode string) {
	const fallbackTask = "L190"
	const fallbackActivity = "A111"

	desc := sanitizeDesc(description)
	evt := sanitizeDesc(eventType)
	prompt := fmt.Sprintf(`Classify this legal time entry with exactly one UTBMS task code and one activity code.
Reply with JSON only: {"taskCode": "LXXX", "activityCode": "AXXX"}.

Description: %s
Event type: %s

Task codes:
%s

Activity codes:
%s`, desc, evt, strings.Join(taskCodes, "\n"), strings.Join(activityCodes, "\n"))

	start := time.Now()
	resp, err := c.provider.Chat(providers.ChatParams{
		Model:     c.haiku,
		MaxTokens: 64,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("UTBMSClassifier failed", "error", err)
		return fallbackTask, fallbackActivity
	}

	dms := time.Since(start).Milliseconds()
	costUSD := cost.CalcCostUSD(c.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	cost.Default.Record(cost.RecordRequest{
		Model: c.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "classification",
	})

	raw := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			raw = blk.Text
			break
		}
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &parsed); err != nil {
		return fallbackTask, fallbackActivity
	}

	tc := parsed["taskCode"]
	if !validTaskCodes[tc] {
		tc = fallbackTask
	}
	ac := parsed["activityCode"]
	if !validActivityCodes[ac] {
		ac = fallbackActivity
	}
	return tc, ac
}

func sanitizeDesc(s string) string {
	// Strip control characters except tab/newline
	var b strings.Builder
	for _, r := range s {
		if r < 0x08 || (r >= 0x0e && r <= 0x1f) || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	if len(result) > 2000 {
		return strutil.Truncate(result, 2000)
	}
	return result
}
