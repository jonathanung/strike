package swebench

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/container"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

func TestContainerRuntimeAvailableMapsEngine(t *testing.T) {
	cli := container.NewCLI("docker")
	cli.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		if len(args) > 0 && args[0] == "info" {
			return "24", "", 0, nil
		}
		return "", "no", 1, nil
	}
	rt := &ContainerRuntime{CLI: cli}
	if err := rt.Available(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerContainerPoolLimitsConcurrency(t *testing.T) {
	// Capacity 1: second instance waits until first finishes; never >1 live create.
	var live atomic.Int32
	var maxLive atomic.Int32
	var creates atomic.Int32

	rt := &fakePoolRT{
		onCreate: func() {
			creates.Add(1)
			n := live.Add(1)
			for {
				cur := maxLive.Load()
				if n <= cur || maxLive.CompareAndSwap(cur, n) {
					break
				}
			}
		},
		onRemove: func() { live.Add(-1) },
	}
	sched, err := scheduler.New(scheduler.Config{Pools: map[string]int{scheduler.PoolContainer: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer sched.Close()

	// Slow materialize so two instances would overlap without the pool.
	r := &Runner{
		RT:    rt,
		Sched: sched,
		Agent: &poolFakeAgent{},
		Grade: &poolFakeGrader{},
		Materialize: func(ctx context.Context, id, host string, pull bool) (MaterializeResult, error) {
			// Create a container under the lease (simulates docker create).
			cid, err := rt.Create(ctx, "img", CreateOpts{Name: id})
			if err != nil {
				return MaterializeResult{}, err
			}
			time.Sleep(30 * time.Millisecond)
			_ = rt.Remove(ctx, cid)
			return MaterializeResult{WorkDir: host, Image: "img"}, nil
		},
		ExtractPatch: func(string) (string, error) { return "diff", nil },
		Now:          func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	cfg := Config{
		Instances: []Instance{
			{InstanceID: "a", ProblemStatement: "a"},
			{InstanceID: "b", ProblemStatement: "b"},
		},
		RunID:  "pool-test",
		OutDir: t.TempDir(),
		Grader: GraderNone,
		DryRun: false,
	}
	rep, err := r.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if maxLive.Load() > 1 {
		t.Fatalf("max concurrent containers = %d, want <=1", maxLive.Load())
	}
	if creates.Load() < 2 {
		t.Fatalf("creates=%d", creates.Load())
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results=%d", len(rep.Results))
	}
}

func TestRunnerContainerPoolCancelBeforeCreate(t *testing.T) {
	var creates atomic.Int32
	rt := &fakePoolRT{onCreate: func() { creates.Add(1) }}
	sched, err := scheduler.New(scheduler.Config{Pools: map[string]int{scheduler.PoolContainer: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer sched.Close()

	// Hold the only slot.
	lease, err := sched.Acquire(context.Background(), scheduler.PoolContainer)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		RT:    rt,
		Sched: sched,
		Agent: &poolFakeAgent{},
		Grade: &poolFakeGrader{},
		Materialize: func(context.Context, string, string, bool) (MaterializeResult, error) {
			t.Fatal("materialize must not run before admission")
			return MaterializeResult{}, errors.New("unreachable")
		},
		Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Run(ctx, Config{
			Instances: []Instance{{InstanceID: "wait", ProblemStatement: "x"}},
			RunID:     "cancel-test",
			OutDir:    t.TempDir(),
			Grader:    GraderNone,
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	lease.Release()
	if creates.Load() != 0 {
		t.Fatalf("created containers before admission: %d", creates.Load())
	}
}

type fakePoolRT struct {
	onCreate func()
	onRemove func()
	n        atomic.Int32
}

func (f *fakePoolRT) Available(context.Context) error    { return nil }
func (f *fakePoolRT) Pull(context.Context, string) error { return nil }
func (f *fakePoolRT) Create(_ context.Context, _ string, _ CreateOpts) (string, error) {
	if f.onCreate != nil {
		f.onCreate()
	}
	id := f.n.Add(1)
	return "cid-" + itoa(int(id)), nil
}
func (f *fakePoolRT) Start(context.Context, string) error                    { return nil }
func (f *fakePoolRT) CopyFrom(context.Context, string, string, string) error { return nil }
func (f *fakePoolRT) CopyTo(context.Context, string, string, string) error   { return nil }
func (f *fakePoolRT) Exec(context.Context, string, []string, ExecOpts) (string, string, int, error) {
	return "", "", 0, nil
}
func (f *fakePoolRT) Remove(_ context.Context, _ string) error {
	if f.onRemove != nil {
		f.onRemove()
	}
	return nil
}

type poolFakeAgent struct{}

func (poolFakeAgent) Run(context.Context, string, string, AgentOpts) (ExecResult, error) {
	return ExecResult{}, nil
}

type poolFakeGrader struct{}

func (poolFakeGrader) Grade(context.Context, Instance, string, string) (GradeResult, error) {
	return GradeResult{Resolved: false, Detail: "fake", Skipped: true}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
