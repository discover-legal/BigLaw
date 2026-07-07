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

var taskCodes = []string{
	"L110 Fact Investigation/Development",
	"L120 Analysis/Strategy",
	"L130 Experts/Consultants",
	"L140 Document/File Management",
	"L150 Budgeting",
	"L160 Settlement/Non-Binding ADR",
	"L190 Other Case Assessment",
	"L210 Pleadings",
	"L220 Preliminary Injunctions/TROs",
	"L230 Court Mandated Conferences",
	"L240 Dispositive Motions",
	"L250 Other Written Motions/Submissions",
	"L260 Class Action Certification",
	"L310 Written Discovery",
	"L320 Document Production",
	"L330 Depositions",
	"L340 Expert Discovery",
	"L350 Discovery Motions",
	"L390 Other Discovery",
	"L410 Fact Witnesses",
	"L420 Expert Witnesses",
	"L430 Trial Preparation",
	"L440 Trial",
	"L450 Post-Trial Motions",
	"L460 Appellate Proceedings",
	"L510 Project Management",
	"L520 Litigation Counseling",
	"L530 Contract/Agreement Drafting",
	"L540 Due Diligence",
	"L550 Regulatory Compliance",
}

var activityCodes = []string{
	"A101 Plan and Prepare",
	"A102 Research",
	"A103 Draft/Revise",
	"A104 Review/Analyze",
	"A105 Communicate (in firm)",
	"A106 Communicate (with client)",
	"A107 Communicate (other outside)",
	"A108 Appear for/Attend",
	"A109 Obtain/compile/index/organize",
	"A110 Other",
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
	const fallbackActivity = "A110"

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
