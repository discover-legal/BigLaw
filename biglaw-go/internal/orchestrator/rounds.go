// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/dytopo"
	"github.com/discover-legal/biglaw-go/internal/protocols"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func (o *Orchestrator) runPhase(task *types.Task, phase types.TaskPhase) error {
	audit.Default.Write(audit.WriteRequest{Event: "phase.start", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"phase": phase}})

	goal, err := o.generateRoundGoal(task, phase)
	if err != nil {
		return fmt.Errorf("generate round goal: %w", err)
	}
	o.update(task, func(t *types.Task) { t.CurrentRound++ })
	goal.Round = task.CurrentRound

	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	var lawyerTone *types.ToneProfile
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil {
			lawyerTone = p.ToneProfile
		}
	}

	var billingCtx *dytopo.AgentBillingCtx
	if o.cfg.AgentBilling.Enabled && primaryProfileID != "" {
		billingCtx = &dytopo.AgentBillingCtx{
			ResponsibleLawyerID: primaryProfileID,
			MatterNumber:        task.MatterNumber,
			ClientNumber:        task.ClientNumber,
		}
		if p := o.profiles.Get(primaryProfileID); p != nil {
			billingCtx.ResponsibleLawyerName = p.Name
		}
	}

	roundState, err := o.engine.RunRound(task, goal, lawyerTone, billingCtx)
	if err != nil {
		return fmt.Errorf("run round: %w", err)
	}
	// Build source-text map for the citation gate, keyed by every identifier a
	// model might cite: the internal UUID, the document title/filename, and the
	// normalised title. Models cite by title ("sec-referral-notice.docx"), not
	// UUID, so without the title keys mechanical verification never matches and
	// every finding is falsely flagged as unverified.
	sourceTexts := map[string]string{}
	for _, docID := range task.DocumentIDs {
		text, err := o.knowledge.GetFullText(docID)
		if err != nil || text == "" {
			continue
		}
		sourceTexts[docID] = text
		if doc := o.knowledge.GetByID(docID); doc != nil && strings.TrimSpace(doc.Title) != "" {
			sourceTexts[doc.Title] = text
			if k := protocols.NormalizeSourceKey(doc.Title); k != "" {
				sourceTexts[k] = text
			}
		}
	}

	passed, _ := o.protocols.ApplyCitationGate(roundState.Findings, sourceTexts)

	// Debate each finding.
	debated := make([]types.Finding, len(passed))
	for i, f := range passed {
		d, _ := o.protocols.RunDebate(f, task.ID)
		debated[i] = d
	}

	// Verification pipeline.
	for i := range debated {
		if result, err := o.protocols.RunVerification(debated[i], task.ID); err == nil {
			debated[i].VerificationResult = &result
		}
	}

	gates := o.protocols.IdentifyGates(task.ID, debated)
	o.annotateGatesWithClientVoice(task, gates)

	// Fold debate/verification outcomes back into the round record, then
	// publish everything in one locked write. The round is appended only
	// now: the citation gate mutates findings in place, which must not
	// happen on data already visible to marshaling readers.
	byID := map[string]types.Finding{}
	for _, f := range debated {
		byID[f.ID] = f
	}
	for i := range roundState.Findings {
		if f, ok := byID[roundState.Findings[i].ID]; ok {
			roundState.Findings[i] = f
		}
	}

	o.update(task, func(t *types.Task) {
		t.Rounds = append(t.Rounds, *roundState)
		t.Findings = append(t.Findings, debated...)
		t.PendingGates = append(t.PendingGates, gates...)
		if roundState.Starved {
			// Ride the degradation into the final task record — consumers
			// (UI, benchmark drivers) must see the run was starved.
			t.StarvedRounds = append(t.StarvedRounds, types.StarvedRound{Round: roundState.Goal.Round, Phase: phase})
		}
		t.UpdatedAt = time.Now()
	})
	emitProgress(task.ID, "round", map[string]interface{}{"round": task.CurrentRound, "phase": phase, "findings": len(debated), "gates": len(gates)})
	audit.Default.Write(audit.WriteRequest{Event: "phase.complete", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"phase": phase, "findings": len(debated), "gates": len(gates)}})
	return nil
}

// annotateGatesWithClientVoice attaches Remy's client-advocacy read to each
// gate when the matter has an advocacy brief. With a provider available the
// note is a Haiku assessment of the finding against the client's stated
// goals; if the model call fails the brief itself is attached verbatim so
// the reviewer still sees the client's voice.
func (o *Orchestrator) annotateGatesWithClientVoice(task *types.Task, gates []types.GateRequest) {
	// Admin-toggleable: some lawyers don't want client-voice hints at gates.
	if !o.cfg.ClientVoice.GateNotes {
		return
	}
	if o.clientVoice == nil || task.MatterNumber == "" || len(gates) == 0 {
		return
	}
	voice := o.clientVoice.Voice(task.MatterNumber)
	if voice == nil || len(voice.Entries) == 0 {
		return
	}
	lines := make([]string, 0, len(voice.Entries))
	for _, e := range voice.Entries {
		lines = append(lines, fmt.Sprintf("- [%s] %s", e.Category, e.Note))
	}
	brief := strings.Join(lines, "\n")

	for i := range gates {
		note := o.assessClientVoice(task, brief, gates[i].Finding)
		if note == "" {
			note = "Client's stated position (via Remy, the client advocate):\n" + brief
		}
		gates[i].ClientVoiceNote = note
	}
}

// assessClientVoice asks Haiku, speaking as the client's advocate, whether a
// gated finding aligns with or cuts against the client's stated goals.
// Returns "" on any failure — the caller falls back to the verbatim brief.
func (o *Orchestrator) assessClientVoice(task *types.Task, brief string, f types.Finding) string {
	tier := types.TierTool
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskVerification,
	})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return ""
	}
	prompt := fmt.Sprintf(`THE CLIENT'S STATED POSITION (captured during intake, in their own words):
%s

A FINDING ON THEIR MATTER IS AWAITING HUMAN REVIEW:
%s

In 2-3 sentences, tell the reviewing lawyer how this finding sits with what
the client actually wants: flag any conflict with their goals, concerns,
constraints, or preferences, or confirm alignment. Be concrete and cite the
client's own words where useful. Do not restate the finding.`, brief, f.Content)

	resp, err := prov.Chat(providers.ChatParams{
		Model:     routing.ResolveModelID(model),
		MaxTokens: 250,
		System: "You are Remy, the client's advocate. You do not work for the firm — " +
			"you speak for the client. Your notes help the reviewing lawyer serve " +
			"the client's actual interests.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		return ""
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextClientVoice, task.ID)
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}

// detectControversies is the reconciliation analyst's detection step: it reads the
// matter's gathered facts (findings, each carrying its source) and surfaces cross-
// document CONTROVERSIES — subjects where sources assert conflicting values. The
// output is graph-shaped (types.Controversy / types.Claim), the seed for the future
// TypeDB contradiction graph. Bounded; best-effort.
