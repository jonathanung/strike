package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestEvaluateDelegationPolicyTinyLocal(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt: "fix typo in readme",
	})
	if d.Action != PolicyActionLocal {
		t.Fatalf("action = %q, want local; %+v", d.Action, d)
	}
	if d.Hard {
		t.Fatal("tiny must be soft")
	}
	if !strings.Contains(d.Reason, "tiny") || !hasCode(d.Codes, "tiny") {
		t.Fatalf("reason/codes = %q %v", d.Reason, d.Codes)
	}
}

func TestEvaluateDelegationPolicyIndependentDelegate(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:    DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:    "x", // short but intentional
		Specialty: "explore",
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("action = %q, want delegate; %+v", d.Action, d)
	}
	if !hasCode(d.Codes, "independent") {
		t.Fatalf("codes = %v", d.Codes)
	}
}

func TestEvaluateDelegationPolicyAgentPinDelegate(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:   DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:   "run tests",
		AgentPin: "tester",
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("action = %q want delegate; %+v", d.Action, d)
	}
}

func TestEvaluateDelegationPolicyOverlapLocal(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:       DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:       "implement the full feature across packages with careful design",
		AgentPin:     "general",
		Paths:        []string{"internal/foo.go"},
		OverlapPaths: []string{"internal/foo.go"},
	})
	if d.Action != PolicyActionLocal {
		t.Fatalf("action = %q want local on overlap; %+v", d.Action, d)
	}
	if !hasCode(d.Codes, "overlap") {
		t.Fatalf("codes = %v", d.Codes)
	}
}

func TestEvaluateDelegationPolicyForceOverride(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt: "tiny",
		Force:  true,
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("action = %q want delegate; %+v", d.Action, d)
	}
	if !d.Overridden || !hasCode(d.Codes, "override") {
		t.Fatalf("override flags missing: %+v", d)
	}
	if d.Preferred != PolicyActionLocal {
		t.Fatalf("preferred = %q want local", d.Preferred)
	}
}

func TestEvaluateDelegationPolicyForceCannotBypassHard(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:   DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:   "anything",
		Force:    true,
		Depth:    1,
		MaxDepth: 1,
	})
	if d.Action != PolicyActionDeny || !d.Hard {
		t.Fatalf("want hard deny; %+v", d)
	}
	if !hasCode(d.Codes, "depth_ceiling") {
		t.Fatalf("codes = %v", d.Codes)
	}
}

func TestEvaluateDelegationPolicyChildCountCeiling(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{
			Mode:            PolicyEnforce,
			MaxLiveChildren: 2,
		},
		Prompt:       "work with agent",
		AgentPin:     "general",
		LiveChildren: 2,
	})
	if d.Action != PolicyActionDeny || !hasCode(d.Codes, "child_count_ceiling") {
		t.Fatalf("want child_count_ceiling deny; %+v", d)
	}
}

func TestEvaluateDelegationPolicyBudgetExhausted(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:          DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:          "more work",
		AgentPin:        "general",
		BudgetExhausted: true,
		Force:           true,
	})
	if d.Action != PolicyActionDeny || !hasCode(d.Codes, "budget_exhausted") {
		t.Fatalf("want budget_exhausted; %+v", d)
	}
}

func TestEvaluateDelegationPolicyAdviseSpawns(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyAdvise},
		Prompt: "tiny",
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("advise must spawn; %+v", d)
	}
	if d.Preferred != PolicyActionLocal {
		t.Fatalf("preferred = %q", d.Preferred)
	}
	if !hasCode(d.Codes, "advise_spawn") {
		t.Fatalf("codes = %v", d.Codes)
	}
}

func TestEvaluateDelegationPolicyOff(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyOff},
		Prompt: "tiny",
	})
	if d.Action != PolicyActionDelegate || !hasCode(d.Codes, "off") {
		t.Fatalf("%+v", d)
	}
}

func TestEvaluateDelegationPolicyMultiPathDelegate(t *testing.T) {
	long := strings.Repeat("investigate and implement carefully across modules. ", 10)
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt: long,
		Paths:  []string{"a.go", "b.go", "c.go"},
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("multi-path substantial → delegate; %+v", d)
	}
	if !hasCode(d.Codes, "multi_path") {
		t.Fatalf("codes = %v", d.Codes)
	}
}

func TestEvaluateDelegationPolicyCriteriaDelegate(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:   DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:   "x",
		Criteria: []string{"tests pass"},
	})
	if d.Action != PolicyActionDelegate || !hasCode(d.Codes, "independent") {
		t.Fatalf("%+v", d)
	}
}

func TestEvaluateDelegationPolicyVerifyDelegate(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config: DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt: "x",
		Verify: []tool.VerifyGate{{Kind: "cmd", Value: "make test"}},
	})
	if d.Action != PolicyActionDelegate {
		t.Fatalf("%+v", d)
	}
}

func TestNormalizeDelegationPolicyDefaults(t *testing.T) {
	// Zero-value Options stay off (legacy always-spawn for embedders/tests).
	c := NormalizeDelegationPolicy(DelegationPolicyConfig{})
	if c.Mode != PolicyOff {
		t.Fatalf("mode = %q want off", c.Mode)
	}
	if c.TinyPromptRunes != DefaultPolicyTinyPromptRunes {
		t.Fatalf("tiny = %d", c.TinyPromptRunes)
	}
	if c.MaxPathsLocal != DefaultPolicyMaxPathsLocal {
		t.Fatalf("paths = %d", c.MaxPathsLocal)
	}
	// Product default used by CLI composition root.
	p := DefaultDelegationPolicy()
	if p.Mode != PolicyEnforce {
		t.Fatalf("DefaultDelegationPolicy mode = %q", p.Mode)
	}
}

func TestPolicyMetricsRecord(t *testing.T) {
	var m DelegationPolicyMetrics
	m.record(PolicyDecision{Action: PolicyActionLocal}, PolicyEnforce)
	m.record(PolicyDecision{Action: PolicyActionDelegate, Overridden: true}, PolicyEnforce)
	m.record(PolicyDecision{Action: PolicyActionDeny}, PolicyEnforce)
	m.record(PolicyDecision{Action: PolicyActionDelegate, Preferred: PolicyActionLocal}, PolicyAdvise)
	del, loc, den, ov, adv := m.Snapshot()
	if del != 2 || loc != 1 || den != 1 || ov != 1 || adv != 1 {
		t.Fatalf("got del=%d loc=%d den=%d ov=%d adv=%d", del, loc, den, ov, adv)
	}
}

func hasCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func TestEvaluateDelegationPolicyDelegationCeiling(t *testing.T) {
	d := EvaluateDelegationPolicy(PolicyInput{
		Config:           DelegationPolicyConfig{Mode: PolicyEnforce},
		Prompt:           "x",
		AgentPin:         "general",
		TotalDelegations: MaxDelegations,
		MaxDelegations:   MaxDelegations,
		Force:            true,
	})
	if d.Action != PolicyActionDeny || !hasCode(d.Codes, "delegation_ceiling") {
		t.Fatalf("%+v", d)
	}
}

func TestSpawnChildPolicyLiveCeiling(t *testing.T) {
	eng := New(Options{
		SessionID: "pol-live-internal",
		Agents:    []Agent{{Name: "build"}, {Name: "general"}},
		DelegationPolicy: DelegationPolicyConfig{
			Mode:            PolicyEnforce,
			MaxLiveChildren: 1,
		},
	})
	fakeDone := make(chan struct{})
	eng.childMu.Lock()
	if eng.children == nil {
		eng.children = map[string]*childHandle{}
	}
	eng.children["fake-live"] = &childHandle{
		id:    "fake-live",
		agent: "general",
		done:  fakeDone,
	}
	eng.childMu.Unlock()
	defer close(fakeDone)

	_, err := eng.spawnChild(context.Background(), tool.TaskRequest{
		Prompt: "independent work for general agent",
		Agent:  "general",
	})
	if err == nil {
		t.Fatal("expected deny")
	}
	if !strings.Contains(err.Error(), "delegation denied") {
		t.Fatalf("err = %v", err)
	}
	_, _, den, _, _ := eng.PolicyMetricsSnapshot()
	if den < 1 {
		t.Fatalf("deny = %d", den)
	}
}

func TestSpawnChildPolicyOverlapLocal(t *testing.T) {
	dir := t.TempDir()
	eng := New(Options{
		SessionID: "pol-overlap-internal",
		WorkDir:   dir,
		Agents:    []Agent{{Name: "build"}, {Name: "general"}},
		DelegationPolicy: DelegationPolicyConfig{
			Mode: PolicyEnforce,
		},
	})
	if eng.team == nil {
		t.Fatal("team")
	}
	own := eng.team.Ownership()
	abs, display := resolveTeamOwnershipPath(dir, "shared.go")
	res := own.AcquireLease("other-session", "peer", abs, display, true)
	if res.Blocked {
		t.Fatalf("lease blocked: %+v", res)
	}

	out, err := eng.spawnChild(context.Background(), tool.TaskRequest{
		Prompt: "edit the shared file carefully with full context",
		Agent:  "general",
		ContextBundle: tool.ContextBundle{
			AllowedPaths: []string{"shared.go"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Status != "local" {
		t.Fatalf("status = %q want local; %+v", out.Status, out)
	}
	if !strings.Contains(out.PolicyReason, "overlap") {
		t.Fatalf("PolicyReason = %q", out.PolicyReason)
	}
}
