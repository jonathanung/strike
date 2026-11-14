package config

import (
	"strings"
	"testing"
)

func readGlobalSession(t *testing.T) SessionConfig {
	t.Helper()
	cfg, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Session
}

func TestSetGlobalGuardrailDialsRoundTripsEachDial(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetGlobalTheme("dracula"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		dials GuardrailDials
		check func(SessionConfig) bool
	}{
		{"turn timeout seconds", GuardrailDials{TurnTimeout: "600"}, func(s SessionConfig) bool { return s.TurnTimeoutS == 600 }},
		{"turn timeout duration", GuardrailDials{TurnTimeout: "1h"}, func(s SessionConfig) bool { return s.TurnTimeoutS == 3600 }},
		{"turn timeout off", GuardrailDials{TurnTimeout: "off"}, func(s SessionConfig) bool { return s.TurnTimeoutS == -1 }},
		{"turn timeout default", GuardrailDials{TurnTimeout: "default"}, func(s SessionConfig) bool { return s.TurnTimeoutS == 0 }},
		{"session cost", GuardrailDials{MaxSessionCostUSD: "2.50"}, func(s SessionConfig) bool { return s.MaxSessionCostUSD == 2.5 }},
		{"session cost off", GuardrailDials{MaxSessionCostUSD: "off"}, func(s SessionConfig) bool { return s.MaxSessionCostUSD == 0 }},
		{"turn tokens", GuardrailDials{MaxTurnTokens: "200000"}, func(s SessionConfig) bool { return s.MaxTurnTokens == 200000 }},
		{"agent budget", GuardrailDials{
			AgentBudgetMaxWallClockS: "300", AgentBudgetMaxTokens: "50000", AgentBudgetMaxCostUSD: "0.75",
			AgentBudgetMaxToolCalls: "40", AgentBudgetMaxDangerousTools: "3", AgentBudgetStallAfterS: "90", AgentBudgetLoopDetectN: "5",
		}, func(s SessionConfig) bool {
			return s.AgentBudget == AgentBudgetConfig{MaxWallClockS: 300, MaxTokens: 50000, MaxCostUSD: 0.75, MaxToolCalls: 40, MaxDangerousTools: 3, StallAfterS: 90, LoopDetectN: 5}
		}},
		{"agent budget partial clear", GuardrailDials{AgentBudgetMaxTokens: "off"}, func(s SessionConfig) bool {
			return s.AgentBudget.MaxTokens == 0 && s.AgentBudget.MaxWallClockS == 300
		}},
		{"delegation policy", GuardrailDials{
			DelegationMode: "advise", DelegationTinyPromptRunes: "500", DelegationMaxPathsLocal: "off", DelegationMaxLiveChildren: "4",
		}, func(s SessionConfig) bool {
			return s.DelegationPolicy == DelegationPolicyConfig{Mode: "advise", TinyPromptRunes: 500, MaxPathsLocal: -1, MaxLiveChildren: 4}
		}},
		{"delegation mode default", GuardrailDials{DelegationMode: "default"}, func(s SessionConfig) bool { return s.DelegationPolicy.Mode == "" }},
		{"handoff off", GuardrailDials{ChildHandoffRequired: "off"}, func(s SessionConfig) bool {
			return s.ChildHandoffRequired != nil && !*s.ChildHandoffRequired
		}},
		{"handoff default", GuardrailDials{ChildHandoffRequired: "default"}, func(s SessionConfig) bool { return s.ChildHandoffRequired == nil }},
		{"isolation worktree", GuardrailDials{ChildIsolation: "worktree"}, func(s SessionConfig) bool { return s.ChildIsolation == ChildIsolationWorktree }},
		{"isolation shared", GuardrailDials{ChildIsolation: "off"}, func(s SessionConfig) bool { return s.ChildIsolation == ChildIsolationShared }},
	}
	for _, tc := range cases {
		if err := SetGlobalGuardrailDials(tc.dials); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if s := readGlobalSession(t); !tc.check(s) {
			t.Fatalf("%s: session = %+v", tc.name, s)
		}
	}
	cfg, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "dracula" {
		t.Fatalf("unrelated config lost: theme=%q", cfg.Theme)
	}
}

func TestSetGlobalGuardrailDialsRejectsWithoutWriting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetGlobalGuardrailDials(GuardrailDials{MaxSessionCostUSD: "5", TurnTimeout: "900"}); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		dials GuardrailDials
		field string
	}{
		{GuardrailDials{MaxSessionCostUSD: "-1"}, "maxSessionCostUSD"},
		{GuardrailDials{MaxSessionCostUSD: "NaN"}, "maxSessionCostUSD"},
		{GuardrailDials{MaxSessionCostUSD: "Inf"}, "maxSessionCostUSD"},
		{GuardrailDials{MaxSessionCostUSD: "lots"}, "maxSessionCostUSD"},
		{GuardrailDials{TurnTimeout: "soon"}, "turnTimeoutS"},
		{GuardrailDials{MaxTurnTokens: "-5"}, "maxTurnTokens"},
		{GuardrailDials{MaxTurnTokens: "1.5"}, "maxTurnTokens"},
		{GuardrailDials{AgentBudgetMaxCostUSD: "-0.1"}, "agentBudget.maxCostUsd"},
		{GuardrailDials{AgentBudgetLoopDetectN: "x"}, "agentBudget.loopDetectN"},
		{GuardrailDials{DelegationMode: "strict"}, "delegationPolicy.mode"},
		{GuardrailDials{DelegationTinyPromptRunes: "-3"}, "delegationPolicy.tinyPromptRunes"},
		{GuardrailDials{DelegationMaxLiveChildren: "-1"}, "delegationPolicy.maxLiveChildren"},
		{GuardrailDials{ChildHandoffRequired: "maybe"}, "childHandoffRequired"},
		{GuardrailDials{ChildIsolation: "vm"}, "childIsolation"},
		// A valid dial alongside an invalid one must not be written either.
		{GuardrailDials{MaxSessionCostUSD: "50", ChildIsolation: "vm"}, "childIsolation"},
	}
	for _, tc := range bad {
		err := SetGlobalGuardrailDials(tc.dials)
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Fatalf("%+v: err = %v, want mention of %s", tc.dials, err, tc.field)
		}
	}
	s := readGlobalSession(t)
	if s.MaxSessionCostUSD != 5 || s.TurnTimeoutS != 900 || s.ChildIsolation != "" {
		t.Fatalf("rejected update changed config: %+v", s)
	}
}

func TestParseTurnTimeoutSeconds(t *testing.T) {
	cases := map[string]int{
		"": -1, "off": -1, "none": -1, "0": -1, "-5": -1, "0s": -1,
		"1800": 1800, "30m": 1800, "1h": 3600, "500ms": 1,
	}
	for in, want := range cases {
		got, err := ParseTurnTimeoutSeconds(in)
		if err != nil || got != want {
			t.Fatalf("ParseTurnTimeoutSeconds(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseTurnTimeoutSeconds("later"); err == nil {
		t.Fatal("accepted garbage turn timeout")
	}
}

func TestEffectiveGuardrailHelpers(t *testing.T) {
	if got := EffectiveChildIsolation(""); got != ChildIsolationShared {
		t.Fatalf("empty isolation = %q", got)
	}
	if got := EffectiveChildIsolation("off"); got != ChildIsolationShared {
		t.Fatalf("off isolation = %q", got)
	}
	if got := EffectiveChildIsolation("worktree"); got != ChildIsolationWorktree {
		t.Fatalf("worktree isolation = %q", got)
	}
	f := false
	if !EffectiveChildHandoffRequired(nil) || EffectiveChildHandoffRequired(&f) {
		t.Fatal("handoff effective wrong")
	}
	for in, want := range map[string]string{"": "enforce", "advise": "advise", "OFF": "off", "bogus": "enforce"} {
		if got := EffectiveDelegationMode(in); got != want {
			t.Fatalf("EffectiveDelegationMode(%q) = %q, want %q", in, got, want)
		}
	}
}
