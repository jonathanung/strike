package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

type namedStub struct{ name string }

func (n namedStub) Name() string        { return n.name }
func (n namedStub) Description() string { return n.name }
func (n namedStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (n namedStub) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	return tool.Result{}, nil
}

func testActReg(t *testing.T) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry(
		tool.NewRead(), tool.NewTask(),
		namedStub{name: "plan_write"}, namedStub{name: "plan_read"},
		namedStub{name: "enter_plan_mode"}, namedStub{name: "exit_plan_mode"},
		namedStub{name: "phase_done"},
		tool.NewAgentRoster(), tool.NewAgentMessage(), tool.NewAgentBroadcast(),
		tool.NewTeamTask(), tool.NewWait(), tool.NewWebFetch(),
	)
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)
	return reg
}

func TestApplyWorkflowToolActivationPlan(t *testing.T) {
	reg := testActReg(t)
	e := New(Options{
		SessionID: "s1",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	e.permMode = protocol.PermissionModePlan
	got := e.applyWorkflowToolActivation()
	if len(got) == 0 || got[0] != activationFamilyPlan {
		t.Fatalf("families = %v", got)
	}
	if !reg.Discovered("plan_write") || !reg.Discovered("enter_plan_mode") {
		t.Fatal("plan family not discovered")
	}
	if reg.Discovered("webfetch") {
		t.Fatal("webfetch should not activate")
	}
}

func TestApplyWorkflowToolActivationChildAndTeam(t *testing.T) {
	reg := testActReg(t)
	e := New(Options{
		SessionID: "s1",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	if fam := e.applyWorkflowToolActivation(); len(fam) != 0 {
		t.Fatalf("solo families = %v", fam)
	}
	if reg.SchemaAdvanced("task") {
		t.Fatal("task should stay basic")
	}

	e.children["c1"] = &childHandle{id: "c1"}
	fam := e.applyWorkflowToolActivation()
	has := map[string]bool{}
	for _, f := range fam {
		has[f] = true
	}
	if !has[activationFamilyChild] || !has[activationFamilyTask] {
		t.Fatalf("child families = %v", fam)
	}
	if has[activationFamilyTeam] {
		t.Fatal("team should not activate with 1 child")
	}
	if !reg.Discovered("agent_roster") || !reg.SchemaAdvanced("task") {
		t.Fatal("child activation incomplete")
	}

	e.children["c2"] = &childHandle{id: "c2"}
	fam = e.applyWorkflowToolActivation()
	has = map[string]bool{}
	for _, f := range fam {
		has[f] = true
	}
	if !has[activationFamilyTeam] {
		t.Fatalf("team families = %v", fam)
	}
	if !reg.Discovered("team_task") || !reg.Discovered("agent_broadcast") {
		t.Fatal("team tools not discovered")
	}
}

func TestApplyWorkflowToolActivationHistoryChild(t *testing.T) {
	reg := testActReg(t)
	e := New(Options{
		SessionID: "s1",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	e.childHistory["old"] = &childRecord{id: "old"}
	fam := e.applyWorkflowToolActivation()
	has := false
	for _, f := range fam {
		if f == activationFamilyChild {
			has = true
		}
	}
	if !has {
		t.Fatalf("history child should activate child family: %v", fam)
	}
	if !reg.Discovered("agent_message") {
		t.Fatal("expected child tools from history")
	}
}

func TestActivationSourceSuffix(t *testing.T) {
	if activationSourceSuffix(nil) != "" {
		t.Fatal("empty")
	}
	got := activationSourceSuffix([]string{"plan", "child"})
	if got != "+activate:plan,child" {
		t.Fatalf("got %q", got)
	}
}
