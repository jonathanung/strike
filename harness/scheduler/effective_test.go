package scheduler

import (
	"context"
	"strings"
	"testing"
)

func TestCompileAndSchedulerConfigUnlimitedDefault(t *testing.T) {
	eff, err := Compile(nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := eff.SchedulerConfig()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	snap := s.Snapshot()
	if len(snap.Pools) != len(DefaultPoolNames) {
		t.Fatalf("pools=%d", len(snap.Pools))
	}
	for _, p := range snap.Pools {
		if !p.Unlimited {
			t.Fatalf("pool %s should be unlimited: %+v", p.Name, p)
		}
	}
	if eff.Classify("anything") != ClassGeneral {
		t.Fatal("default class")
	}
}

func TestCompileLayeredLimitsAndRules(t *testing.T) {
	// Simulate global then project merge outside Compile.
	limits := MergeLimits(
		Limits{PoolProcess: 8, PoolModel: 2},
		Limits{PoolModel: 4, PoolBuild: 1},
	)
	rules := []CommandRule{
		{Pattern: "go *", Class: ClassBuild, Source: "global-config"},
		{Pattern: "go test *", Class: ClassTest, Source: "project-config"},
	}
	eff, err := Compile(limits, rules, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if eff.Limits[PoolProcess] != 8 || eff.Limits[PoolModel] != 4 || eff.Limits[PoolBuild] != 1 {
		t.Fatalf("limits=%v", eff.Limits)
	}
	if _, ok := eff.Limits[PoolTest]; ok {
		t.Fatal("test should remain omitted/unlimited")
	}

	s, err := New(eff.SchedulerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// model capacity 4
	var leases []*Lease
	for i := 0; i < 4; i++ {
		l, err := s.Acquire(context.Background(), PoolModel)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	assertPool(t, s, PoolModel, 4, 0)
	for _, l := range leases {
		l.Release()
	}

	if eff.Classify("go build .") != ClassBuild {
		t.Fatal("build")
	}
	if eff.Classify("go test ./...") != ClassTest {
		t.Fatal("test overrides build")
	}
	pools := eff.PoolsForCommand("go test ./...")
	if len(pools) != 2 || pools[0] != PoolProcess || pools[1] != PoolTest {
		t.Fatalf("pools=%v", pools)
	}
}

func TestCompileRejectsBadLimitsWithSource(t *testing.T) {
	_, err := Compile(Limits{PoolProcess: 0}, nil, "/home/u/.strike/config")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "/home/u/.strike/config") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompileRejectsBadRuleWithSource(t *testing.T) {
	_, err := Compile(nil, []CommandRule{
		{Pattern: "ok *", Class: ClassBuild, Source: "global"},
		{Pattern: "", Class: ClassTest, Source: "project"},
	}, "fallback")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "commands[1]") {
		t.Fatalf("err=%v", err)
	}
}

func TestEffectiveReport(t *testing.T) {
	eff, err := Compile(
		Limits{PoolProcess: 2},
		[]CommandRule{{Pattern: "make *", Class: ClassBuild, Source: "/proj/.strike/config"}},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	rep := eff.Report()
	for _, sub := range []string{
		"in-process",
		"process: 2",
		"build: unlimited",
		"last match wins",
		`make *`,
		"build",
		"/proj/.strike/config",
	} {
		if !strings.Contains(rep, sub) {
			t.Fatalf("report missing %q:\n%s", sub, rep)
		}
	}
}

func TestEffectiveNilSafe(t *testing.T) {
	var e *Effective
	if e.Classify("x") != ClassGeneral {
		t.Fatal()
	}
	if got := e.PoolsForCommand("x"); len(got) != 1 || got[0] != PoolProcess {
		t.Fatalf("%v", got)
	}
	_ = e.Report()
	_ = e.SchedulerConfig()
}

func TestNewFromEffectiveRespectsCapacity(t *testing.T) {
	eff, err := Compile(Limits{PoolBuild: 1}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(eff.SchedulerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	l1, err := s.Acquire(context.Background(), PoolBuild)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := s.Acquire(ctx, PoolBuild)
		errCh <- err
	}()
	waitForWaiting(t, s, PoolBuild, 1)
	cancel()
	if err := <-errCh; err == nil {
		t.Fatal("second acquire should block then cancel")
	}
	l1.Release()
}
