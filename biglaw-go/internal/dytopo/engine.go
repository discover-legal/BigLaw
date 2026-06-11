// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// DyTopo Engine — Dynamic Topology Routing for Multi-Agent Reasoning.
// Based on arXiv:2602.06039 (Lu et al., 2026).

package dytopo

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type AgentBillingCtx struct {
	ResponsibleLawyerID   string
	ResponsibleLawyerName string
	MatterNumber          string
	ClientNumber          string
}

type Engine struct {
	registry   *agents.Registry
	memory     *memory.InterRoundStore
	memAdapter *memory.Adapter
	knowledge  agents.KnowledgeStore
	pinned     []types.AgentDefinition
	cfg        *config.Config
	provReg    *providers.Registry
	costs      *cost.Store
	embedC     *embeddings.Client
	tools      agents.ToolRegistry
	learning   *learning.Engine
}

type Options struct {
	Registry  *agents.Registry
	Memory    *memory.InterRoundStore
	Knowledge agents.KnowledgeStore
	Pinned    []types.AgentDefinition
	Tools     agents.ToolRegistry
	Learning  *learning.Engine
}

func New(cfg *config.Config, prov *providers.Registry, costs *cost.Store, embedC *embeddings.Client, opts Options) *Engine {
	return &Engine{
		registry:   opts.Registry,
		memory:     opts.Memory,
		memAdapter: memory.NewAdapter(opts.Memory),
		knowledge:  opts.Knowledge,
		pinned:     opts.Pinned,
		tools:      opts.Tools,
		cfg:        cfg,
		provReg:    prov,
		costs:      costs,
		embedC:     embedC,
		learning:   opts.Learning,
	}
}

// RunRound executes one round of DyTopo orchestration.
func (e *Engine) RunRound(task *types.Task, goal types.RoundGoal, lawyerTone *types.ToneProfile, billing *AgentBillingCtx) (*types.RoundState, error) {
	roundID := uuid.New().String()
	intra := memory.NewIntraRound(roundID)

	audit.Default.Write(audit.WriteRequest{
		Event:   "round.start",
		ActorID: audit.ActorSystem,
		TaskID:  task.ID,
		Data:    map[string]interface{}{"round": goal.Round, "phase": goal.Phase, "roundId": roundID},
	})

	// Step 1: Recruit agents.
	recruited, err := e.recruitAgents(goal, task)
	if err != nil {
		return nil, fmt.Errorf("recruit agents: %w", err)
	}
	agentMap := map[string]types.AgentDefinition{}
	for _, a := range e.pinned {
		agentMap[a.ID] = a
	}
	for _, a := range recruited {
		agentMap[a.ID] = a
	}
	var activeDefs []types.AgentDefinition
	for _, a := range agentMap {
		if JurisdictionMatch(a, task.Jurisdiction) {
			activeDefs = append(activeDefs, a)
		}
		if len(activeDefs) >= e.cfg.DyTopo.MaxAgentsPerRound {
			break
		}
	}
	activeAgents := make([]*agents.Agent, len(activeDefs))
	for i, def := range activeDefs {
		activeAgents[i] = agents.NewAgent(def, e.cfg, e.provReg, e.costs)
	}

	// Step 2: Fetch inter-round memory per agent.
	agentMemories, err := e.fetchAgentMemories(activeDefs, task, goal)
	if err != nil {
		return nil, err
	}

	// Step 3: Need/Offer descriptors (parallel).
	type noResult struct {
		need  types.NeedDescriptor
		offer types.OfferDescriptor
	}
	noResults := make([]noResult, len(activeAgents))
	var g1 errgroup.Group
	for i, ag := range activeAgents {
		i, ag := i, ag
		g1.Go(func() error {
			ctx := agents.AgentContext{
				RoundGoal:       goal,
				MemoryEntries:   agentMemories[ag.Def.ID],
				TaskDescription: task.Description,
				TaskID:          task.ID,
			}
			need, offer, err := ag.GenerateNeedOffer(ctx)
			if err != nil {
				need = types.NeedDescriptor{AgentID: ag.Def.ID, Text: "No specific need."}
				offer = types.OfferDescriptor{AgentID: ag.Def.ID, Text: "General expertise."}
			}
			noResults[i] = noResult{need, offer}
			return nil
		})
	}
	g1.Wait()

	needs := make([]types.NeedDescriptor, len(noResults))
	offers := make([]types.OfferDescriptor, len(noResults))
	for i, r := range noResults {
		needs[i] = r.need
		offers[i] = r.offer
	}

	// Step 4: Build sparse comm graph.
	edges, err := e.buildCommGraph(needs, offers, activeDefs)
	if err != nil {
		return nil, err
	}

	// Step 5: Route messages.
	msgs := e.routeMessages(edges, offers, goal.Round)
	for _, msg := range msgs {
		intra.RecordMessage(msg.To, msg)
	}

	// Step 6: Process agents (parallel).
	findingsCh := make([][]types.Finding, len(activeAgents))
	roundTimeout := time.Duration(e.cfg.Agents.RoundTimeoutMs) * time.Millisecond
	var g2 errgroup.Group
	for i, ag := range activeAgents {
		i, ag := i, ag
		g2.Go(func() error {
			ctx := agents.AgentContext{
				RoundGoal:          goal,
				IncomingMessages:   intra.GetMessagesFor(ag.Def.ID),
				MemoryEntries:      agentMemories[ag.Def.ID],
				TaskDescription:    task.Description,
				TaskID:             task.ID,
				ToolRegistry:       e.tools,
				KnowledgeStore:     e.knowledge,
				MemoryStore:        e.memAdapter,
				OwnerID:            task.CreatedByProfileID,
				AssignedLawyerTone: lawyerTone,
			}
			if billing != nil {
				ctx.ResponsibleLawyerID = billing.ResponsibleLawyerID
				ctx.ResponsibleLawyerName = billing.ResponsibleLawyerName
				ctx.MatterNumber = billing.MatterNumber
				ctx.ClientNumber = billing.ClientNumber
			}
			// Per-agent wall-clock cap (AGENT_ROUND_TIMEOUT_MS) so one hung
			// provider/tool call can't stall the whole round. Process takes
			// an AgentContext, not a context.Context, so the deadline cannot
			// be propagated into the call; the call is raced against it
			// instead (mirroring the TS Promise.race) and its findings are
			// discarded on timeout while the abandoned goroutine drains into
			// the buffered channel.
			tctx, cancel := context.WithTimeout(context.Background(), roundTimeout)
			defer cancel()
			type procResult struct {
				findings []types.Finding
				err      error
			}
			done := make(chan procResult, 1)
			go func() {
				findings, err := ag.Process(ctx)
				done <- procResult{findings: findings, err: err}
			}()
			select {
			case r := <-done:
				if r.err != nil {
					return nil
				}
				findingsCh[i] = r.findings
			case <-tctx.Done():
				slog.Warn("agent exceeded round timeout; recording no findings for it this round",
					"agentId", ag.Def.ID, "round", goal.Round, "phase", goal.Phase,
					"timeoutMs", e.cfg.Agents.RoundTimeoutMs)
			}
			return nil
		})
	}
	g2.Wait()

	// Non-nil even when empty: these slices are part of the REST/MCP JSON
	// contract (the UI iterates them), and nil marshals to null.
	allFindings := []types.Finding{}
	for _, findings := range findingsCh {
		for _, f := range findings {
			f.Round = goal.Round
			intra.RecordFinding(f.AgentID, f)
			intra.AddSharedContext(fmt.Sprintf("[%s] %s", f.AgentName, truncate(f.Content, 200)))
			allFindings = append(allFindings, f)
		}
	}

	// Step 7: Persist round memory.
	e.persistRoundMemory(task, goal, allFindings, intra)

	now := time.Now()
	state := &types.RoundState{
		RoundID:        roundID,
		Goal:           goal,
		ActiveAgentIDs: agentIDs(activeDefs),
		Edges:          edges,
		Messages:       msgs,
		Findings:       allFindings,
		Status:         "complete",
		StartedAt:      now,
		CompletedAt:    &now,
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "round.complete",
		ActorID: audit.ActorSystem,
		TaskID:  task.ID,
		Data: map[string]interface{}{
			"round":    goal.Round,
			"phase":    goal.Phase,
			"roundId":  roundID,
			"findings": len(allFindings),
			"edges":    len(edges),
		},
	})

	return state, nil
}

// ─── Private helpers ──────────────────────────────────────────────────────────

var phaseToTier = map[types.TaskPhase]*types.AgentTier{
	types.PhaseIntake:       tierPtr(types.TierManager),
	types.PhaseResearch:     tierPtr(types.TierSpecialist),
	types.PhaseAnalysis:     tierPtr(types.TierSpecialist),
	types.PhaseDrafting:     tierPtr(types.TierSpecialist),
	types.PhaseReview:       tierPtr(types.TierSpecialist),
	types.PhaseVerification: tierPtr(types.TierSpecialist),
	types.PhaseDelivery:     tierPtr(types.TierManager),
}

func tierPtr(t types.AgentTier) *types.AgentTier { return &t }

func (e *Engine) recruitAgents(goal types.RoundGoal, task *types.Task) ([]types.AgentDefinition, error) {
	topK := e.cfg.DyTopo.MaxAgentsPerRound - 1
	opts := agents.SearchOpts{Tier: phaseToTier[goal.Phase], TopK: topK}

	var positive, negative []string
	for _, round := range task.Rounds {
		for _, f := range round.Findings {
			if f.Challenged {
				negative = append(negative, f.AgentID)
			} else {
				positive = append(positive, f.AgentID)
			}
		}
	}
	positive = unique(positive)[:min(8, len(unique(positive)))]
	negative = unique(negative)[:min(4, len(unique(negative)))]

	var candidates []types.AgentDefinition
	var err error
	if len(positive) > 0 {
		candidates, err = e.registry.Recommend(goal.Description, positive, negative, opts)
	} else {
		candidates, err = e.registry.Search(goal.Description, opts)
	}
	if err != nil {
		return nil, err
	}

	// Q-learning rerank.
	if e.learning != nil {
		ids := make([]string, len(candidates))
		for i, c := range candidates {
			ids[i] = c.ID
		}
		rankedIDs := e.learning.RankCandidates(goal.Phase, task.Jurisdiction, task.WorkflowType, ids)
		byID := map[string]types.AgentDefinition{}
		for _, c := range candidates {
			byID[c.ID] = c
		}
		ranked := make([]types.AgentDefinition, 0, len(rankedIDs))
		for _, id := range rankedIDs {
			if def, ok := byID[id]; ok {
				ranked = append(ranked, def)
			}
		}
		return ranked, nil
	}
	return candidates, nil
}

func (e *Engine) fetchAgentMemories(defs []types.AgentDefinition, task *types.Task, goal types.RoundGoal) (map[string][]types.MemoryEntry, error) {
	result := map[string][]types.MemoryEntry{}
	for _, def := range defs {
		agentMem, _ := e.memory.Query(goal.Description, memory.QueryOpts{
			TaskID:      task.ID,
			AgentID:     def.ID,
			BeforeRound: goal.Round,
			TopK:        6,
		})
		taskMem, _ := e.memory.Query(goal.Description, memory.QueryOpts{
			TaskID:      task.ID,
			BeforeRound: goal.Round,
			TopK:        4,
		})
		result[def.ID] = append(agentMem, taskMem...)
	}
	return result, nil
}

func (e *Engine) buildCommGraph(needs []types.NeedDescriptor, offers []types.OfferDescriptor, defs []types.AgentDefinition) ([]types.CommunicationEdge, error) {
	allTexts := make([]string, 0, len(needs)+len(offers))
	for _, n := range needs {
		allTexts = append(allTexts, n.Text)
	}
	for _, o := range offers {
		allTexts = append(allTexts, o.Text)
	}

	results, err := e.embedC.EmbedBatch(allTexts)
	if err != nil {
		return nil, fmt.Errorf("embed descriptors: %w", err)
	}

	needEmbs := make([][]float32, len(needs))
	offerEmbs := make([][]float32, len(offers))
	for i := range needs {
		needEmbs[i] = results[i].Embedding
	}
	for i := range offers {
		offerEmbs[i] = results[len(needs)+i].Embedding
	}

	threshold := e.cfg.DyTopo.SimilarityThreshold
	// Non-nil even when no pair clears the threshold — see RunRound: this
	// slice is serialized to the UI, and nil marshals to null.
	edges := []types.CommunicationEdge{}
	for i, need := range needs {
		for j, offer := range offers {
			if need.AgentID == offer.AgentID {
				continue
			}
			sim := embeddings.CosineSimilarity(needEmbs[i], offerEmbs[j])
			if sim >= threshold {
				edges = append(edges, types.CommunicationEdge{
					From:       offer.AgentID,
					To:         need.AgentID,
					Similarity: sim,
					OfferText:  offer.Text,
				})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Similarity > edges[j].Similarity })
	return edges, nil
}

func (e *Engine) routeMessages(edges []types.CommunicationEdge, offers []types.OfferDescriptor, round int) []types.AgentMessage {
	offerMap := map[string]string{}
	for _, o := range offers {
		offerMap[o.AgentID] = o.Text
	}
	msgs := make([]types.AgentMessage, len(edges))
	for i, edge := range edges {
		text := offerMap[edge.From]
		if len(text) > 500 {
			text = strutil.Truncate(text, 500)
		}
		msgs[i] = types.AgentMessage{
			ID:        uuid.New().String(),
			From:      edge.From,
			To:        edge.To,
			Content:   fmt.Sprintf("[Offer from %s] %s", edge.From, text),
			Round:     round,
			Timestamp: time.Now(),
		}
	}
	return msgs
}

func (e *Engine) persistRoundMemory(task *types.Task, goal types.RoundGoal, findings []types.Finding, intra *memory.IntraRoundStore) {
	for _, f := range findings {
		e.memory.WriteFindingMemory(memory.WriteFindingOpts{
			TaskID:  task.ID,
			Round:   goal.Round,
			Phase:   goal.Phase,
			AgentID: f.AgentID,
			Finding: f,
		})
	}

	summary := fmt.Sprintf("Round %d (%s): No findings produced.", goal.Round, goal.Phase)
	if len(findings) > 0 {
		// Call Haiku for a rollup summary.
		bullets := ""
		max := 12
		if len(findings) < max {
			max = len(findings)
		}
		for _, f := range findings[:max] {
			c := f.Content
			if len(c) > 150 {
				c = strutil.Truncate(c, 150)
			}
			bullets += fmt.Sprintf("- [%s] %s\n", f.AgentName, c)
		}

		tier := types.TierTool
		model := routing.SelectModel(e.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskDescriptor})
		prov, err := e.provReg.Get(model)
		if err == nil {
			resp, err := prov.Chat(providers.ChatParams{
				Model:     routing.ResolveModelID(model),
				MaxTokens: 300,
				System:    "You are a legal analysis synthesizer. Produce a concise inter-round memory digest.",
				Messages: []providers.Message{{
					Role:    "user",
					Content: fmt.Sprintf("Round %d (%s) findings:\n%s\n\nSummarise the key legal conclusions in 2-3 sentences.", goal.Round, goal.Phase, bullets),
				}},
			})
			if err == nil {
				for _, b := range resp.Content {
					if b.Type == providers.BlockText && b.Text != "" {
						summary = b.Text
						break
					}
				}
			}
		}
	}

	e.memory.WriteRoundSummary(memory.WriteRoundSummaryOpts{
		TaskID:       task.ID,
		Round:        goal.Round,
		Phase:        goal.Phase,
		Summary:      summary,
		FindingCount: len(findings),
	})
}

// ─── Utility ──────────────────────────────────────────────────────────────────

func agentIDs(defs []types.AgentDefinition) []string {
	ids := make([]string, len(defs))
	for i, d := range defs {
		ids[i] = d.ID
	}
	return ids
}

func unique(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, max int) string {
	return strutil.Truncate(s, max)
}
