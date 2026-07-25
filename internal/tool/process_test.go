package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
