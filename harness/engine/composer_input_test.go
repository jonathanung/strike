package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestComposerShellAsksPermissionAndSkipsModelTurn(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:              selectEcho,
		InitialProvider:     "echo",
		Registry:            tool.NewRegistry(),
		WorkDir:             t.TempDir(),
		SandboxMode:         "off",
		SandboxAllowDegrade: true,
		Rules:               []permission.Ruleset{permission.Defaults()},
		PrepareInput: func(text, _ string) engine.InputPrep {
			text = strings.TrimSpace(text)
			if !strings.HasPrefix(text, "!") {
				return engine.InputPrep{}
			}
			return engine.InputPrep{
				Action:  engine.InputShell,
				Shell:   strings.TrimSpace(strings.TrimPrefix(text, "!")),
				Display: text,
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "!echo composer-bang-ok"}

	var sawAsk, sawUser, sawBegin, sawEnd, sawTurn bool
	var output string
	deadline := time.After(15 * time.Second)
	for !sawEnd {
		select {
		case <-deadline:
			t.Fatalf("timed out ask=%v user=%v begin=%v end=%v turn=%v", sawAsk, sawUser, sawBegin, sawEnd, sawTurn)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				sawAsk = true
				if ev.Permission != "bash" {
					t.Errorf("permission = %q, want bash", ev.Permission)
				}
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.UserMessage:
				sawUser = true
				if ev.Text != "!echo composer-bang-ok" {
					t.Errorf("user message = %q", ev.Text)
				}
			case protocol.ToolCallBegin:
				sawBegin = true
				if ev.Name != "bash" {
					t.Errorf("tool = %q", ev.Name)
				}
			case protocol.ToolCallEnd:
				sawEnd = true
				output = ev.Output
				if ev.IsError {
					t.Errorf("tool error: %s", ev.Output)
				}
			case protocol.TurnStarted, protocol.TextDelta:
				sawTurn = true
			}
		}
	}
	if !sawAsk || !sawUser || !sawBegin {
		t.Fatalf("ask=%v user=%v begin=%v", sawAsk, sawUser, sawBegin)
	}
	if sawTurn {
		t.Fatal("shell started a model turn")
	}
	if !strings.Contains(output, "composer-bang-ok") {
		t.Fatalf("output = %q", output)
	}
}

func TestComposerShellRejectDoesNotRun(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:              selectEcho,
		InitialProvider:     "echo",
		WorkDir:             t.TempDir(),
		SandboxMode:         "off",
		SandboxAllowDegrade: true,
		Rules:               []permission.Ruleset{permission.Defaults()},
		PrepareInput: func(text, _ string) engine.InputPrep {
			return engine.InputPrep{Action: engine.InputShell, Shell: strings.TrimPrefix(strings.TrimSpace(text), "!"), Display: strings.TrimSpace(text)}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "!echo should-not-run"}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for rejected shell")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionReject}
			case protocol.ToolCallEnd:
				if !ev.IsError {
					t.Fatalf("reject still ran: %#v", ev)
				}
				if strings.Contains(ev.Output, "should-not-run") {
					t.Fatalf("command output leaked after reject: %q", ev.Output)
				}
				return
			case protocol.TurnStarted:
				t.Fatal("reject started a model turn")
			}
		}
	}
}

func TestComposerTextKeepsDisplaySeparateFromModel(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		WorkDir:         t.TempDir(),
		PrepareInput: func(text, _ string) engine.InputPrep {
			if !strings.Contains(text, "@notes") {
				return engine.InputPrep{}
			}
			return engine.InputPrep{
				Action:    engine.InputText,
				Display:   text,
				ModelText: text + "\n\n--- file: notes.txt ---\nsecret body\n--- end file: notes.txt ---",
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "see @notes"}

	var user, reply string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out user=%q reply=%q", user, reply)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.UserMessage:
				user = ev.Text
			case protocol.TextDelta:
				reply += ev.Text
			case protocol.TurnCompleted:
				if user == "" || reply == "" {
					t.Fatalf("turn ended early user=%q reply=%q", user, reply)
				}
				goto checkedDisplay
			}
		}
	}
checkedDisplay:
	if user != "see @notes" {
		t.Fatalf("user message = %q", user)
	}
	if !strings.Contains(reply, "secret body") {
		t.Fatalf("model did not see the file body: %q", reply)
	}
	if strings.Contains(user, "secret body") {
		t.Fatal("transcript user line included the file body")
	}
}

func TestComposerOpDoesNotStartTurn(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		WorkDir:         t.TempDir(),
		PrepareInput: func(text, _ string) engine.InputPrep {
			if strings.TrimSpace(text) != "/mode plan" {
				return engine.InputPrep{}
			}
			return engine.InputPrep{Action: engine.InputOp, Op: protocol.SetPermissionMode{Mode: protocol.PermissionModePlan}}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Drain the startup mode event first.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for startup mode")
		case ev := <-eng.Events():
			if sel, ok := ev.(protocol.PermissionModeSelected); ok && sel.Mode == protocol.PermissionModeDefault {
				goto send
			}
		}
	}
send:
	eng.Ops() <- protocol.UserInput{Text: "/mode plan"}
	deadline = time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for plan mode")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionModeSelected:
				if ev.Mode != protocol.PermissionModePlan {
					t.Fatalf("mode = %q", ev.Mode)
				}
				return
			case protocol.TurnStarted, protocol.UserMessage:
				t.Fatalf("mode command started a turn: %T", ev)
			}
		}
	}
}

func TestComposerUnknownCommandErrors(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		WorkDir:         t.TempDir(),
		PrepareInput: func(text, _ string) engine.InputPrep {
			return engine.InputPrep{Action: engine.InputError, Error: "unknown command " + text}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "/nope"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.EngineError:
				if !strings.Contains(ev.Message, "unknown command") {
					t.Fatalf("message = %q", ev.Message)
				}
				return
			case protocol.TurnStarted:
				t.Fatal("unknown command started a turn")
			}
		}
	}
}
