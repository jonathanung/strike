package external_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/harness/external"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestPersistentSequentialReuse(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, path := newPersistentFixture(t, "persistent-echo", &starts, external.WorkerOptions{
		MaxConcurrent: 1,
		IdleTimeout:   -1,
		MaxRestarts:   3,
	})
	defer func() { _ = closeFn() }()

	for i := 0; i < 3; i++ {
		text := fmt.Sprintf("run-%d", i)
		result, err := h(harness.Input{
			Context: context.Background(),
			Request: provider.Request{Model: text},
		}, harness.Provider{}, nil)
		if err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
		if result.Text != text {
			t.Fatalf("invoke %d text = %q, want %q", i, result.Text, text)
		}
	}
	requireStarts(t, path, 1)
}

func TestPersistentConcurrentInvocations(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, path := newPersistentFixture(t, "persistent-echo", &starts, external.WorkerOptions{
		MaxConcurrent: 3,
		IdleTimeout:   -1,
		MaxRestarts:   3,
	})
	defer func() { _ = closeFn() }()

	const n = 3
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := fmt.Sprintf("c-%d", i)
			// Hold the worker briefly so invocations overlap.
			result, err := h(harness.Input{
				Context: context.Background(),
				Request: provider.Request{
					Model:    text,
					System:   "slow",
					Messages: []provider.Message{{Role: provider.RoleUser, Text: "x"}},
				},
			}, harness.Provider{}, nil)
			if err != nil {
				errs <- err
				return
			}
			if result.Text != text {
				errs <- fmt.Errorf("text = %q, want %q", result.Text, text)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	requireStarts(t, path, 1)
}

func TestPersistentCancelIsolatesInvocation(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, _ := newPersistentFixture(t, "persistent-block", &starts, external.WorkerOptions{
		MaxConcurrent: 2,
		IdleTimeout:   -1,
		MaxRestarts:   3,
	})
	defer func() { _ = closeFn() }()

	ctxA, cancelA := context.WithCancel(context.Background())
	errA := make(chan error, 1)
	go func() {
		_, err := h(harness.Input{
			Context: ctxA,
			Request: provider.Request{Model: "block-a"},
		}, harness.Provider{}, nil)
		errA <- err
	}()

	// Let A start blocking inside the worker.
	time.Sleep(100 * time.Millisecond)
	cancelA()

	select {
	case err := <-errA:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("A err = %v, want canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("A cancel timed out")
	}

	// B should still succeed on the same or restarted worker.
	result, err := h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "ok-b", System: "quick"},
	}, harness.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ok-b" {
		t.Fatalf("B result = %#v", result)
	}
}

func TestPersistentCrashRecoveryAndDisable(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, _ := newPersistentFixture(t, "persistent-crash", &starts, external.WorkerOptions{
		MaxConcurrent: 1,
		IdleTimeout:   -1,
		MaxRestarts:   1, // one restart after first crash, then disable
	})
	defer func() { _ = closeFn() }()

	// First invoke crashes the worker immediately.
	_, err := h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "boom"},
	}, harness.Provider{}, nil)
	if err == nil {
		t.Fatal("expected crash error")
	}

	// Second invoke may start a new process (restart budget).
	_, err = h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "boom"},
	}, harness.Provider{}, nil)
	if err == nil {
		t.Fatal("expected second crash error")
	}

	// Exhausted restarts → disabled.
	_, err = h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "boom"},
	}, harness.Provider{}, nil)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v, want disabled", err)
	}
}

func TestPersistentIdleShutdown(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, path := newPersistentFixture(t, "persistent-echo", &starts, external.WorkerOptions{
		MaxConcurrent: 1,
		IdleTimeout:   80 * time.Millisecond,
		MaxRestarts:   5,
	})
	defer func() { _ = closeFn() }()

	result, err := h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "first"},
	}, harness.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "first" {
		t.Fatalf("result = %#v", result)
	}
	requireStarts(t, path, 1)

	time.Sleep(200 * time.Millisecond)

	result, err = h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "second"},
	}, harness.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "second" {
		t.Fatalf("result = %#v", result)
	}
	if got := readStartCount(path); got < 2 {
		t.Fatalf("starts after idle = %d, want >= 2", got)
	}
}

func TestPersistentClose(t *testing.T) {
	var starts atomic.Int32
	h, closeFn, _ := newPersistentFixture(t, "persistent-echo", &starts, external.WorkerOptions{
		MaxConcurrent: 1,
		IdleTimeout:   -1,
		MaxRestarts:   3,
	})
	_, err := h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "x"},
	}, harness.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	_, err = h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "y"},
	}, harness.Provider{}, nil)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("err = %v, want closed", err)
	}
}

func TestPersistentGoSDKWorker(t *testing.T) {
	var starts atomic.Int32
	path := startCountFile(t, &starts)
	adapter, err := external.Command(external.Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHarnessHelperProcess"},
		Env: map[string]string{
			"GO_WANT_HARNESS_HELPER":      "go-sdk-worker",
			"GO_HARNESS_START_COUNT_FILE": path,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, closeFn, err := external.NewPersistent("sdk-worker", adapter, external.WorkerOptions{
		MaxConcurrent: 1,
		IdleTimeout:   -1,
		MaxRestarts:   3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeFn() }()

	for i := 0; i < 2; i++ {
		result, err := h(harness.Input{
			Context: context.Background(),
			Request: provider.Request{Model: fmt.Sprintf("m-%d", i)},
		}, harness.Provider{
			Call: func(req provider.Request) (harness.ModelResponse, error) {
				return harness.ModelResponse{Text: "sdk:" + req.Model, StopReason: "end_turn"}, nil
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.Text, "sdk:m-") {
			t.Fatalf("result = %#v", result)
		}
	}
	requireStarts(t, path, 1)
}

func TestOneshotUnchangedDefault(t *testing.T) {
	// Ensure New (oneshot) still starts a process per call via existing helper.
	var starts atomic.Int32
	path := startCountFile(t, &starts)

	run := func() {
		adapter, err := external.Command(external.Config{
			Command: os.Args[0],
			Args:    []string{"-test.run=TestHarnessHelperProcess"},
			Env: map[string]string{
				"GO_WANT_HARNESS_HELPER":      "count-start",
				"GO_HARNESS_START_COUNT_FILE": path,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		h, err := external.New("oneshot", adapter)
		if err != nil {
			t.Fatal(err)
		}
		result, err := h(harness.Input{
			Context: context.Background(),
			Request: provider.Request{Model: "o"},
		}, harness.Provider{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != "o" {
			t.Fatalf("result = %#v", result)
		}
	}
	run()
	run()
	requireStarts(t, path, 2)
}

func newPersistentFixture(t *testing.T, mode string, starts *atomic.Int32, opts external.WorkerOptions) (harness.Func, func() error, string) {
	t.Helper()
	path := startCountFile(t, starts)
	adapter, err := external.Command(external.Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHarnessHelperProcess"},
		Env: map[string]string{
			"GO_WANT_HARNESS_HELPER":      mode,
			"GO_HARNESS_START_COUNT_FILE": path,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, closeFn, err := external.NewPersistent("fixture", adapter, opts)
	if err != nil {
		t.Fatal(err)
	}
	return h, closeFn, path
}

func startCountFile(t *testing.T, starts *atomic.Int32) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/starts"
	// Keep atomic in sync for tests that read starts.Load(); also use readStartCount.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
				n := readStartCount(path)
				for {
					cur := starts.Load()
					if int32(n) <= cur || starts.CompareAndSwap(cur, int32(n)) {
						break
					}
				}
			}
		}
	}()
	return path
}

func readStartCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "1")
}

func requireStarts(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readStartCount(path) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process starts = %d, want %d", readStartCount(path), want)
}
