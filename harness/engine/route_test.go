package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func testAgents() []RouteAgent {
	return []RouteAgent{
		{Name: "build", Capabilities: nil},
		{Name: "explore", Capabilities: []string{"search"}},
		{Name: "general", Capabilities: []string{"implement"}},
		{Name: "tester", Capabilities: []string{"test"}},
		{Name: "reviewer", Capabilities: []string{"review"}},
		{Name: "debugger", Capabilities: []string{"debug"}},
		{Name: "cheap-explore", Capabilities: []string{"explore"}, Model: "haiku", CostClass: "low"},
		{Name: "pricey-explore", Capabilities: []string{"explore"}, Model: "opus", CostClass: "high"},
	}
}

func TestRouteOffInheritsParent(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteOff,
		ParentAgent: "build",
		Agents:      testAgents(),
	})
	if d.Mode != "off" {
		t.Fatalf("mode = %q", d.Mode)
	}
	if d.Agent != "build" || d.Model != "" {
		t.Fatalf("decision = %+v, want inherit build/empty model", d)
	}
	if !strings.Contains(d.Reason, "route=off") {
		t.Fatalf("reason = %q", d.Reason)
	}
}

func TestRoutePinOverridesAuto(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		AgentPin:    "tester",
		ModelPin:    "gpt-test",
		ParentAgent: "build",
		Agents:      testAgents(),
		Load: RouteLoad{
			ActiveByAgent: map[string]int{"tester": 5},
			BudgetBlocked: map[string]bool{"tester": true},
		},
	})
	if d.Mode != "pin" {
		t.Fatalf("mode = %q, want pin", d.Mode)
	}
	if d.Agent != "tester" || d.Model != "gpt-test" {
		t.Fatalf("pin ignored: %+v", d)
	}
	if d.Fallback {
		t.Fatal("pins must not report fallback")
	}
	if !strings.Contains(d.Reason, "pin") {
		t.Fatalf("reason = %q", d.Reason)
	}
}

func TestRouteModelPinAloneKeepsParentAgent(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		ModelPin:    "fast-model",
		ParentAgent: "build",
		Agents:      testAgents(),
	})
	if d.Agent != "build" || d.Model != "fast-model" {
		t.Fatalf("got %+v", d)
	}
	if d.Mode != "pin" {
		t.Fatalf("mode = %q", d.Mode)
	}
}

func TestRouteAutoSelectsSpecialty(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		ParentAgent: "build",
		Agents:      testAgents(),
	})
	if d.Mode != "auto" {
		t.Fatalf("mode = %q", d.Mode)
	}
	// explore name matches; cheap-explore/pricey-explore also have explore cap.
	// Stable rank: higher overlap then lower name — "cheap-explore" and "explore"
	// and "pricey-explore" all match. explore has name+search; cheap has explore cap.
	// All have overlap 1 for required [explore]. Tie-break by name ascending:
	// cheap-explore < explore < pricey-explore.
	if d.Agent != "cheap-explore" {
		t.Fatalf("agent = %q, want cheap-explore (stable name order on tie)", d.Agent)
	}
	if d.Fallback {
		t.Fatal("unexpected fallback")
	}
	if !strings.Contains(d.Reason, "primary") {
		t.Fatalf("reason = %q", d.Reason)
	}
	if d.Model != "haiku" {
		t.Fatalf("model = %q, want agent pin haiku", d.Model)
	}
}

func TestRouteAutoFallbackOnConcurrent(t *testing.T) {
	// Ideal primary among equal specialty score is cheap-explore (name order).
	// At concurrency → fall through to explore.
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		ParentAgent: "build",
		Agents:      testAgents(),
		Load: RouteLoad{
			ActiveByAgent: map[string]int{
				"cheap-explore": 1,
			},
		},
		MaxConcurrent: 1,
	})
	if d.Agent != "explore" {
		t.Fatalf("agent = %q, want explore after cheap-explore at concurrency", d.Agent)
	}
	if !d.Fallback {
		t.Fatal("want Fallback=true")
	}
	if !strings.Contains(d.Reason, "fallback_from=cheap-explore") {
		t.Fatalf("reason = %q", d.Reason)
	}
	found := false
	for _, s := range d.Skipped {
		if strings.HasPrefix(s, "cheap-explore:concurrent") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("skipped = %v, want cheap-explore:concurrent", d.Skipped)
	}
}

func TestRouteAutoFallbackOnBudget(t *testing.T) {
	// Block the name-order primary (cheap-explore) via budget.
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		ParentAgent: "build",
		Agents:      testAgents(),
		Load: RouteLoad{
			BudgetBlocked: map[string]bool{
				"cheap-explore": true,
			},
		},
	})
	if d.Agent != "explore" {
		t.Fatalf("agent = %q, want explore after budget block on cheap-explore", d.Agent)
	}
	if !d.Fallback {
		t.Fatal("want Fallback=true")
	}
}

func TestRouteAutoMaxCostClass(t *testing.T) {
	// Only pricey-explore has high cost; filter it out. cheap-explore is low and free.
	d := Route(RouteInput{
		Mode:         RouteAuto,
		Required:     []string{"explore"},
		MaxCostClass: "low",
		ParentAgent:  "build",
		Agents:       testAgents(),
	})
	if d.Agent != "cheap-explore" {
		t.Fatalf("agent = %q, want cheap-explore under max_cost_class=low", d.Agent)
	}
	found := false
	for _, s := range d.Skipped {
		if s == "pricey-explore:cost" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %v, want pricey-explore:cost", d.Skipped)
	}
}

func TestRouteAutoModelAllowList(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"explore"},
		ModelAllow:  []string{"opus"},
		ParentAgent: "build",
		Agents:      testAgents(),
	})
	// cheap-explore model haiku not allowed; explore has empty model (inherit) — allowed;
	// pricey-explore opus allowed. cheap filtered; explore (empty model) ranks before pricey by name.
	if d.Agent != "explore" {
		t.Fatalf("agent = %q, want explore (empty model passes allow-list)", d.Agent)
	}
}

func TestRouteDeterministic(t *testing.T) {
	in := RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"test"},
		ParentAgent: "build",
		Agents:      testAgents(),
		Load: RouteLoad{
			ActiveByAgent: map[string]int{"tester": 0},
		},
	}
	a := Route(in)
	b := Route(in)
	if a.Agent != b.Agent || a.Model != b.Model || a.Reason != b.Reason || a.Fallback != b.Fallback || a.Mode != b.Mode {
		t.Fatalf("non-deterministic:\n%+v\n%+v", a, b)
	}
	if a.Agent != "tester" {
		t.Fatalf("agent = %q, want tester", a.Agent)
	}
}

func TestRouteLastResortGeneral(t *testing.T) {
	d := Route(RouteInput{
		Mode:        RouteAuto,
		Required:    []string{"nonexistent-specialty"},
		ParentAgent: "build",
		Agents:      testAgents(),
	})
	if d.Agent != "general" {
		t.Fatalf("agent = %q, want general last-resort", d.Agent)
	}
	if !d.Fallback {
		t.Fatal("want fallback")
	}
}

func TestApplyRouteDecisionRespectsExistingPins(t *testing.T) {
	req := tool.TaskRequest{Agent: "keep", Model: "keep-model", Effort: "high"}
	applyRouteDecision(&req, RouteDecision{Agent: "other", Model: "other-model", Effort: "low"})
	// applyRouteDecision overwrites agent always when decision agent set;
	// model/effort only fill empties. Document current contract:
	if req.Agent != "other" {
		t.Fatalf("agent = %q", req.Agent)
	}
	if req.Model != "keep-model" {
		t.Fatalf("model = %q, want keep existing pin", req.Model)
	}
	if req.Effort != "high" {
		t.Fatalf("effort = %q, want keep existing pin", req.Effort)
	}
}

func TestNormalizeTags(t *testing.T) {
	got := normalizeTags([]string{" Explore ", "test,review", "TEST"})
	want := []string{"explore", "review", "test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAgentNameIsImplicitCapability(t *testing.T) {
	if !agentMatchesRequired(RouteAgent{Name: "explore"}, []string{"explore"}) {
		t.Fatal("name should match specialty")
	}
	if !agentMatchesRequired(RouteAgent{Name: "scout", Capabilities: []string{"explore"}}, []string{"explore"}) {
		t.Fatal("declared cap should match")
	}
	if agentMatchesRequired(RouteAgent{Name: "build"}, []string{"explore"}) {
		t.Fatal("build should not match explore")
	}
}

func TestRouteEffortFromAgentPin(t *testing.T) {
	d := Route(RouteInput{
		Mode:     RouteAuto,
		Required: []string{"reviewer"},
		Agents: []RouteAgent{
			{Name: "reviewer", Effort: protocol.EffortHigh},
		},
	})
	if d.Effort != string(protocol.EffortHigh) {
		t.Fatalf("effort = %q, want high (got agent=%q reason=%q)", d.Effort, d.Agent, d.Reason)
	}
}
