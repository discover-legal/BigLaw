// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package modules

import "testing"

func TestDefaultsAndOverrides(t *testing.T) {
	r := NewRegistry()

	if on := r.Register("billing", "Billing", true, "default on"); !on {
		t.Fatal("default-on module resolved off")
	}
	if on := r.Register("intake", "Intake", false, "requires INTAKE_HMAC_SECRET"); on {
		t.Fatal("default-off module resolved on")
	}

	t.Setenv("BIGLAW_MODULE_BILLING", "off")
	t.Setenv("BIGLAW_MODULE_INTAKE", "on")
	t.Setenv("BIGLAW_MODULE_MONITOR_DOCKETS", "0")
	if on := r.Register("billing", "Billing", true, "default on"); on {
		t.Fatal("env off override ignored")
	}
	if on := r.Register("intake", "Intake", false, "requires INTAKE_HMAC_SECRET"); !on {
		t.Fatal("env on override ignored")
	}
	// Dashes map to underscores in the env key.
	if on := r.Register("monitor-dockets", "Dockets", true, "MONITOR_DOCKETS"); on {
		t.Fatal("dashed module env override ignored")
	}

	if !r.Enabled("intake") || r.Enabled("billing") {
		t.Fatal("Enabled() disagrees with registration")
	}
	// Unregistered names are enabled (core fail-open).
	if !r.Enabled("orchestrator") {
		t.Fatal("unregistered module must default enabled")
	}

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List() = %d entries, want 3", len(list))
	}
	if list[0].Name != "billing" || list[0].Reason != "BIGLAW_MODULE_BILLING=off" {
		t.Fatalf("reason not recorded: %+v", list[0])
	}
}
