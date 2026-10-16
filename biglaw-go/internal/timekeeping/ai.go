// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package timekeeping

import (
	"os"
	"strings"
)

// Non-billable reasons shared by the orchestrator, migration and exports.
const (
	ReasonAIRateNotConfigured = "AI rate not configured"
	ReasonAIBillingDisabled   = "AI billing disabled"
	ReasonTaskDeleted         = "task deleted"
)

// AITimekeeper is the firm's AI timekeeper: AI runs are billed to it at its
// own hourly rate, never to the lawyer who submitted the task.
//
// The rate is the existing agent-billing setting (AGENT_BILLING_RATE_DEFAULT,
// gated by AGENT_BILLING_ENABLED) — one firm-wide hourly rate rather than a
// per-model table: a run's wall-clock is spent across many models at once
// (descriptors, specialists, debate, synthesis), so time cannot be apportioned
// to a model meaningfully, and per-model token cost is already tracked by the
// cost store. The per-tier AGENT_BILLING_RATE_T* values do not apply to the
// run-level clock for the same reason.
type AITimekeeper struct {
	ID      string
	Name    string
	RateUSD *float64 // nil → AI time closes non-billable with DisabledReason
	// NonBillableReason explains a nil rate.
	NonBillableReason string
}

// NewAITimekeeper builds the AI timekeeper from the agent-billing config.
// AI_TIMEKEEPER_ID (default "biglaw-ai") and AI_TIMEKEEPER_NAME (default
// "BigLaw AI") name it on invoices.
func NewAITimekeeper(enabled bool, hourlyRate float64) AITimekeeper {
	tk := AITimekeeper{
		ID:   strings.TrimSpace(os.Getenv("AI_TIMEKEEPER_ID")),
		Name: strings.TrimSpace(os.Getenv("AI_TIMEKEEPER_NAME")),
	}
	if tk.ID == "" {
		tk.ID = "biglaw-ai"
	}
	if tk.Name == "" {
		tk.Name = "BigLaw AI"
	}
	switch {
	case !enabled:
		tk.NonBillableReason = ReasonAIBillingDisabled
	case hourlyRate > 0:
		r := hourlyRate
		tk.RateUSD = &r
	default:
		tk.NonBillableReason = ReasonAIRateNotConfigured
	}
	return tk
}
