package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestDelegationLifecycleCriteriaToReview(t *testing.T) {
	// task with criteria → child completes → lifecycle review + protocol event.
	const parentSession = "lead-deleg"
	prov := newScriptedProvider(
		toolCallStep(mustToolCallJSON(t, "t1", "task", map[string]any{
			"prompt":    "impl-with-criteria",
			"criteria":  []string{"make test"},
			"subscribe": []string{"review", "done"},
		})),
		func() streamStep {
			s := completedStep("child finished")
			s.match = matchUserText("impl-with-criteria")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("t1")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
	)
	reg := tool.NewRegistry(tool.NewTask(), tool.NewTaskStatus(), tool.NewDelegate())
	eng := engine.New(engine.Options{
		SessionID:       parentSession,
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        reg,
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainUntil(t, eng, 12*time.Second, func(evs []protocol.Event) bool {
		var sawChildDone, sawReview bool
		for _, ev := range evs {
			switch e := ev.(type) {
			case protocol.ChildCompleted:
				sawChildDone = true
				if e.DelegationID == "" {
					t.Errorf("ChildCompleted missing DelegationID")
				}
			case protocol.DelegationChanged:
				if e.State == protocol.DelegationReview {
					sawReview = true
				}
			}
		}
		return sawChildDone && sawReview
	})

	var (
		started   int
		reviewEv  *protocol.DelegationChanged
		delegID   string
		childSess string
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ChildStarted:
			started++
			childSess = e.SessionID
		case protocol.DelegationChanged:
			if e.State == protocol.DelegationReview {
				cp := e
				reviewEv = &cp
			}
			if e.ID != "" {
				delegID = e.ID
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", e.Message)
		case protocol.ToolCallEnd:
			if e.CallID == "t1" && e.IsError {
				t.Fatalf("task error: %s", e.Output)
			}
			if e.CallID == "t1" && !strings.Contains(e.Output, "Delegation") {
				t.Errorf("task output missing delegation mention: %s", e.Output)
			}
		}
	}
	if started != 1 {
		t.Fatalf("ChildStarted = %d; events=%v", started, summarizeEvents(events))
	}
	if reviewEv == nil {
		t.Fatalf("missing delegation.changed review; events=%v", summarizeEvents(events))
	}
	if reviewEv.SessionID != childSess {
		t.Errorf("review session = %q want %q", reviewEv.SessionID, childSess)
	}

	tm := eng.Team()
	if tm == nil {
		t.Fatal("nil team")
	}
	d, ok := tm.GetDelegation(delegID)
	if !ok {
		t.Fatalf("delegation %s missing", delegID)
	}
	if d.State != protocol.DelegationReview {
		t.Fatalf("lifecycle = %s, want review", d.State)
	}
	if len(d.Criteria) != 1 || d.Criteria[0] != "make test" {
		t.Fatalf("criteria = %#v", d.Criteria)
	}

	// Transition review → done (verification gate placeholder for #780).
	done, err := tm.TransitionDelegation(d.ID, parentSession, protocol.DelegationDone, "verified", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != protocol.DelegationDone {
		t.Fatalf("done = %s", done.State)
	}
}

func TestDelegationPlainTaskCreatesWorkingLifecycle(t *testing.T) {
	// Backward compatible: plain task still starts a child and tracks lifecycle=working→done.
	const parentSession = "lead-plain"
	prov := newScriptedProvider(
		toolCallStep(mustToolCallJSON(t, "t1", "task", map[string]any{
			"prompt": "plain-task-prompt",
		})),
		func() streamStep {
			s := completedStep("child ok")
			s.match = matchUserText("plain-task-prompt")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("t1")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       parentSession,
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if d, ok := ev.(protocol.DelegationChanged); ok && d.State == protocol.DelegationDone {
				return true
			}
		}
		return false
	})
	var sawWorking, sawDone bool
	for _, ev := range events {
		if d, ok := ev.(protocol.DelegationChanged); ok {
			if d.State == protocol.DelegationWorking {
				sawWorking = true
			}
			if d.State == protocol.DelegationDone {
				sawDone = true
			}
		}
	}
	if !sawWorking || !sawDone {
		t.Fatalf("working=%v done=%v events=%v", sawWorking, sawDone, summarizeEvents(events))
	}
}

func TestDelegationDepsHoldSpawnUntilDone(t *testing.T) {
	tm := engine.NewTeam("L", "build")
	up, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt: "up", OwnerSessionID: "L",
		SessionID: "sess-up", StartState: protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	down, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt: "down", OwnerSessionID: "L", Deps: []string{up.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if down.SpawnPending || down.State != protocol.DelegationQueued {
		t.Fatalf("dependent should be queued without spawn: %+v", down)
	}

	// Complete upstream → dependent becomes spawn-pending.
	if _, ok := tm.BindDelegationOnChildCompleted("sess-up", protocol.ChildStatusCompleted); !ok {
		t.Fatal("bind up")
	}
	got, ok := tm.GetDelegation(down.ID)
	if !ok || !got.SpawnPending {
		t.Fatalf("after up done = %+v", got)
	}
	pending := tm.TakeSpawnPending("L")
	if len(pending) != 1 || pending[0].ID != down.ID {
		t.Fatalf("pending = %+v", pending)
	}

	// Fresh dependent when upstream already done → spawn pending immediately.
	down2, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt: "down2", OwnerSessionID: "L", Deps: []string{up.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !down2.SpawnPending {
		t.Fatalf("expected spawn pending when deps already done: %+v", down2)
	}
}

func TestDelegateIllegalTransitionMessage(t *testing.T) {
	tm := engine.NewTeam("L", "build")
	d, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt: "x", OwnerSessionID: "L", SessionID: "s", StartState: protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tm.TransitionDelegation(d.ID, "L", protocol.DelegationQueued, "", 0)
	if err == nil || !strings.Contains(err.Error(), "illegal transition") {
		t.Fatalf("err = %v", err)
	}
}

func mustToolCallJSON(t *testing.T, id, name string, input map[string]any) provider.ToolCall {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return provider.ToolCall{ID: id, Name: name, Args: raw}
}
