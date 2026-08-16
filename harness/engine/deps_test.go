package engine

import (
	"errors"
	"testing"
)

func TestCanonicalProviderID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{" OpenAI ", "openai"},
		{"gemini", "google"},
		{"Gemini", "google"},
		{"google", "google"},
	}
	for _, tc := range cases {
		if got := CanonicalProviderID(tc.in); got != tc.want {
			t.Errorf("CanonicalProviderID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWantChildWorktree(t *testing.T) {
	cases := []struct {
		session, spawn string
		want           bool
	}{
		{"", "", false},
		{"shared", "", false},
		{"worktree", "", true},
		{"shared", "worktree", true},
		{"worktree", "shared", false},
		{"off", "worktree", true},
	}
	for _, tc := range cases {
		if got := WantChildWorktree(tc.session, tc.spawn); got != tc.want {
			t.Errorf("WantChildWorktree(%q,%q) = %v, want %v", tc.session, tc.spawn, got, tc.want)
		}
	}
}

func TestValidateWorkflow(t *testing.T) {
	if err := ValidateWorkflow(BuiltinPlanImplement()); err != nil {
		t.Fatalf("builtin: %v", err)
	}
	if err := ValidateWorkflow(Workflow{Name: "x"}); err == nil {
		t.Fatal("expected error for empty phases")
	}
	if err := ValidateWorkflow(Workflow{
		Name:   "ok",
		Phases: []Phase{{Name: "a", Exit: ExitGate{Type: GateCheck}}},
	}); err == nil {
		t.Fatal("expected check-gate command error")
	}
	if err := ValidatePhaseName(""); err == nil {
		t.Fatal("expected empty phase name error")
	}
}

func TestPlanErrorSentinels(t *testing.T) {
	if !errors.Is(ErrPlanNotOwner, ErrPlanNotOwner) {
		t.Fatal("sentinel")
	}
	if errors.Is(ErrPlanConflict, ErrPlanNotOwner) {
		t.Fatal("distinct sentinels")
	}
}
