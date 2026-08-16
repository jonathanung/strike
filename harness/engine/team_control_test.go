package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func teamControlLead(t *testing.T, sessionID string) *engine.Engine {
	t.Helper()
	// Children sleep briefly so mailbox/interrupt tests see a live recipient.
	sleepCall := controlToolCall("sleep-1", "sleep", map[string]any{"seconds": 2})
	childProv := newScriptedProvider(
		func() streamStep {
			s := toolCallStep(sleepCall)
			s.match = func(provider.Request) bool { return true }
			return s
		}(),
		func() streamStep {
			s := completedStep("child-done")
			s.match = matchToolResult("sleep-1")
			return s
		}(),
	)
	return engine.New(engine.Options{
		SessionID: sessionID,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore"},
			{Name: "general"},
		},
		Select: func(string) (provider.Provider, string, error) {
			return childProv, "m", nil
		},
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Registry: tool.NewRegistry(
			tool.NewTask(),
			tool.NewAgentMessage(),
			tool.NewAgentBroadcast(),
			tool.NewTeamTask(),
			tool.NewDelegate(),
			tool.NewSleep(),
		),
		WorkDir:       t.TempDir(),
		Rules:         []permission.Ruleset{permission.Defaults()},
		MaxChildDepth: 1,
	})
}

func decodeTeamOp(t *testing.T, typ string, data map[string]any) protocol.Op {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	op, err := (protocol.OpEnvelope{Type: typ, Data: raw}).Decode()
	if err != nil {
		t.Fatalf("decode %s: %v", typ, err)
	}
	return op
}

func submitTeamOp(t *testing.T, eng *engine.Engine, op protocol.Op, timeout time.Duration) protocol.TeamOpOutcome {
	t.Helper()
	reply := make(chan protocol.TeamOpOutcome, 1)
	op = protocol.WithTeamControlReply(op, reply)
	select {
	case eng.Ops() <- op:
	case <-time.After(2 * time.Second):
		t.Fatal("ops channel blocked")
	}
	select {
	case out := <-reply:
		return out
	case <-time.After(timeout):
		t.Fatal("team op timed out")
		return protocol.TeamOpOutcome{}
	}
}

func startTeamControlEngine(t *testing.T, sessionID string) (*engine.Engine, context.CancelFunc) {
	t.Helper()
	eng := teamControlLead(t, sessionID)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	go func() {
		for range eng.Events() {
		}
	}()
	waitTeamLive(t, eng.Team(), sessionID)
	return eng, cancel
}

func TestTeamControlBoardCreateClaimCompleteAndCAS(t *testing.T) {
	const leadID = "lead-tc-board"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	create := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-create-1",
		"title":          "ship feature",
		"body":           "details here",
	}), 5*time.Second)
	if !create.OK || create.TaskID == "" {
		t.Fatalf("create = %+v", create)
	}
	taskID := create.TaskID
	ver := create.Version

	// Idempotent replay returns same task without duplicate.
	replay := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-create-1",
		"title":          "ship feature",
		"body":           "details here",
	}), 5*time.Second)
	if !replay.OK || replay.TaskID != taskID {
		t.Fatalf("replay = %+v want task %s", replay, taskID)
	}

	// Same key different body → conflict.
	clash := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-create-1",
		"title":          "different",
	}), 5*time.Second)
	if clash.OK || clash.Code != protocol.ErrTeamIdempotencyConflict {
		t.Fatalf("clash = %+v", clash)
	}

	claim := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardClaim, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-claim-1",
		"taskId":          taskID,
		"expectedVersion": ver,
	}), 5*time.Second)
	if !claim.OK {
		t.Fatalf("claim = %+v", claim)
	}

	// Stale CAS → conflict.
	stale := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardComplete, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-complete-stale",
		"taskId":          taskID,
		"expectedVersion": ver, // pre-claim version
	}), 5*time.Second)
	if stale.OK || stale.Code != protocol.ErrTeamConflict {
		t.Fatalf("stale complete = %+v", stale)
	}

	done := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardComplete, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-complete-1",
		"taskId":          taskID,
		"expectedVersion": claim.Version,
	}), 5*time.Second)
	if !done.OK {
		t.Fatalf("complete = %+v", done)
	}
}

func TestTeamControlCrossRootDenied(t *testing.T) {
	const leadID = "lead-tc-xroot"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	out := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  "other-root",
		"idempotencyKey": "idem-xroot",
		"title":          "nope",
	}), 5*time.Second)
	if out.OK || out.Code != protocol.ErrTeamCrossRoot {
		t.Fatalf("out = %+v", out)
	}
}

func TestTeamControlMissingIdempotencyKey(t *testing.T) {
	const leadID = "lead-tc-noidem"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	out := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId": leadID,
		"title":         "no key",
	}), 5*time.Second)
	if out.OK || out.Code != protocol.ErrTeamValidation {
		t.Fatalf("out = %+v", out)
	}
}

func TestTeamControlSpawnAndMessage(t *testing.T) {
	const leadID = "lead-tc-spawn"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	spawn := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamSpawn, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-spawn-1",
		"objective":      "explore the codebase briefly",
		"agent":          "explore",
		"name":           "scout",
	}), 30*time.Second)
	if !spawn.OK || spawn.ChildSessionID == "" {
		t.Fatalf("spawn = %+v", spawn)
	}
	childID := spawn.ChildSessionID
	waitTeamLive(t, eng.Team(), leadID, childID)

	// Idempotent spawn replay returns same child.
	again := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamSpawn, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-spawn-1",
		"objective":      "explore the codebase briefly",
		"agent":          "explore",
		"name":           "scout",
	}), 5*time.Second)
	if !again.OK || again.ChildSessionID != childID {
		t.Fatalf("spawn replay = %+v want %s", again, childID)
	}

	msg := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamMessage, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-msg-1",
		"to":             childID,
		"body":           "focus on internal/engine",
		"kind":           "message",
		"urgency":        "normal",
	}), 5*time.Second)
	if !msg.OK {
		t.Fatalf("message = %+v", msg)
	}

	bcast := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBroadcast, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-bcast-1",
		"body":           "stand down",
	}), 5*time.Second)
	if !bcast.OK {
		t.Fatalf("broadcast = %+v", bcast)
	}

	// Interrupt is idempotent whether the child is still running or already terminal.
	intr := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamChildInterrupt, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-intr-1",
		"childSessionId": childID,
		"reason":         "test stop",
	}), 30*time.Second)
	if !intr.OK {
		t.Fatalf("interrupt = %+v", intr)
	}

	intr2 := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamChildInterrupt, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-intr-2",
		"childSessionId": childID,
	}), 10*time.Second)
	if !intr2.OK {
		t.Fatalf("interrupt2 = %+v", intr2)
	}
}

func TestTeamControlTaskTransitionCAS(t *testing.T) {
	const leadID = "lead-tc-deleg"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	// Create a queued delegation via tool path, then transition via human Op.
	tm := eng.Team()
	d, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt:         "queued work",
		Agent:          "build",
		OwnerSessionID: leadID,
		StartState:     protocol.DelegationQueued,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Bad version.
	bad := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamTaskTransition, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-tr-bad",
		"delegationId":    d.ID,
		"expectedVersion": d.Version + 99,
		"toState":         "canceled",
	}), 5*time.Second)
	if bad.OK || bad.Code != protocol.ErrTeamConflict {
		t.Fatalf("bad cas = %+v", bad)
	}

	ok := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamTaskTransition, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-tr-ok",
		"delegationId":    d.ID,
		"expectedVersion": d.Version,
		"toState":         "canceled",
		"reason":          "human cancel",
	}), 5*time.Second)
	if !ok.OK || ok.DelegationID != d.ID {
		t.Fatalf("transition = %+v", ok)
	}

	// completed alias → done
	d2, err := tm.CreateDelegation(engine.CreateDelegationSpec{
		Prompt:         "more work",
		Agent:          "build",
		OwnerSessionID: leadID,
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamTaskTransition, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-tr-done",
		"delegationId":    d2.ID,
		"expectedVersion": d2.Version,
		"toState":         "completed",
	}), 5*time.Second)
	if !done.OK {
		t.Fatalf("completed alias = %+v", done)
	}
	got, _ := tm.GetDelegation(d2.ID)
	if got.State != protocol.DelegationDone {
		t.Fatalf("state=%s", got.State)
	}
}

func TestTeamControlPermissionDeny(t *testing.T) {
	const leadID = "lead-tc-deny"
	childProv := newScriptedProvider(completedStep("idle"))
	denySpawn := permission.Ruleset{
		{Permission: "team.spawn", Pattern: "*", Action: permission.Deny},
	}
	eng := engine.New(engine.Options{
		SessionID: leadID,
		Agents:    []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return childProv, "m", nil
		},
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults(), denySpawn},
		MaxChildDepth:   1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	go func() {
		for range eng.Events() {
		}
	}()
	waitTeamLive(t, eng.Team(), leadID)

	out := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamSpawn, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-deny",
		"objective":      "should fail",
	}), 5*time.Second)
	if out.OK || out.Code != protocol.ErrTeamPermissionDenied {
		t.Fatalf("out = %+v", out)
	}
}

func TestTeamControlNotLeadDepth(t *testing.T) {
	const (
		leadID  = "lead-tc-depth"
		childID = "child-tc-depth"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: childID, ParentSessionID: leadID, Persona: "explore", Depth: 1})
	child := engine.New(engine.Options{
		SessionID:       childID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "explore"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("c")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go child.Run(ctx)
	go func() {
		for range child.Events() {
		}
	}()
	waitTeamLive(t, team, childID)

	out := submitTeamOp(t, child, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"idempotencyKey": "idem-child",
		"title":          "nope",
	}), 5*time.Second)
	if out.OK || out.Code != protocol.ErrTeamNotLead {
		t.Fatalf("out = %+v", out)
	}
}

func TestTeamControlParallelClaimOneWinner(t *testing.T) {
	const leadID = "lead-tc-race"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()

	create := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-race-create",
		"title":          "race me",
	}), 5*time.Second)
	if !create.OK {
		t.Fatalf("create = %+v", create)
	}

	// Two claims with different keys; one should win, one conflict or both ok if same actor.
	// Same lead actor claiming twice is idempotent owner — both OK. Use CAS race instead:
	// two completes with same expectedVersion after claim.
	claim := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardClaim, map[string]any{
		"rootSessionId":   leadID,
		"idempotencyKey":  "idem-race-claim",
		"taskId":          create.TaskID,
		"expectedVersion": create.Version,
	}), 5*time.Second)
	if !claim.OK {
		t.Fatalf("claim = %+v", claim)
	}

	var wg sync.WaitGroup
	results := make([]protocol.TeamOpOutcome, 2)
	keys := []string{"idem-race-complete-a", "idem-race-complete-b"}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardComplete, map[string]any{
				"rootSessionId":   leadID,
				"idempotencyKey":  keys[i],
				"taskId":          create.TaskID,
				"expectedVersion": claim.Version,
			}), 5*time.Second)
		}(i)
	}
	wg.Wait()
	var wins, conflicts int
	for _, r := range results {
		if r.OK {
			wins++
		} else if r.Code == protocol.ErrTeamConflict {
			conflicts++
		} else {
			t.Fatalf("unexpected result %+v", r)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d results=%+v", wins, conflicts, results)
	}
}

func TestTeamControlDissolvedUnavailable(t *testing.T) {
	const leadID = "lead-tc-dissolve"
	eng, cancel := startTeamControlEngine(t, leadID)
	defer cancel()
	eng.Team().Dissolve()

	out := submitTeamOp(t, eng, decodeTeamOp(t, protocol.OpTeamBoardCreate, map[string]any{
		"rootSessionId":  leadID,
		"idempotencyKey": "idem-dissolved",
		"title":          "gone",
	}), 5*time.Second)
	if out.OK || out.Code != protocol.ErrTeamUnavailable {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.Error, "team_unavailable") && out.Code != protocol.ErrTeamUnavailable {
		t.Fatalf("error=%q", out.Error)
	}
}
