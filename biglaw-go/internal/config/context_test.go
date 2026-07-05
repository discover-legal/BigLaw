// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import "testing"

func TestContextTokensFor(t *testing.T) {
	c := &Config{}

	// Ollama-served models are always the small class (default num_ctx era).
	if got := c.ContextTokensFor("ollama:qwen2.5:14b"); got != SmallContextTokens {
		t.Errorf("ollama: got %d", got)
	}
	// Bare stack IDs (Qwen/GLM/Kimi cloud tiers) are 128K-class.
	if got := c.ContextTokensFor("qwen-max"); got != LargeContextTokens {
		t.Errorf("cloud stack: got %d", got)
	}

	// "local:" models follow the endpoint: local hardware is small…
	for _, u := range []string{
		"http://localhost:1234/v1",
		"http://127.0.0.1:11434/v1",
		"http://192.168.1.7:8000/v1",
		"http://host.docker.internal:1234/v1",
		"http://inference.local:8080/v1",
	} {
		c.Local.LocalInferenceURL = u
		if got := c.ContextTokensFor("local:qwen2.5:14b"); got != SmallContextTokens {
			t.Errorf("LAN endpoint %s: got %d, want small", u, got)
		}
	}
	// …but the OPENAI_MODEL shortcut and other hosted endpoints are large.
	for _, u := range []string{
		"https://api.openai.com/v1",
		"https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
	} {
		c.Local.LocalInferenceURL = u
		if got := c.ContextTokensFor("local:gpt-4.1"); got != LargeContextTokens {
			t.Errorf("cloud endpoint %s: got %d, want large", u, got)
		}
	}

	// MODEL_CONTEXT_TOKENS is the explicit override and always wins.
	c.Model.ContextTokens = 32000
	if got := c.ContextTokensFor("ollama:anything"); got != 32000 {
		t.Errorf("explicit override lost: got %d", got)
	}
}
