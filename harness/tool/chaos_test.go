package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/fault"
)

// Chaos: process.after_start kill — RunProcess returns canceled, does not hang.
func TestChaosProcessAfterStartKill(t *testing.T) {
	t.Cleanup(fault.Reset)
	disarm := fault.Arm(fault.ProcessAfterStart, 1, nil)
	t.Cleanup(disarm)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := RunProcess(ctx, ProcessSpec{
		Argv: []string{"bash", "-c", "sleep 30"},
	}, ProcessObserver{})
	if err != nil {
		t.Fatalf("RunProcess err = %v (want nil with canceled status)", err)
	}
	if res.Status != ProcessStatusCanceled {
		t.Fatalf("status = %s, want canceled", res.Status)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v, want quick kill", elapsed)
	}
}

// Chaos: cancel + process.after_start — either path yields a terminal status
// without hanging (race-friendly).
func TestChaosCancelAndProcessInject(t *testing.T) {
	t.Cleanup(fault.Reset)
	for i := 0; i < 4; i++ {
		disarm := fault.Arm(fault.ProcessAfterStart, 1, nil)
		ctx, cancel := context.WithCancel(context.Background())
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		done := make(chan ProcessResult, 1)
		errc := make(chan error, 1)
		go func() {
			res, err := RunProcess(ctx, ProcessSpec{
				Argv: []string{"bash", "-c", "echo ok > '" + ready + "'; sleep 30"},
			}, ProcessObserver{})
			done <- res
			errc <- err
		}()
		go func() {
			time.Sleep(time.Duration(i) * 5 * time.Millisecond)
			cancel()
		}()
		select {
		case res := <-done:
			_ = <-errc
			disarm()
			switch res.Status {
			case ProcessStatusCanceled, ProcessStatusExited, ProcessStatusError:
			default:
				t.Fatalf("iter %d status = %s", i, res.Status)
			}
		case <-time.After(5 * time.Second):
			disarm()
			cancel()
			t.Fatalf("iter %d hung", i)
		}
		_, _ = os.Stat(ready)
	}
}

// Chaos: bash tool maps process.after_start kill to ErrorCodeCanceled.
func TestChaosBashKillDuringRun(t *testing.T) {
	t.Cleanup(fault.Reset)
	disarm := fault.Arm(fault.ProcessAfterStart, 1, nil)
	t.Cleanup(disarm)

	b := NewBash()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tc := &Context{
		WorkDir:     t.TempDir(),
		SandboxMode: "off",
		Ask:         func(context.Context, AskRequest) error { return nil },
	}
	res, err := b.Execute(ctx, []byte(`{"command":"sleep 30"}`), tc)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if res.ErrorCode != ErrorCodeCanceled {
		t.Fatalf("ErrorCode = %q, want %q (output=%q)", res.ErrorCode, ErrorCodeCanceled, res.Output)
	}
}
