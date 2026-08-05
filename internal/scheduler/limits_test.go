package scheduler

import (
	"strings"
	"testing"
)

func TestMergeLimitsProjectOverridesPerPool(t *testing.T) {
	base := Limits{
		PoolProcess: 8,
		PoolBuild:   2,
		PoolModel:   3,
	}
	layer := Limits{
		PoolBuild: 4, // override
		PoolTest:  1, // new
		// process and model omitted → preserve base
	}
	got := MergeLimits(base, layer)
	want := Limits{
		PoolProcess: 8,
		PoolBuild:   4,
		PoolModel:   3,
		PoolTest:    1,
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %+v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("limits[%q]=%d want %d", k, got[k], v)
		}
	}
	// Inputs not mutated.
	if base[PoolBuild] != 2 {
		t.Fatalf("base mutated: %v", base)
	}
	if _, ok := layer[PoolProcess]; ok {
		t.Fatal("layer unexpectedly gained process")
	}
}

func TestMergeLimitsEmpty(t *testing.T) {
	if MergeLimits(nil, nil) != nil {
		t.Fatal("nil+nil want nil")
	}
	got := MergeLimits(Limits{PoolModel: 2}, nil)
	if got[PoolModel] != 2 {
		t.Fatalf("got %+v", got)
	}
	got = MergeLimits(nil, Limits{PoolTest: 1})
	if got[PoolTest] != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateLimitsUnlimitedByOmission(t *testing.T) {
	// Empty / nil is valid: all pools unlimited.
	if err := ValidateLimits(nil, "test"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLimits(Limits{}, "test"); err != nil {
		t.Fatal(err)
	}
	pools := poolsFromLimits(nil)
	for _, name := range DefaultPoolNames {
		if pools[name] != 0 {
			t.Fatalf("pool %s capacity=%d want 0 (unlimited)", name, pools[name])
		}
	}
}

func TestValidateLimitsRejectsNonPositive(t *testing.T) {
	cases := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLimits(Limits{PoolProcess: tc.limit}, "/tmp/cfg")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "/tmp/cfg") {
				t.Fatalf("error should name source: %v", err)
			}
			if !strings.Contains(err.Error(), "positive") {
				t.Fatalf("error should mention positive: %v", err)
			}
		})
	}
}

func TestValidateLimitsRejectsUnknownPool(t *testing.T) {
	err := ValidateLimits(Limits{"gpu": 1}, "global")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "gpu") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateLimitsAcceptsPositiveKnown(t *testing.T) {
	l := Limits{
		PoolProcess:   8,
		PoolBuild:     2,
		PoolTest:      4,
		PoolModel:     3,
		PoolContainer: 1,
	}
	if err := ValidateLimits(l, ""); err != nil {
		t.Fatal(err)
	}
	pools := poolsFromLimits(l)
	for name, want := range l {
		if pools[name] != want {
			t.Errorf("%s=%d want %d", name, pools[name], want)
		}
	}
}

func TestCloneLimits(t *testing.T) {
	if CloneLimits(nil) != nil {
		t.Fatal("nil")
	}
	in := Limits{PoolBuild: 2}
	out := CloneLimits(in)
	out[PoolBuild] = 9
	if in[PoolBuild] != 2 {
		t.Fatal("clone shared map")
	}
}
