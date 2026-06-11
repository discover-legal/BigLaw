// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Citation gate, debate protocol, verification pipeline, and human gate.

package protocols

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
	"golang.org/x/sync/errgroup"
)

type Runner struct {
	cfg     *config.Config
	provReg *providers.Registry
	costs   *cost.Store
}

func New(cfg *config.Config, provReg *providers.Registry, costs *cost.Store) *Runner {
	return &Runner{cfg: cfg, provReg: provReg, costs: costs}
}

// ─── 1. Citation gate ─────────────────────────────────────────────────────────

func (r *Runner) ApplyCitationGate(findings []types.Finding, sourceTexts map[string]string) (passed, rejected []types.Finding) {
	if !r.cfg.Debate.CitationRequired {
		return findings, nil
	}
	for i := range findings {
		f := &findings[i]
		if len(f.Citations) == 0 {
			rejected = append(rejected, *f)
			continue
		}
		for j := range f.Citations {
			src := sourceTexts[f.Citations[j].Source]
			if src != "" {
				f.Citations[j].MechanicallyVerified = strings.Contains(src, f.Citations[j].Quote)
			}
		}
		passed = append(passed, *f)
	}
	return
}

// ─── 2. Debate protocol ───────────────────────────────────────────────────────

const challengerSystem = `You are the Adversarial Challenger in a legal AI debate protocol.
Your job: challenge the finding below if it is incorrect, overstated, or unsupported.
Your challenge MUST include a verbatim citation from a specific source.
If you believe the finding is correct and well-supported, output: NO_CHALLENGE
Otherwise output:
CHALLENGE:
Content: <your challenge>
Citation: SOURCE=<source> | QUOTE=<verbatim text>
Strength: <1-5>
END_CHALLENGE`

const resolverSystem = `You are the Debate Resolver in a legal AI debate protocol.
You receive a finding and a challenge to that finding.
Weigh both. Cite specific reasons for your resolution.
Output:
RESOLUTION: <UPHELD | MODIFIED | OVERTURNED>
REASONING: <one paragraph explaining your resolution, citing both sides>
MODIFIED_CONTENT: <if MODIFIED, the corrected finding content; otherwise leave blank>`

func (r *Runner) RunDebate(finding types.Finding, taskID string) (types.Finding, error) {
	if !r.cfg.Debate.AdversarialEnabled {
		return finding, nil
	}

	model := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskDebate})
	audit.Default.Write(audit.WriteRequest{
		Event:   "debate.start",
		ActorID: audit.ActorSystem,
		TaskID:  taskID,
		Data:    map[string]interface{}{"findingId": finding.ID, "model": model},
	})

	snippet := finding.Content
	if len(snippet) > 20_000 {
		snippet = snippet[:20_000]
	}
	citLines := make([]string, 0, len(finding.Citations))
	for _, c := range finding.Citations {
		src := c.Source
		if len(src) > 200 {
			src = src[:200]
		}
		q := c.Quote
		if len(q) > 500 {
			q = q[:500]
		}
		citLines = append(citLines, fmt.Sprintf("SOURCE=%s | QUOTE=%s", src, q))
	}

	challengeText, err := r.callModel(challengerSystem,
		fmt.Sprintf("FINDING:\n%s\n\nCITATIONS:\n%s", snippet, strings.Join(citLines, "\n")),
		600, model, taskID, cost.ContextDebate)
	if err != nil {
		// Fail-open by design: an unavailable challenger must not block the
		// round, but it must be visible in the logs.
		slog.Warn("debate: challenger call failed, finding passes unchallenged",
			"findingId", finding.ID, "taskId", taskID, "err", err)
		return finding, nil
	}

	if strings.Contains(challengeText, "NO_CHALLENGE") {
		audit.Default.Write(audit.WriteRequest{
			Event:   "debate.resolved",
			ActorID: audit.ActorSystem,
			TaskID:  taskID,
			Data:    map[string]interface{}{"findingId": finding.ID, "verdict": "NO_CHALLENGE"},
		})
		return finding, nil
	}

	challenge := parseChallenge(challengeText, "adversarial-challenger")
	finding.Challenged = true
	finding.Challenge = &challenge

	resolutionText, err := r.callModel(resolverSystem,
		fmt.Sprintf("FINDING:\n%s\n\nCHALLENGE:\n%s", snippet, challenge.Content),
		800, model, taskID, cost.ContextDebate)
	if err != nil {
		// Challenged but unresolved — IdentifyGates routes this to a human.
		slog.Warn("debate: resolver call failed, finding left challenged/unresolved",
			"findingId", finding.ID, "taskId", taskID, "err", err)
		return finding, nil
	}

	verdict, reasoning, modified, parseErr := parseResolution(resolutionText)
	challenge.Resolution = reasoning
	now := time.Now()
	challenge.ResolvedAt = &now
	finding.Challenge = &challenge

	if parseErr {
		// Malformed resolver output — leave the finding challenged/unresolved
		// so IdentifyGates routes it to a human instead of silently upholding.
		slog.Warn("debate: resolution parse error, finding left unresolved",
			"findingId", finding.ID, "taskId", taskID)
		finding.Resolved = false
	} else {
		switch verdict {
		case "MODIFIED":
			if modified != "" {
				// The resolver's rewrite re-enters prompts downstream
				// (synthesis, verification) — neutralise protocol markers.
				finding.Content = adapters.SanitizePromptContent(modified)
			}
		case "OVERTURNED":
			if finding.Confidence > 0.3 {
				finding.Confidence -= 0.3
			} else {
				finding.Confidence = 0
			}
		}
		finding.Resolved = true
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "debate.resolved",
		ActorID: audit.ActorSystem,
		TaskID:  taskID,
		Data:    map[string]interface{}{"findingId": finding.ID, "verdict": verdict},
	})
	return finding, nil
}

// ─── 3. Verification pipeline ─────────────────────────────────────────────────

var verificationChecks = []string{
	"Context: Is the finding grounded in the stated context and not taken out of scope?",
	"Accuracy: Are all legal propositions correctly stated per the cited authority?",
	"Completeness: Does the finding address all aspects of the question it purports to answer?",
	"Clarity: Is the finding expressed clearly and unambiguously?",
	"Structure: Is the finding logically structured?",
	"Citations: Are all citations present, specific, and relevant?",
	"Risk: Does the finding appropriately flag relevant risks or uncertainties?",
	"Jurisdiction: Is the jurisdictional scope of the finding explicitly stated?",
	"Timeliness: Are the sources current? Are any materials superseded?",
	"Proportionality: Is the conclusion proportionate to the evidence cited?",
}

func (r *Runner) RunVerification(finding types.Finding, taskID string) (types.VerificationResult, error) {
	passes := r.cfg.Debate.VerificationPasses
	if passes > len(verificationChecks) {
		passes = len(verificationChecks)
	}
	checks := verificationChecks[:passes]

	model := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
	audit.Default.Write(audit.WriteRequest{
		Event:   "verification.start",
		ActorID: audit.ActorSystem,
		TaskID:  taskID,
		Data:    map[string]interface{}{"findingId": finding.ID, "checks": len(checks)},
	})

	snippet := finding.Content
	if len(snippet) > 20_000 {
		snippet = snippet[:20_000]
	}
	citLines := make([]string, 0, len(finding.Citations))
	for _, c := range finding.Citations {
		q := c.Quote
		if len(q) > 500 {
			q = q[:500]
		}
		citLines = append(citLines, fmt.Sprintf("%s: \"%s\"", c.Source, q))
	}
	userMsg := fmt.Sprintf("FINDING:\n%s\n\nCITATIONS:\n%s", snippet, strings.Join(citLines, "\n"))

	verChecks := make([]types.VerificationCheck, len(checks))
	g, _ := errgroup.WithContext(nil)
	for i, checkDesc := range checks {
		i, checkDesc := i, checkDesc
		g.Go(func() error {
			resp, err := r.callModel(
				fmt.Sprintf("You are a legal verification specialist. Assess the following finding against this criterion: %s\nRespond with: PASS or FAIL followed by a one-line note.", checkDesc),
				userMsg, 150, model, taskID, cost.ContextVerify)
			if err != nil {
				slog.Warn("verification: check call failed, recorded as FAIL",
					"check", strings.Split(checkDesc, ":")[0], "findingId", finding.ID, "taskId", taskID, "err", err)
				verChecks[i] = types.VerificationCheck{Name: strings.Split(checkDesc, ":")[0], Passed: false, Notes: "verification call failed: " + err.Error()}
				return nil
			}
			passed := strings.Contains(strings.ToUpper(resp), "PASS")
			notes := regexp.MustCompile(`(?i)^(PASS|FAIL)\s*`).ReplaceAllString(resp, "")
			verChecks[i] = types.VerificationCheck{
				Name:   strings.Split(checkDesc, ":")[0],
				Passed: passed,
				Notes:  strings.TrimSpace(notes),
			}
			return nil
		})
	}
	g.Wait()

	allPassed := true
	for _, c := range verChecks {
		if !c.Passed {
			allPassed = false
			break
		}
	}

	result := types.VerificationResult{
		FindingID:   finding.ID,
		Checks:      verChecks,
		Passed:      allPassed,
		CompletedAt: time.Now(),
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "verification.complete",
		ActorID: audit.ActorSystem,
		TaskID:  taskID,
		Data:    map[string]interface{}{"findingId": finding.ID, "passed": allPassed},
	})
	return result, nil
}

// ─── 4. Human gate ────────────────────────────────────────────────────────────

func (r *Runner) IdentifyGates(taskID string, findings []types.Finding) []types.GateRequest {
	threshold := r.cfg.Debate.GateConfidenceThreshold
	var gates []types.GateRequest
	for _, f := range findings {
		needsGate := f.Confidence < threshold ||
			(f.Challenged && !f.Resolved) ||
			(f.VerificationResult != nil && !f.VerificationResult.Passed)
		if needsGate {
			gates = append(gates, types.GateRequest{
				ID:        uuid.New().String(),
				TaskID:    taskID,
				FindingID: f.ID,
				Finding:   f,
				Status:    "pending",
				CreatedAt: time.Now(),
			})
		}
	}
	return gates
}

// ─── callModel helper ─────────────────────────────────────────────────────────

func (r *Runner) callModel(system, user string, maxTokens int, model, taskID string, cctx cost.CostContext) (string, error) {
	if model == "" {
		model = r.cfg.Anthropic.Model
	}
	prov, err := r.provReg.Get(model)
	if err != nil {
		return "", err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   maxTokens,
		System:      system,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
	})
	if err != nil {
		return "", err
	}

	isLocal := routing.IsOllamaModel(model) || routing.IsLocalModel(model)
	bare := routing.ResolveModelID(model)
	var costUSD *float64
	var wh *float64
	var watts *int
	if !isLocal {
		cw, cr := 0, 0
		if resp.Usage.CacheWriteTokens != nil {
			cw = *resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens != nil {
			cr = *resp.Usage.CacheReadTokens
		}
		costUSD = cost.CalcCostUSD(bare, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	} else {
		w := cost.CalcWattHours(r.cfg.Local.InferenceWatts, resp.DurationMs)
		wh = &w
		watts = &r.cfg.Local.InferenceWatts
	}
	provider := "anthropic"
	if routing.IsOllamaModel(model) {
		provider = "ollama"
	} else if routing.IsLocalModel(model) {
		provider = "local"
	}
	r.costs.Record(cost.RecordRequest{
		Model:          bare,
		Provider:       provider,
		InputTokens:    resp.Usage.InputTokens,
		OutputTokens:   resp.Usage.OutputTokens,
		CostUSD:        costUSD,
		EstimatedWh:    wh,
		EstimatedWatts: watts,
		DurationMs:     resp.DurationMs,
		Context:        cctx,
		TaskID:         taskID,
	})

	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return b.Text, nil
		}
	}
	return "", fmt.Errorf("no text in response")
}

// ─── Parsers ──────────────────────────────────────────────────────────────────

func parseChallenge(text, challengerID string) types.Challenge {
	re := regexp.MustCompile(`(?si)Content:\s*(.*?)(?:Citation:|Strength:|END_CHALLENGE)`)
	cm := re.FindStringSubmatch(text)
	content := text
	if len(cm) > 1 {
		content = strings.TrimSpace(cm[1])
	}
	citRe := regexp.MustCompile(`(?i)Citation:\s*SOURCE=(.+?)\s*\|\s*QUOTE=(.+?)(?:\n|END_CHALLENGE|$)`)
	var citations []types.Citation
	for _, m := range citRe.FindAllStringSubmatch(text, 5) {
		citations = append(citations, types.Citation{
			Source: strings.TrimSpace(m[1]),
			Quote:  strings.TrimSpace(m[2]),
		})
	}
	return types.Challenge{
		ChallengerID:   challengerID,
		ChallengerName: "Adversarial Challenger",
		Content:        content,
		Citations:      citations,
	}
}

func parseResolution(text string) (verdict, reasoning, modified string, parseErr bool) {
	if m := regexp.MustCompile(`(?i)RESOLUTION:\s*(UPHELD|MODIFIED|OVERTURNED)`).FindStringSubmatch(text); len(m) > 1 {
		verdict = strings.ToUpper(m[1])
	} else {
		slog.Warn("parseResolution: no RESOLUTION verdict found in resolver output")
		return "UPHELD", "Parse error - no verdict found", "", true
	}
	if m := regexp.MustCompile(`(?si)REASONING:\s*(.*?)(?:MODIFIED_CONTENT:|$)`).FindStringSubmatch(text); len(m) > 1 {
		reasoning = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`(?si)MODIFIED_CONTENT:\s*(.*)`).FindStringSubmatch(text); len(m) > 1 {
		modified = strings.TrimSpace(m[1])
	}
	return
}
