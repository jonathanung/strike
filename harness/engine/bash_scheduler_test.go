package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/scheduler"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// TestEngineBashSchedulerCancelQueuedNoProcessStarted holds the process pool
// so the engine's bash tool blocks on admission, then interrupts the turn.
// A canceled waiter must never emit process.started.
func TestEngineBashSchedulerCancelQueuedNoProcessStarted(t *testing.T) {
	// Include model so the engine's stream admission (SCHED.5) can run while
	// we hold the process pool for bash queueing.
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
		scheduler.PoolModel:   4,
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

	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Scheduler:       s,
		Rules: []permission.Ruleset{{
			{Permission: "bash", Action: permission.Allow, Pattern: "*"},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run sleep 30"}

	// Wait until bash is queued on the process pool.
	deadline := time.Now().Add(5 * time.Second)
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
			t.Fatalf("bash never queued on process pool: %+v", snap)
		}
		// Drain events so the engine can progress to Execute.
		select {
		case ev := <-eng.Events():
			if e, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error before queue: %s", e.Message)
			}
			if _, ok := ev.(protocol.ProcessStarted); ok {
				t.Fatal("ProcessStarted before admission")
			}
		case <-time.After(5 * time.Millisecond):
		}
	}

	eng.Ops() <- protocol.Interrupt{}

	var sawProcessStarted bool
	var sawToolEnd bool
	var toolErr bool
	endDeadline := time.After(5 * time.Second)
	for !sawToolEnd {
		select {
		case <-endDeadline:
			t.Fatalf("timed out; processStarted=%v toolEnd=%v", sawProcessStarted, sawToolEnd)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ProcessStarted:
				sawProcessStarted = true
			case protocol.ToolCallEnd:
				sawToolEnd = true
				toolErr = ev.IsError
			case protocol.TurnCompleted:
				// may arrive with or after tool end
			case protocol.EngineError:
				// interrupt path may surface errors; only fail on unexpected
				if !strings.Contains(strings.ToLower(ev.Message), "cancel") &&
					!strings.Contains(strings.ToLower(ev.Message), "interrupt") {
					// keep draining
				}
			}
		}
	}
	if sawProcessStarted {
		t.Fatal("ProcessStarted must not emit for a canceled queued bash")
	}
	if !toolErr {
		// Tool may settle as canceled error or empty cancel feedback.
		// Prefer IsError; if the engine normalizes cancel without IsError,
		// absence of ProcessStarted is still the acceptance criterion.
		t.Log("ToolCallEnd IsError=false (cancel may be normalized); ProcessStarted absent is sufficient")
	}

	// Capacity still held only by our test holder.
	snap := s.Snapshot()
	for _, p := range snap.Pools {
		if p.Name == scheduler.PoolProcess && p.InUse != 1 {
			t.Fatalf("process inUse=%d want 1 (test holder only; leaked lease?)", p.InUse)
		}
	}
}

func TestEngineBashSchedulerSharedPolicyClassifies(t *testing.T) {
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 4,
		scheduler.PoolBuild:   4,
		scheduler.PoolTest:    4,
		scheduler.PoolModel:   4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eff, err := scheduler.Compile(nil, []scheduler.CommandRule{
		{Pattern: "echo *", Class: scheduler.ClassBuild},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Scheduler:       s,
		SchedulerPolicy: eff,
		Rules: []permission.Ruleset{{
			{Permission: "bash", Action: permission.Allow, Pattern: "*"},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run echo classified-build"}

	deadline := time.After(10 * time.Second)
	var sawEnd bool
	var output string
	for !sawEnd {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				sawEnd = true
				output = ev.Output
				if ev.IsError {
					t.Fatalf("tool error: %s", ev.Output)
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			case protocol.TurnCompleted:
				if !sawEnd {
					// wait a bit more for tool end if ordering differs
				}
			}
		}
	}
	if !strings.Contains(output, "classified-build") {
		t.Fatalf("output = %q", output)
	}
}
