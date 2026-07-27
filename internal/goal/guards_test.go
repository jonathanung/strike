package goal

import (
	"testing"
	"time"
)

func TestGuardSuccess(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status: StatusActive,
		Criteria: []Criterion{
			{Description: "a", Satisfied: true},
			{Description: "b", Satisfied: true},
		},
		Constraints: DefaultConstraints(),
	}
	r := CheckGuards(GuardContext{Goal: g, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusDone {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardHumanAbort(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:         StatusActive,
		AbortRequested: true,
		Criteria:       []Criterion{{Description: "a"}},
		Constraints:    DefaultConstraints(),
	}
	r := CheckGuards(GuardContext{Goal: g, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusAborted {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardBudgetIterations(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:        StatusActive,
		LastIteration: 3,
		Criteria:      []Criterion{{Description: "a"}},
		Constraints:   Constraints{MaxIterations: 3, MaxWallClockS: 999, MaxNoProgressIters: 99, MaxCostUSD: 100},
	}
	r := CheckGuards(GuardContext{Goal: g, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardBudgetCost(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:   StatusActive,
		CostUSD:  5.0,
		Criteria: []Criterion{{Description: "a"}},
		Constraints: Constraints{
			MaxIterations: 25, MaxCostUSD: 5.0, MaxWallClockS: 999, MaxNoProgressIters: 99,
		},
	}
	r := CheckGuards(GuardContext{Goal: g, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardBudgetWallClock(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g := Goal{
		Status:          StatusActive,
		ActiveStartedAt: start,
		Criteria:        []Criterion{{Description: "a"}},
		Constraints: Constraints{
			MaxIterations: 25, MaxCostUSD: 100, MaxWallClockS: 10, MaxNoProgressIters: 99,
		},
	}
	r := CheckGuards(GuardContext{Goal: g, Now: start.Add(11 * time.Second)})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardNoProgressHash(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:   StatusActive,
		Criteria: []Criterion{{Description: "a"}},
		Constraints: Constraints{
			MaxIterations: 25, MaxCostUSD: 100, MaxWallClockS: 9999, MaxNoProgressIters: 3,
		},
	}
	hist := []IterationRecord{
		{N: 1, StateHash: "same"},
		{N: 2, StateHash: "same"},
		{N: 3, StateHash: "same"},
	}
	r := CheckGuards(GuardContext{Goal: g, History: hist, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardNoProgressActionSequence(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:   StatusActive,
		Criteria: []Criterion{{Description: "a"}},
		Constraints: Constraints{
			MaxIterations: 25, MaxCostUSD: 100, MaxWallClockS: 9999, MaxNoProgressIters: 3,
		},
	}
	acts := []ActionRecord{{Tool: "bash", Args: map[string]string{"command": "x"}}}
	hist := []IterationRecord{
		{N: 1, StateHash: "h1", Actions: acts},
		{N: 2, StateHash: "h2", Actions: acts},
		{N: 3, StateHash: "h3", Actions: acts},
	}
	r := CheckGuards(GuardContext{Goal: g, History: hist, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardIrrecoverable(t *testing.T) {
	t.Parallel()
	g := Goal{
		Status:   StatusActive,
		Criteria: []Criterion{{Description: "a"}},
		Constraints: Constraints{
			MaxIterations: 25, MaxCostUSD: 100, MaxWallClockS: 9999, MaxNoProgressIters: 99,
		},
	}
	fail := ActionRecord{Tool: "bash", OK: false, Error: "permission denied"}
	hist := []IterationRecord{
		{N: 1, StateHash: "a", Actions: []ActionRecord{fail}},
		{N: 2, StateHash: "b", Actions: []ActionRecord{fail}},
		{N: 3, StateHash: "c", Actions: []ActionRecord{fail}},
	}
	r := CheckGuards(GuardContext{Goal: g, History: hist, Now: time.Now()})
	if !r.Tripped || r.NextStatus != StatusFailed {
		t.Fatalf("got %+v", r)
	}
}

func TestGuardOrderSuccessBeforeAbort(t *testing.T) {
	t.Parallel()
	// success is checked first
	g := Goal{
		Status:         StatusActive,
		AbortRequested: true,
		Criteria:       []Criterion{{Description: "a", Satisfied: true}},
		Constraints:    DefaultConstraints(),
	}
	r := CheckGuards(GuardContext{Goal: g, Now: time.Now()})
	if r.NextStatus != StatusDone {
		t.Fatalf("want done first, got %+v", r)
	}
}
