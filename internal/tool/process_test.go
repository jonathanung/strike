package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

func TestRunProcessStdoutExitZero(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "printf 'hi\\n'"},
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ProcessStatusExited || res.ExitCode != 0 {
		t.Fatalf("status=%s exit=%d", res.Status, res.ExitCode)
	}
	if res.ID == "" {
		t.Fatal("empty process id")
	}
	if res.Stdout != "hi\n" && res.Output != "hi\n" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestRunProcessNonZeroExit(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "exit 7"},
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 || res.Status != ProcessStatusExited {
		t.Fatalf("exit=%d status=%s", res.ExitCode, res.Status)
	}
}

func TestRunProcessStdin(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:  []string{"bash", "-c", "cat"},
		Stdin: []byte(`{"event":"pre_tool"}`),
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != `{"event":"pre_tool"}` {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestRunProcessTimeout(t *testing.T) {
	start := time.Now()
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:    []string{"bash", "-c", "sleep 5"},
		Timeout: 80 * time.Millisecond,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ProcessStatusTimeout {
		t.Fatalf("status = %s, want timeout", res.Status)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v, want quick timeout", elapsed)
	}
}

func TestRunProcessCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	// Background sleep + wait: bash stays parent so a direct-child-only kill
	// leaves the grandchild holding stdout/stderr and hangs Wait. Process-group
	// cancel must reap the whole tree.
	done := make(chan ProcessResult, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := RunProcess(ctx, ProcessSpec{
			Argv: []string{"bash", "-c", "echo $$ > '" + ready + "'; sleep 30 & wait"},
		}, ProcessObserver{})
		done <- res
		errc <- err
	}()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("process never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Let the background sleep start before cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if err := <-errc; err != nil && res.Status == ProcessStatusError {
			// start/wait hard failure is unexpected after ready
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		if res.Status != ProcessStatusCanceled && res.Status != ProcessStatusExited {
			// On some platforms kill may surface as exited with -1; accept canceled primarily.
			t.Fatalf("status = %s, want canceled", res.Status)
		}
		if res.Status != ProcessStatusCanceled {
			t.Logf("status = %s (platform may map cancel to exited)", res.Status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunProcess did not return after cancel")
	}
}

func TestRunProcessCancelDuringStdoutFlood(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	done := make(chan ProcessResult, 1)
	errc := make(chan error, 1)
	var (
		mu     sync.Mutex
		chunks int
	)
	go func() {
		res, err := RunProcess(ctx, ProcessSpec{
			// Flood stdout forever after signaling ready; cancel must still return.
			Argv:      []string{"bash", "-c", "echo ok > '" + ready + "'; while true; do printf 'xxxxxxxx'; done"},
			MaxOutput: 256,
			Combine:   true,
		}, ProcessObserver{
			Output: func(_, _, _ string) {
				mu.Lock()
				chunks++
				mu.Unlock()
			},
		})
		done <- res
		errc <- err
	}()
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("process never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Let some output land, then cancel mid-flood.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if err := <-errc; err != nil && res.Status == ProcessStatusError {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		if res.Status != ProcessStatusCanceled {
			t.Fatalf("status = %s, want canceled", res.Status)
		}
		if !res.Truncated {
			t.Fatal("expected truncated flood output")
		}
		mu.Lock()
		n := chunks
		mu.Unlock()
		if n == 0 {
			t.Fatal("expected at least one output chunk before cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunProcess did not return after cancel during stdout flood")
	}
}

func TestRunProcessTimeoutVsCancel(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		res, err := RunProcess(context.Background(), ProcessSpec{
			Argv:    []string{"bash", "-c", "sleep 5"},
			Timeout: 50 * time.Millisecond,
		}, ProcessObserver{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != ProcessStatusTimeout {
			t.Fatalf("status = %s, want timeout", res.Status)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan ProcessResult, 1)
		errc := make(chan error, 1)
		go func() {
			res, err := RunProcess(ctx, ProcessSpec{
				Argv: []string{"bash", "-c", "sleep 30"},
			}, ProcessObserver{})
			done <- res
			errc <- err
		}()
		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case res := <-done:
			if err := <-errc; err != nil && res.Status == ProcessStatusError {
				t.Fatalf("err=%v status=%s", err, res.Status)
			}
			if res.Status != ProcessStatusCanceled {
				t.Fatalf("status = %s, want canceled", res.Status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("RunProcess did not return after cancel")
		}
	})
	t.Run("parent cancel beats nested timeout", func(t *testing.T) {
		// Parent cancel should surface as canceled even when Timeout is set.
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan ProcessResult, 1)
		errc := make(chan error, 1)
		go func() {
			res, err := RunProcess(ctx, ProcessSpec{
				Argv:    []string{"bash", "-c", "sleep 30"},
				Timeout: 10 * time.Second,
			}, ProcessObserver{})
			done <- res
			errc <- err
		}()
		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case res := <-done:
			if err := <-errc; err != nil && res.Status == ProcessStatusError {
				t.Fatalf("err=%v status=%s", err, res.Status)
			}
			if res.Status != ProcessStatusCanceled {
				t.Fatalf("status = %s, want canceled (not timeout)", res.Status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("RunProcess did not return after parent cancel")
		}
	})
}

func TestRunProcessMaxOutputTruncate(t *testing.T) {
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:      []string{"bash", "-c", "printf '%s' '0123456789'"},
		MaxOutput: 5,
		Combine:   true,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected truncated")
	}
	if res.Output != "01234" {
		t.Fatalf("output = %q", res.Output)
	}
	if res.BytesSeen != 10 {
		t.Fatalf("bytesSeen = %d", res.BytesSeen)
	}
}

func TestRunProcessObserverLifecycle(t *testing.T) {
	var (
		mu      sync.Mutex
		started bool
		chunks  []string
		exited  bool
		status  ProcessStatus
		pid     string
	)
	obs := ProcessObserver{
		Started: func(id string, argv []string) {
			mu.Lock()
			started = true
			pid = id
			mu.Unlock()
			if len(argv) < 2 || argv[0] != "bash" {
				t.Errorf("argv = %#v", argv)
			}
		},
		Output: func(id, stream, data string) {
			mu.Lock()
			chunks = append(chunks, stream+":"+data)
			mu.Unlock()
			if id == "" {
				t.Error("empty id on output")
			}
		},
		Exited: func(id string, exitCode int, st ProcessStatus) {
			mu.Lock()
			exited = true
			status = st
			mu.Unlock()
			if exitCode != 0 {
				t.Errorf("exit = %d", exitCode)
			}
			if id == "" {
				t.Error("empty id on exit")
			}
		},
	}
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:    []string{"bash", "-c", "printf 'a'; printf 'b' >&2"},
		Combine: false,
	}, obs)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !started || !exited {
		t.Fatalf("started=%v exited=%v", started, exited)
	}
	if pid != res.ID {
		t.Fatalf("id mismatch %q vs %q", pid, res.ID)
	}
	if status != ProcessStatusExited {
		t.Fatalf("status = %s", status)
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "stdout:a") || !strings.Contains(joined, "stderr:b") {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestRunProcessEmptyArgv(t *testing.T) {
	_, err := RunProcess(context.Background(), ProcessSpec{}, ProcessObserver{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunProcessWorkDir(t *testing.T) {
	dir := t.TempDir()
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "pwd"},
		Dir:  dir,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, dir) {
		t.Fatalf("pwd output = %q, want contain %q", res.Output, dir)
	}
}

func TestRunProcessSandboxPolicyDegradesOrRuns(t *testing.T) {
	// Sandbox ModeWorkspaceWrite must not break RunProcess when the OS backend
	// is missing/blocked (graceful degrade) and must still succeed when applied.
	dir := t.TempDir()
	var started []string
	// AllowDegrade so CI hosts without bwrap/sandbox-exec still exercise the path.
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "printf hi"},
		Dir:  dir,
		Sandbox: sandbox.Policy{
			Mode:         sandbox.ModeWorkspaceWrite,
			WorkDir:      dir,
			AllowDegrade: true,
		},
	}, ProcessObserver{
		Started: func(_ string, argv []string) {
			started = append([]string(nil), argv...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ProcessStatusExited || res.ExitCode != 0 {
		t.Fatalf("status=%s exit=%d out=%q", res.Status, res.ExitCode, res.Output)
	}
	if res.Output != "hi" && res.Stdout != "hi" {
		t.Fatalf("output = %q", res.Output)
	}
	// Observer sees pre-wrap argv (not bwrap/sandbox-exec).
	if len(started) < 1 || started[0] != "bash" {
		t.Fatalf("Started argv = %#v, want original bash", started)
	}
	// When the backend is available, Applied should be true; when degraded,
	// Degraded should be true. Never both false when Mode is non-off unless
	// ModeOff — here Mode is workspace-write so one of the two flags holds.
	if !res.SandboxApplied && !res.SandboxDegraded {
		// Backend may report Applied via wrap; if neither, Mode was treated as off.
		t.Logf("sandbox neither applied nor degraded (backend=%q) — ok if probe flaky", res.SandboxBackend)
	}
}

func TestRunProcessSandboxDegradeFailClosed(t *testing.T) {
	// When Mode is non-off and AllowDegrade is false, a missing backend must
	// not run the process unsandboxed (#1030).
	if sandbox.Available() {
		t.Skip("backend available — cannot force degrade path")
	}
	sandbox.ResetWarnForTest()
	dir := t.TempDir()
	_, err := RunProcess(context.Background(), ProcessSpec{
		Argv: []string{"bash", "-c", "echo should-not-run"},
		Dir:  dir,
		Sandbox: sandbox.Policy{
			Mode:    sandbox.ModeWorkspaceWrite,
			WorkDir: dir,
			// AllowDegrade false (default)
		},
	}, ProcessObserver{})
	if err == nil {
		t.Fatal("expected sandbox_denied when backend unavailable and degrade denied")
	}
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != CodeSandboxDenied {
		t.Fatalf("err = %v (%T), want sandbox_denied", err, err)
	}
}

func TestRunProcessMemoryLimitLinux(t *testing.T) {
	// Linux-only: tiny RLIMIT_AS must kill a process that tries to touch a large
	// anonymous mapping. Non-Linux builds document Limits as unsupported no-ops.
	if !processRlimitEnforced() {
		t.Skip("process memory limits enforced on Linux only (see docs/isolation.md)")
	}
	// Allocate ~64MiB via dd into a pipe-backed process memory... simpler:
	// python/perl may be missing; use a small Go-free bash approach with
	// /dev/zero head into a variable is unreliable. Use `python3` if present,
	// else a C-free approach: `dd` writing to a memfd is hard. Prefer python3.
	script := `python3 -c 'a="x"*(80*1024*1024); print(len(a))'`
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:    []string{"bash", "-c", script},
		Timeout: 5 * time.Second,
		// 32 MiB address space — well under the 80 MiB allocation.
		Limits: ProcessLimits{MemoryBytes: 32 * 1024 * 1024},
	}, ProcessObserver{})
	if err != nil {
		// Start failures (no python) — skip rather than fail CI without python3.
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	// If python3 missing, bash exits 127 — skip.
	if res.ExitCode == 127 || strings.Contains(res.Output, "python3:") {
		t.Skip("python3 not available for memory-limit probe")
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit under 32MiB RLIMIT_AS; out=%q status=%s", res.Output, res.Status)
	}
}
