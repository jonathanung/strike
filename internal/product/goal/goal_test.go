package goal

import (
	"strings"
	"testing"
)

func TestParseCheckSpec(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw      string
		wantKind CheckKind
		wantVal  string
		wantErr  bool
	}{
		{"cmd: pytest -q", CheckCmd, "pytest -q", false},
		{"predicate: always_true", CheckPredicate, "always_true", false},
		{"judge: is the UI polished?", CheckJudge, "is the UI polished?", false},
		{"", "", "", true},
		{"make it better", "", "", true},
		{"cmd:", "", "", true},
		{"foo: bar", "", "", true},
	}
	for _, tt := range tests {
		got, err := ParseCheckSpec(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseCheckSpec(%q) err=nil, want error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCheckSpec(%q) err=%v", tt.raw, err)
			continue
		}
		if got.Kind != tt.wantKind || got.Value != tt.wantVal {
			t.Errorf("ParseCheckSpec(%q)=%+v, want kind=%s val=%s", tt.raw, got, tt.wantKind, tt.wantVal)
		}
	}
}

func TestValidateGoal(t *testing.T) {
	t.Parallel()
	c := DefaultConstraints()
	okCrit := []Criterion{{
		Description: "tests pass",
		Check:       CheckSpec{Kind: CheckCmd, Value: "true"},
	}}
	if err := ValidateGoal("ship feature", okCrit, c); err != nil {
		t.Fatalf("valid goal: %v", err)
	}
	if err := ValidateGoal("", okCrit, c); err == nil {
		t.Fatal("empty description should fail")
	}
	if err := ValidateGoal("vague", nil, c); err == nil {
		t.Fatal("no criteria should fail")
	}
	if err := ValidateGoal("vague", []Criterion{{Description: "be better"}}, c); err == nil {
		t.Fatal("criterion without check should fail")
	}
	if err := ValidateGoal("x", okCrit, Constraints{MaxIterations: 0}); err == nil {
		t.Fatal("bad constraints should fail")
	}
	// Unfalsifiable empty check value
	bad := []Criterion{{Description: "x", Check: CheckSpec{Kind: CheckCmd, Value: "  "}}}
	if err := ValidateGoal("x", bad, c); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty check value: err=%v", err)
	}
}
