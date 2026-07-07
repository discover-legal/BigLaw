// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureBody stands up a fake OpenAI-compatible endpoint, runs one Chat call,
// and returns the decoded request body it received.
func captureBody(t *testing.T, params ChatParams) map[string]interface{} {
	t.Helper()
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "test-key")
	if _, err := p.Chat(params); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	return got
}

// TestOpenAICompatVisionSerialization verifies that an image content block is
// emitted as an OpenAI multimodal parts array carrying a base64 data URL — the
// shape Qwen-VL / GPT-4o / llama-vision expect.
func TestOpenAICompatVisionSerialization(t *testing.T) {
	body := captureBody(t, ChatParams{
		Model:     "qwen-vl-max",
		MaxTokens: 256,
		Messages: []Message{{
			Role: "user",
			Content: []ContentBlock{
				{Type: BlockText, Text: "Transcribe this."},
				ImageBlock("image/png", "QUJD"), // "ABC"
			},
		}},
	})

	msgs, ok := body["messages"].([]interface{})
	if !ok || len(msgs) == 0 {
		t.Fatalf("no messages in request body: %v", body)
	}
	msg := msgs[len(msgs)-1].(map[string]interface{})
	parts, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("vision message content is not a parts array: %#v", msg["content"])
	}
	var sawText, sawImage bool
	for _, p := range parts {
		part := p.(map[string]interface{})
		switch part["type"] {
		case "text":
			sawText = true
		case "image_url":
			sawImage = true
			url := part["image_url"].(map[string]interface{})["url"].(string)
			if !strings.HasPrefix(url, "data:image/png;base64,QUJD") {
				t.Errorf("unexpected image_url data URL: %q", url)
			}
		}
	}
	if !sawText || !sawImage {
		t.Errorf("expected both text and image_url parts, got text=%v image=%v", sawText, sawImage)
	}
}

// TestOpenAICompatTextStaysString verifies a plain text-only message is sent as
// a string (not a parts array) so text-only models that reject the array form
// still work.
func TestOpenAICompatTextStaysString(t *testing.T) {
	body := captureBody(t, ChatParams{
		Model:     "qwen-plus",
		MaxTokens: 64,
		Messages:  []Message{{Role: "user", Content: "hello"}},
	})
	msgs := body["messages"].([]interface{})
	msg := msgs[len(msgs)-1].(map[string]interface{})
	if _, isString := msg["content"].(string); !isString {
		t.Errorf("text-only content should serialize as a string, got %T", msg["content"])
	}
}
