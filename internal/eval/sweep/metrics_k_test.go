package sweep

import (
	"math"
	"testing"
)

func TestPassAtKKnown(t *testing.T) {
	// n=2, c=1, k=1 → pass@1 = 0.5
	m := ComputeRepeatedTrials([]TrialOutcome{{true}, {false}}, 1)
	if math.Abs(m.PassAtK-0.5) > 1e-9 {
		t.Fatalf("pass@1=%v", m.PassAtK)
	}
	// pass^1 = 0.5
	if math.Abs(m.PassHatK-0.5) > 1e-9 {
		t.Fatalf("pass^1=%v", m.PassHatK)
	}
	// flakiness = 0.5
	if math.Abs(m.Flakiness-0.5) > 1e-9 {
		t.Fatalf("flakiness=%v", m.Flakiness)
	}

	// all success
	m = ComputeRepeatedTrials([]TrialOutcome{{true}, {true}, {true}}, 2)
	if m.PassAtK != 1 || m.PassHatK != 1 || m.Flakiness != 0 {
		t.Fatalf("%+v", m)
	}

	// all fail
	m = ComputeRepeatedTrials([]TrialOutcome{{false}, {false}}, 2)
	if m.PassAtK != 0 || m.PassHatK != 0 || m.Flakiness != 0 {
		t.Fatalf("%+v", m)
	}
}

func TestPassAtKUnbiased(t *testing.T) {
	// n=5, c=3, k=2
	// ratio = C(2,2)/C(5,2) = 1/10 → pass@2 = 0.9
	trials := []TrialOutcome{{true}, {true}, {true}, {false}, {false}}
	m := ComputeRepeatedTrials(trials, 2)
	if math.Abs(m.PassAtK-0.9) > 1e-9 {
		t.Fatalf("got %v want 0.9", m.PassAtK)
	}
	if m.ConfidenceN != 5 || m.K != 2 || m.Successes != 3 {
		t.Fatalf("%+v", m)
	}
}

func TestAggregateTrialSets(t *testing.T) {
	got := AggregateTrialSets(map[string][]TrialOutcome{
		"b": {{true}},
		"a": {{true}, {false}},
	}, 1)
	if len(got) != 2 || got["a"].PassAtK != 0.5 {
		t.Fatalf("%+v", got)
	}
}
