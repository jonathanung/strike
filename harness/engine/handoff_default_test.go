package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/harness/verify"
)

func TestChildHandoffRequiredPrecedence(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		name                  string
		session, agent, spawn *bool
		want                  bool
	}{
		{name: "default required", want: true},
		{name: "session off", session: &off, want: false},
		{name: "agent off beats session on", session: &on, agent: &off, want: false},
		{name: "spawn on beats agent off", session: &off, agent: &off, spawn: &on, want: true},
		{name: "spawn off beats default", spawn: &off, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := childHandoffRequired(tc.session, tc.agent, tc.spawn); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWithDefaultHandoffGate(t *testing.T) {
	got, err := withDefaultHandoffGate(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != verify.KindSchema || got[0].Value != "handoff" {
		t.Fatalf("default gate = %#v", got)
	}

	got, err = withDefaultHandoffGate(nil, false)
	if err != nil || got != nil {
		t.Fatalf("opt-out got %#v err %v", got, err)
	}

	already := []tool.VerifyGate{
		{Kind: "path", Value: "a.go"},
		{Kind: "schema", Value: "handoff"},
	}
	got, err = withDefaultHandoffGate(already, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("duplicate schema:handoff: %#v", got)
	}

	full := make([]tool.VerifyGate, verify.MaxGates)
	for i := range full {
		full[i] = tool.VerifyGate{Kind: "path", Value: fmt.Sprintf("f-%d", i)}
	}
	if _, err := withDefaultHandoffGate(full, true); err == nil || !strings.Contains(err.Error(), "schema:handoff") {
		t.Fatalf("cap err = %v", err)
	}
	full[0] = tool.VerifyGate{Kind: "schema", Value: "handoff"}
	got, err = withDefaultHandoffGate(full, true)
	if err != nil || len(got) != verify.MaxGates {
		t.Fatalf("existing handoff at cap: n=%d err=%v", len(got), err)
	}
}

func TestSpawnDefaultHandoffRespectsGateCap(t *testing.T) {
	eng := New(Options{
		SessionID:        "handoff-cap",
		DelegationPolicy: DelegationPolicyConfig{Mode: PolicyOff},
		WorkDir:          t.TempDir(),
	})
	gates := make([]tool.VerifyGate, verify.MaxGates)
	for i := range gates {
		gates[i] = tool.VerifyGate{Kind: "path", Value: fmt.Sprintf("f-%d", i)}
	}
	_, err := eng.spawnChild(context.Background(), tool.TaskRequest{
		Prompt: "ship the feature across the package",
		Agent:  "build",
		Verify: gates,
	})
	if err == nil || !strings.Contains(err.Error(), "schema:handoff") {
		t.Fatalf("err = %v", err)
	}
}
