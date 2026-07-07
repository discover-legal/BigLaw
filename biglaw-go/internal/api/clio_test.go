// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import "testing"

func TestClioMapPracticeArea(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Corporate", "Corporate & M&A"},
		{"Mergers & Acquisitions", "Corporate & M&A"},
		{"Employment Law", "Employment & Labour"},
		{"Labor Relations", "Employment & Labour"},
		{"Intellectual Property", "Intellectual Property"},
		{"Patent Prosecution", "Intellectual Property"},
		{"Commercial Real Estate", "Real Estate"},
		{"Tax Controversy", "Tax"},
		{"Bankruptcy", "Insolvency & Restructuring"},
		{"Data Privacy", "Data Privacy & Cybersecurity"},
		{"Securities Offerings", "Capital Markets"},
		// Unknown or empty falls back to the litigation default (TS contract).
		{"Maritime", "Litigation & Dispute Resolution"},
		{"", "Litigation & Dispute Resolution"},
	}
	for _, tt := range tests {
		if got := clioMapPracticeArea(tt.in); got != tt.want {
			t.Errorf("clioMapPracticeArea(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClioDocText(t *testing.T) {
	// Text extension always passes.
	if text, ok := clioDocText([]byte("hello"), "memo.txt"); !ok || text != "hello" {
		t.Errorf("clioDocText(.txt) = %q, %v; want \"hello\", true", text, ok)
	}
	// Unknown extension but clean UTF-8 body passes.
	if _, ok := clioDocText([]byte("plain prose"), "notes"); !ok {
		t.Error("clioDocText(UTF-8, no ext) should extract")
	}
	// Binary payload (NUL bytes) is rejected.
	if _, ok := clioDocText([]byte{0x25, 0x50, 0x44, 0x46, 0x00, 0x01}, "scan.pdf"); ok {
		t.Error("clioDocText(binary) should not extract")
	}
	// Cap at 50k chars.
	big := make([]byte, 60_000)
	for i := range big {
		big[i] = 'a'
	}
	if text, ok := clioDocText(big, "big.txt"); !ok || len(text) != 50_000 {
		t.Errorf("clioDocText(60k) len = %d, %v; want 50000, true", len(text), ok)
	}
}

func TestClioStateStore(t *testing.T) {
	state, err := clioStatePut()
	if err != nil || state == "" {
		t.Fatalf("clioStatePut() = %q, %v", state, err)
	}
	if !clioStateTake(state) {
		t.Error("clioStateTake(valid) = false, want true")
	}
	// Single use: a second take of the same state must fail.
	if clioStateTake(state) {
		t.Error("clioStateTake(reused) = true, want false")
	}
	if clioStateTake("") || clioStateTake("never-issued") {
		t.Error("clioStateTake should reject empty and unknown states")
	}
}
