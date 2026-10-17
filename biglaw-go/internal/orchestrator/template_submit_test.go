// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// A template with placeholders nobody filled (e.g. a Lavern workflow such as
// gdpr-dpia, run bare from "@BigMichael run gdpr-dpia") is refused with the
// keys it needs — never submitted with literal {{placeholders}}.
func TestSubmitFromTemplateRefusesUnfilledPlaceholders(t *testing.T) {
	ts := templates.NewStore()
	ts.Add(types.TaskTemplate{ID: "gdpr-dpia", WorkflowType: types.WorkflowReview,
		PromptTemplate: "Run a DPIA for {{description}} using {{document}}."})
	o := &Orchestrator{templates: ts}
	_, err := o.SubmitFromTemplate("gdpr-dpia", map[string]string{"description": "the CRM"}, nil, SubmitParams{})
	if err == nil || !strings.Contains(err.Error(), "needs: document") {
		t.Fatalf("want a needs: document error, got %v", err)
	}
}
