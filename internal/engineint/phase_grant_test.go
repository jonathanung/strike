package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// widenBashWorkflow is a two-phase workflow whose second phase allows bash
// (widening a config/agent deny). Fingerprint is set via annotate.
func widenBashWorkflow() engine.Workflow {
	w := engine.Workflow{
		SchemaVersion: engine.WorkflowSchemaVersion,
		Name:          "widen-bash",
		Description:   "test widening",
		Phases: []engine.Phase{
			{
				Name:        "locked",
				Description: "no widen",
				Agent:       "build",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
				},
				Exit: engine.ExitGate{Type: engine.GateAgent},
			},
			{
				Name:        "open-bash",
				Description: "widens bash",
				Agent:       "build",
				Permissions: permission.Ruleset{
					{Permission: "bash", Pattern: "*", Action: permission.Allow},
				},
				Exit: engine.ExitGate{Type: engine.GateAgent},
			},
		},
	}
	w.Fingerprint = "fp-" + w.Name
	return w
}

func TestPhaseWideningRequiresApproval(t *testing.T) {
	w := widenBashWorkflow()
	// Enter open-bash directly via a custom single-phase entry by advancing.
	// Start on locked (deny write only — no widen), then phase_done → open-bash.
	doneArgs, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "pd1", Name: "phase_done", Args: doneArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("after advance"),
	)
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-widen-ask",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewPhaseDone()),
		Agents:          []engine.Agent{{Name: "build"}},
		Workflows:       []engine.Workflow{w},
		Rules:           []permission.Ruleset{permission.Defaults(), denyBash},
		InitialAutonomy: protocol.AutonomyAgent,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	// Force enter locked phase (no widening).
	// Use SelectAgent path is awkward; enter via enter_plan-like by restoring.
	// Instead: seed InitialPhase and re-run is heavy. Call through ops isn't exposed.
	// Use a provider turn that only advances after we inject phase via resume opts.
	cancel()

	eng = engine.New(engine.Options{
		BuildDiagnostic:      enginebind.Diagnostic(),
		SessionID:            "phase-widen-ask-2",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(tools.NewPhaseDone()),
		Agents:               []engine.Agent{{Name: "build"}},
		Workflows:            []engine.Workflow{w},
		Rules:                []permission.Ruleset{permission.Defaults(), denyBash},
		InitialAutonomy:      protocol.AutonomyAgent,
		InitialPhaseWorkflow: w.Name,
		InitialPhaseIndex:    0,
	})
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "locked"
	})

	eng.Ops() <- protocol.UserInput{Text: "advance"}
	var sawGrantQ bool
	var sawOpen bool
	var endOK bool
	deadline := time.After(10 * time.Second)
	for !endOK {
		select {
		case <-deadline:
			t.Fatalf("timeout grantQ=%v open=%v", sawGrantQ, sawOpen)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				if len(e.Questions) > 0 && e.Questions[0].ID == "phase_grant" {
					sawGrantQ = true
					eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
				} else {
					// unexpected — answer yes anyway
					eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
				}
			case protocol.PhaseGrantApproved:
				if e.Phase != "open-bash" || len(e.Grants) == 0 {
					t.Fatalf("grant approved = %#v", e)
				}
				if e.Grants[0].Permission != "bash" || e.Grants[0].Action != "allow" {
					t.Fatalf("grants = %#v", e.Grants)
				}
				if e.Auto {
					t.Fatal("interactive approval must not set Auto")
				}
			case protocol.PhaseChanged:
				if e.Phase == "open-bash" {
					sawOpen = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "pd1" && !e.IsError {
					endOK = true
				}
			}
		}
	}
	if !sawGrantQ {
		t.Fatal("expected phase_grant question")
	}
	if !sawOpen {
		t.Fatal("expected open-bash phase")
	}
}

func TestPhaseWideningRejectionLeavesPhaseUnchanged(t *testing.T) {
	w := widenBashWorkflow()
	doneArgs, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "pd-rej", Name: "phase_done", Args: doneArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("stayed"),
	)
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic:      enginebind.Diagnostic(),
		SessionID:            "phase-widen-reject",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(tools.NewPhaseDone()),
		Agents:               []engine.Agent{{Name: "build"}},
		Workflows:            []engine.Workflow{w},
		Rules:                []permission.Ruleset{permission.Defaults(), denyBash},
		InitialAutonomy:      protocol.AutonomyAgent,
		InitialPhaseWorkflow: w.Name,
		InitialPhaseIndex:    0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "locked"
	})

	eng.Ops() <- protocol.UserInput{Text: "try advance"}
	var endErr bool
	var openSeen bool
	deadline := time.After(10 * time.Second)
	for !endErr {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				if len(e.Questions) > 0 && e.Questions[0].ID == "phase_grant" {
					eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"No"}}
				}
			case protocol.PhaseChanged:
				if e.Phase == "open-bash" {
					openSeen = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "pd-rej" {
					if !e.IsError {
						t.Fatalf("want error end after reject: %#v", e)
					}
					if !strings.Contains(strings.ToLower(e.Output), "declin") &&
						!strings.Contains(strings.ToLower(e.Output), "widen") {
						t.Fatalf("error output = %q", e.Output)
					}
					endErr = true
				}
			}
		}
	}
	if openSeen {
		t.Fatal("rejection must not enter open-bash")
	}
}

func TestPhaseWideningAutoAcceptsWithoutPrompt(t *testing.T) {
	w := widenBashWorkflow()
	doneArgs, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "pd-auto", Name: "phase_done", Args: doneArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("auto ok"),
	)
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic:            enginebind.Diagnostic(),
		SessionID:                  "phase-widen-auto",
		Select:                     func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:            "scripted",
		Registry:                   tool.NewRegistry(tools.NewPhaseDone()),
		Agents:                     []engine.Agent{{Name: "build"}},
		Workflows:                  []engine.Workflow{w},
		Rules:                      []permission.Ruleset{permission.Defaults(), denyBash},
		InitialAutonomy:            protocol.AutonomyAgent,
		InitialPhaseWorkflow:       w.Name,
		InitialPhaseIndex:          0,
		DangerouslySkipPermissions: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "locked"
	})

	eng.Ops() <- protocol.UserInput{Text: "advance auto"}
	var sawAutoGrant bool
	var sawOpen bool
	var endOK bool
	deadline := time.After(10 * time.Second)
	for !endOK {
		select {
		case <-deadline:
			t.Fatalf("timeout auto=%v open=%v", sawAutoGrant, sawOpen)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				t.Fatalf("auto mode must not prompt: %#v", e)
			case protocol.PhaseGrantApproved:
				if !e.Auto {
					t.Fatalf("want Auto grant: %#v", e)
				}
				sawAutoGrant = true
			case protocol.PhaseChanged:
				if e.Phase == "open-bash" {
					sawOpen = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "pd-auto" && !e.IsError {
					endOK = true
				}
			}
		}
	}
	if !sawAutoGrant || !sawOpen {
		t.Fatalf("auto=%v open=%v", sawAutoGrant, sawOpen)
	}
}

func TestPhaseWideningResumeSkipsReprompt(t *testing.T) {
	w := widenBashWorkflow()
	delta := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Allow},
	}
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	// Resume directly into open-bash with matching prior approval.
	prov := newScriptedProvider(completedStep("resumed"))
	eng := engine.New(engine.Options{
		BuildDiagnostic:      enginebind.Diagnostic(),
		SessionID:            "phase-widen-resume",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Agents:               []engine.Agent{{Name: "build"}},
		Workflows:            []engine.Workflow{w},
		Rules:                []permission.Ruleset{permission.Defaults(), denyBash},
		InitialPhaseWorkflow: w.Name,
		InitialPhaseIndex:    1,
		InitialPhaseGrantApproval: engine.PhaseGrantApproval{
			Workflow:    w.Name,
			Phase:       "open-bash",
			Index:       1,
			Fingerprint: w.Fingerprint,
			Grants:      delta,
		},
		QuietStartup: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Quiet startup: no PhaseChanged emit. Probe via a bash allow after startup
	// by running a write-free turn is hard; instead ensure no QuestionAsked
	// and that a subsequent non-quiet re-entry isn't needed.
	// Drain briefly for any unexpected prompts.
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			return
		case ev := <-eng.Events():
			if q, ok := ev.(protocol.QuestionAsked); ok {
				t.Fatalf("resume must not re-prompt: %#v", q)
			}
		}
	}
}

func TestPhaseWideningChangedFingerprintInvalidates(t *testing.T) {
	w := widenBashWorkflow()
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	prov := newScriptedProvider(completedStep("need reapprove"))
	eng := engine.New(engine.Options{
		BuildDiagnostic:      enginebind.Diagnostic(),
		SessionID:            "phase-widen-stale-fp",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Agents:               []engine.Agent{{Name: "build"}},
		Workflows:            []engine.Workflow{w},
		Rules:                []permission.Ruleset{permission.Defaults(), denyBash},
		InitialPhaseWorkflow: w.Name,
		InitialPhaseIndex:    1,
		InitialPhaseGrantApproval: engine.PhaseGrantApproval{
			Workflow:    w.Name,
			Phase:       "open-bash",
			Index:       1,
			Fingerprint: "stale-fingerprint-not-matching",
			Grants: permission.Ruleset{
				{Permission: "bash", Pattern: "*", Action: permission.Allow},
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Without questions service and without auto, enterPhase fails closed —
	// phase should not activate. Wait for AgentSelected then ensure no open-bash.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			return
		case ev := <-eng.Events():
			if p, ok := ev.(protocol.PhaseChanged); ok && p.Phase == "open-bash" {
				t.Fatalf("stale fingerprint must not enter phase without re-approval: %#v", p)
			}
		}
	}
}

func TestChildInheritsPhaseCeilingCannotWiden(t *testing.T) {
	dir := t.TempDir()
	// Parent phase denies write. Child agent tries to allow write — must fail.
	w := engine.Workflow{
		SchemaVersion: engine.WorkflowSchemaVersion,
		Name:          "phase-deny-write",
		Phases: []engine.Phase{{
			Name:  "readonly",
			Agent: "build",
			Permissions: permission.Ruleset{
				{Permission: "write", Pattern: "*", Action: permission.Deny},
				{Permission: "edit", Pattern: "*", Action: permission.Deny},
			},
			Exit: engine.ExitGate{Type: engine.GateAgent},
		}},
	}
	w.Fingerprint = "fp-" + w.Name

	const childPrompt = "try write"
	taskCall := taskToolCallWithAgent("task-phase-ceil", childPrompt, "writer")
	writeCall := writeToolCall("w-child-phase", "secret.txt", "pwned\n")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(writeCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after deny")
			s.match = matchToolResult("w-child-phase")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-phase-ceil")
			return s
		}(),
		childCompletedNudgeStep("parent ack phase child"),
	)

	baseAllow := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "edit", Pattern: "*", Action: permission.Allow},
		{Permission: "read", Pattern: "*", Action: permission.Allow},
		{Permission: "task", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic:      enginebind.Diagnostic(),
		SessionID:            "phase-child-ceiling",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(tool.NewTask(), tool.NewWrite(), tool.NewEdit()),
		WorkDir:              dir,
		Rules:                []permission.Ruleset{permission.Defaults(), baseAllow},
		Workflows:            []engine.Workflow{w},
		InitialPhaseWorkflow: w.Name,
		InitialPhaseIndex:    0,
		Agents: []engine.Agent{
			{Name: "build", Description: "parent"},
			{
				Name:        "writer",
				Description: "tries to widen",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Allow},
					{Permission: "edit", Pattern: "*", Action: permission.Allow},
				},
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "readonly"
	})

	eng.Ops() <- protocol.UserInput{Text: "delegate write to writer child"}
	var (
		askedWrite bool
		sawTask    bool
		childDone  bool
		parentDone bool
		writeEnd   protocol.ToolCallEnd
		sawWrite   bool
	)
	deadline := time.After(15 * time.Second)
	for !(parentDone && childDone && sawTask) {
		select {
		case <-deadline:
			t.Fatalf("timed out task=%v child=%v parent=%v write=%v", sawTask, childDone, parentDone, sawWrite)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PermissionAsked:
				if e.Permission == "write" || e.Permission == "edit" {
					askedWrite = true
				}
				eng.Ops() <- protocol.PermissionReply{
					RequestID: e.RequestID,
					Decision:  protocol.DecisionReject,
				}
			case protocol.ChildCompleted:
				childDone = true
			case protocol.ToolCallEnd:
				if e.CallID == "task-phase-ceil" {
					sawTask = true
				}
				if e.CallID == "w-child-phase" {
					writeEnd = e
					sawWrite = true
				}
			case protocol.TurnCompleted:
				parentDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
	if askedWrite {
		t.Error("child write emitted PermissionAsked; parent phase deny must hard-reject")
	}
	if sawWrite && !writeEnd.IsError {
		t.Fatalf("child write must error under phase ceiling: %#v", writeEnd)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.txt")); !os.IsNotExist(err) {
		t.Errorf("secret.txt should not exist; err=%v", err)
	}
}

func TestChildPhaseCannotWidenParentDenyEvenWithAuto(t *testing.T) {
	// Parent config denies bash. Child with --auto enters a phase that allows
	// bash — filtered ceiling must keep bash denied (no Deny→Allow on children).
	dir := t.TempDir()
	childWF := engine.Workflow{
		SchemaVersion: engine.WorkflowSchemaVersion,
		Name:          "child-widen",
		Phases: []engine.Phase{{
			Name:  "open",
			Agent: "build",
			Permissions: permission.Ruleset{
				{Permission: "bash", Pattern: "*", Action: permission.Allow},
			},
			Exit: engine.ExitGate{Type: engine.GateAgent},
		}},
	}
	childWF.Fingerprint = "fp-" + childWF.Name

	const childPrompt = "enter widen phase"
	// Child calls enter_plan_mode equivalent via phase: we seed InitialPhase on child
	// by having the child engine created with the workflow — use task + child Initial*
	// is internal. Instead spawn child and have it try bash after parent already
	// applied phase deny via Rules.
	//
	// Simpler unit-style: parent Rules deny bash; child Depth=1 with auto + phase allow.
	// Use engine.New directly as a depth-1 engine (simulates spawned child).
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	// Parent layers as child would receive them (config deny in base).
	childRules := permission.DeriveChildRules(
		[]permission.Ruleset{permission.Defaults(), denyBash},
		true,
	)
	bashArgs, _ := json.Marshal(map[string]any{"command": "echo pwned"})
	bashCall := provider.ToolCall{ID: "bash-child", Name: "bash", Args: bashArgs}
	prov := newScriptedProvider(
		toolCallStep(bashCall),
		completedStep("after bash"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic:            enginebind.Diagnostic(),
		SessionID:                  "child-depth-1",
		ParentSessionID:            "parent",
		Depth:                      1,
		MaxChildDepth:              1,
		Select:                     func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:            "scripted",
		Registry:                   tool.NewRegistry(tool.NewBash()),
		WorkDir:                    dir,
		SandboxMode:                "off",
		Rules:                      childRules,
		Agents:                     []engine.Agent{{Name: "build"}},
		Workflows:                  []engine.Workflow{childWF},
		InitialPhaseWorkflow:       childWF.Name,
		InitialPhaseIndex:          0,
		DangerouslySkipPermissions: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Phase may apply (filtered) without grant prompt; bash must stay denied.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "run bash"}
	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PermissionAsked:
				if e.Permission == "bash" {
					t.Fatalf("bash should be hard-denied, not asked: %#v", e)
				}
				eng.Ops() <- protocol.PermissionReply{RequestID: e.RequestID, Decision: protocol.DecisionReject}
			case protocol.ToolCallEnd:
				if e.CallID == "bash-child" {
					end = e
					goto done
				}
			}
		}
	}
done:
	if !end.IsError {
		t.Fatalf("child bash must fail under parent deny ceiling: %#v", end)
	}
	_ = childPrompt
}

func TestRestorePhaseGrantApproved(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	events := []protocol.Event{
		protocol.PhaseChanged{Correlation: corr, Workflow: "widen-bash", Phase: "open-bash", Index: 1, Gate: "agent"},
		protocol.PhaseGrantApproved{
			Correlation: corr,
			Workflow:    "widen-bash",
			Phase:       "open-bash",
			Index:       1,
			Fingerprint: "fp1",
			Grants:      []protocol.PhaseGrantRule{{Permission: "bash", Pattern: "*", Action: "allow"}},
		},
	}
	// Grant after phase.changed still restores (order can vary on older logs).
	r := engine.Restore(events)
	if r.PhaseGrant.Workflow != "widen-bash" || r.PhaseGrant.Fingerprint != "fp1" {
		t.Fatalf("PhaseGrant = %#v", r.PhaseGrant)
	}
	if len(r.PhaseGrant.Grants) != 1 || r.PhaseGrant.Grants[0].Permission != "bash" {
		t.Fatalf("grants = %#v", r.PhaseGrant.Grants)
	}

	// Clearing phase drops grant.
	events = append(events, protocol.PhaseChanged{Correlation: corr})
	r = engine.Restore(events)
	if r.PhaseGrant.Workflow != "" || r.PhaseName != "" {
		t.Fatalf("cleared grant/phase = phase=%q grant=%#v", r.PhaseName, r.PhaseGrant)
	}
}

func TestRestorePhaseGrantBeforePhaseChanged(t *testing.T) {
	// Production order: grant approved then phase.changed.
	corr := protocol.Correlation{SessionID: "s2"}
	events := []protocol.Event{
		protocol.PhaseGrantApproved{
			Correlation: corr,
			Workflow:    "w",
			Phase:       "p",
			Index:       0,
			Fingerprint: "fp",
			Grants:      []protocol.PhaseGrantRule{{Permission: "bash", Pattern: "*", Action: "allow"}},
		},
		protocol.PhaseChanged{Correlation: corr, Workflow: "w", Phase: "p", Index: 0},
	}
	r := engine.Restore(events)
	if r.PhaseGrant.Fingerprint != "fp" || r.PhaseName != "p" {
		t.Fatalf("restore = phase=%q grant=%#v", r.PhaseName, r.PhaseGrant)
	}
}
