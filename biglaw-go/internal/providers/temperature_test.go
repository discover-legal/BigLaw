// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

// Confirms Temperature serializes into the OpenAI-compatible request body (and
// is omitted when nil) — the plumbing behind the LLM_TEMPERATURE knob.
func TestTemperatureSerializes(t *testing.T) {
	temp := 0.2
	body, _ := json.Marshal(openAIChatRequest{Model: "qwen2.5:14b", Temperature: &temp})
	if !strings.Contains(string(body), `"temperature":0.2`) {
		t.Errorf("temperature not serialized: %s", body)
	}

	body2, _ := json.Marshal(openAIChatRequest{Model: "qwen2.5:14b"})
	if strings.Contains(string(body2), "temperature") {
		t.Errorf("nil temperature should be omitted: %s", body2)
	}
}
