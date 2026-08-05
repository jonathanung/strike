package tool

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/scheduler"
)

func TestBashPoolsForCommand(t *testing.T) {
	if got := bashPoolsForCommand(nil, "ls"); len(got) != 1 || got[0] != scheduler.PoolProcess {
		t.Fatalf("nil tc = %v", got)
	}
	if got := bashPoolsForCommand(&Context{}, "ls"); len(got) != 1 || got[0] != scheduler.PoolProcess {
		t.Fatalf("no policy = %v", got)
	}

	// Last-match-wins: broader build rule first, then test override.
	eff, err := scheduler.Compile(nil, []scheduler.CommandRule{
		{Pattern: "go *", Class: scheduler.ClassBuild},
		{Pattern: "go test *", Class: scheduler.ClassTest},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	tc := &Context{SchedulerPolicy: eff}
	got := bashPoolsForCommand(tc, "go test ./...")
	if len(got) != 2 || got[0] != scheduler.PoolProcess || got[1] != scheduler.PoolTest {
		t.Fatalf("test class = %v", got)
	}
	got = bashPoolsForCommand(tc, "go build .")
	if len(got) != 2 || got[0] != scheduler.PoolBuild || got[1] != scheduler.PoolProcess {
		t.Fatalf("build class = %v", got)
	}
	got = bashPoolsForCommand(tc, "echo hi")
	if len(got) != 1 || got[0] != scheduler.PoolProcess {
		t.Fatalf("general = %v", got)
	}
}

func TestAcquireBashLeaseNilScheduler(t *testing.T) {
	lease, err := acquireBashLease(context.Background(), nil, "echo")
	if err != nil || lease != nil {
		t.Fatalf("nil tc: lease=%v err=%v", lease, err)
	}
	lease, err = acquireBashLease(context.Background(), &Context{}, "echo")
	if err != nil || lease != nil {
		t.Fatalf("nil scheduler: lease=%v err=%v", lease, err)
	}
	// nil lease Release is safe
	lease.Release()
}

func TestBashNilSchedulerPreservesBehavior(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	tc := allowAll(t.TempDir())
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo unlimited-ok",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output == "" || res.Title != "echo unlimited-ok" {
		t.Fatalf("result = %+v", res)
	}
}

func TestBashAcquiresAndReleasesProcessPool(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tc := allowAll(t.TempDir())
	tc.Scheduler = s
	tc.SandboxMode = "off"

	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo held",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output == "" {
		t.Fatal("empty output")
	}
	// Capacity must be free for a subsequent acquire.
	lease, err := s.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatalf("post-release acquire: %v", err)
	}
	lease.Release()
}

func TestBashClassifiedCommandAcquiresMultiPool(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 2,
		scheduler.PoolBuild:   1,
		scheduler.PoolTest:    1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eff, err := scheduler.Compile(nil, []scheduler.CommandRule{
		{Pattern: "true", Class: scheduler.ClassBuild},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	// Hold the sole build slot so bash must wait on multi-pool acquire.
	holder, err := s.Acquire(context.Background(), scheduler.PoolBuild, scheduler.PoolProcess)
	if err != nil {
		t.Fatal(err)
	}

	tc := allowAll(t.TempDir())
	tc.Scheduler = s
	tc.SchedulerPolicy = eff
	tc.SandboxMode = "off"

	started := make(chan struct{})
	var startedOnce sync.Once
	tc.Process = ProcessObserver{
		Started: func(string, []string) {
			startedOnce.Do(func() { close(started) })
		},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
			"command": "true",
		}), tc)
		errCh <- err
	}()

	// While build is held, bash must not start a process.
	select {
	case <-started:
		t.Fatal("process started while build pool exhausted")
	case <-time.After(50 * time.Millisecond):
	}

	// Confirm a waiter is queued on build.
	snap := s.Snapshot()
	var buildWaiting int
	for _, p := range snap.Pools {
		if p.Name == scheduler.PoolBuild {
			buildWaiting = p.Waiting
		}
	}
	if buildWaiting < 1 {
		t.Fatalf("expected build waiter, snapshot=%+v", snap)
	}

	holder.Release()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bash did not finish after build release")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("process never started after admission")
	}
}

func TestBashCancelWhileQueuedNeverStartsProcess(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	tc := allowAll(t.TempDir())
	tc.Scheduler = s
	tc.SandboxMode = "off"

	var started atomic.Int32
	tc.Process = ProcessObserver{
		Started: func(string, []string) {
			started.Add(1)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := NewBash().Execute(ctx, mustJSON(t, map[string]any{
			"command": "echo should-not-run",
		}), tc)
		errCh <- err
	}()

	// Wait until queued.
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := s.Snapshot()
		waiting := 0
		for _, p := range snap.Pools {
			if p.Name == scheduler.PoolProcess {
				waiting = p.Waiting
			}
		}
		if waiting >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never queued: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("want cancel error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after cancel")
	}
	if started.Load() != 0 {
		t.Fatalf("process started %d times; canceled queue must not start OS process", started.Load())
	}
}

func TestBashConcurrentProcessLimit(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var peak atomic.Int32
	var live atomic.Int32
	dir := t.TempDir()

	runOne := func() error {
		tc := allowAll(dir)
		tc.Scheduler = s
		tc.SandboxMode = "off"
		tc.Process = ProcessObserver{
			Started: func(string, []string) {
				n := live.Add(1)
				for {
					cur := peak.Load()
					if n <= cur || peak.CompareAndSwap(cur, n) {
						break
					}
				}
			},
			Exited: func(string, int, ProcessStatus) {
				live.Add(-1)
			},
		}
		_, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
			"command": "sleep 0.15",
		}), tc)
		return err
	}

	const n = 6
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errCh <- runOne() }()
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrent processes = %d, want <= 2", p)
	}
	if p := peak.Load(); p < 1 {
		t.Fatal("expected at least one process")
	}
}

func TestBashTimeoutStartsAfterAdmission(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatal(err)
	}

	tc := allowAll(t.TempDir())
	tc.Scheduler = s
	tc.SandboxMode = "off"

	errCh := make(chan error, 1)
	go func() {
		// Short timeout: if it started during the queue wait (~150ms hold),
		// the command would time out before finishing.
		_, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
			"command":   "echo after-wait",
			"timeoutMs": 200,
		}), tc)
		errCh <- err
	}()

	// Hold longer than timeoutMs so a premature timer would fire.
	time.Sleep(150 * time.Millisecond)
	holder.Release()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("command should succeed when timeout starts after admission: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestBashReleasesLeaseOnCommandFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tc := allowAll(t.TempDir())
	tc.Scheduler = s
	tc.SandboxMode = "off"

	// Non-zero exit is not an Execute error; lease must still release.
	_, err = NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "exit 7",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatalf("capacity leaked after nonzero exit: %v", err)
	}
	lease.Release()
}
