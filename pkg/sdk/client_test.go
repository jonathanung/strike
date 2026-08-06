package sdk_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)

func TestNewPanicsOnNilOps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil ops")
		}
	}()
	_ = sdk.New(nil, make(chan protocol.Event))
}

func TestNewPanicsOnNilEvents(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil events")
		}
	}()
	_ = sdk.New(make(chan protocol.Op), nil)
}

func TestSendAndPrompt(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event)
	c := sdk.New(ops, events)

	ctx := context.Background()
	if err := c.Prompt(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	op := <-ops
	in, ok := op.(protocol.UserInput)
	if !ok || in.Text != "hello" {
		t.Fatalf("op = %#v", op)
	}

	if err := c.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := (<-ops).(protocol.Interrupt); !ok {
		t.Fatal("want Interrupt")
	}
}

func TestSendRespectsCancel(t *testing.T) {
	ops := make(chan protocol.Op) // unbuffered, no receiver
	events := make(chan protocol.Event)
	c := sdk.New(ops, events)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Send(ctx, protocol.UserInput{Text: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTurnHappyPath(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	c := sdk.New(ops, events)

	go func() {
		op := <-ops
		if _, ok := op.(protocol.UserInput); !ok {
			t.Errorf("first op = %#v", op)
		}
		events <- protocol.TurnStarted{}
		events <- protocol.TextDelta{Text: "hel"}
		events <- protocol.TextDelta{Text: "lo"}
		events <- protocol.TurnCompleted{StopReason: "end_turn"}
	}()

	result, err := c.RunTurn(context.Background(), sdk.Turn{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" {
		t.Fatalf("text = %q", result.Text)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stop = %q", result.StopReason)
	}
}

func TestRunTurnPermissionDefaultReject(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event, 8)
	c := sdk.New(ops, events)

	go func() {
		<-ops // user input
		events <- protocol.PermissionAsked{RequestID: "r1", Permission: "bash"}
		reply := <-ops
		pr, ok := reply.(protocol.PermissionReply)
		if !ok {
			t.Errorf("reply = %#v", reply)
			return
		}
		if pr.RequestID != "r1" || pr.Decision != protocol.DecisionReject {
			t.Errorf("reply = %#v", pr)
		}
		if !strings.Contains(pr.Message, "permission ask denied") {
			t.Errorf("message = %q", pr.Message)
		}
		events <- protocol.TurnCompleted{StopReason: "end_turn"}
	}()

	if _, err := c.RunTurn(context.Background(), sdk.Turn{Text: "run"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunTurnPermissionHook(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event, 8)
	c := sdk.New(ops, events)

	go func() {
		<-ops
		events <- protocol.PermissionAsked{RequestID: "r2", Permission: "edit"}
		reply := <-ops
		pr := reply.(protocol.PermissionReply)
		if pr.Decision != protocol.DecisionOnce {
			t.Errorf("decision = %v", pr.Decision)
		}
		events <- protocol.TextDelta{Text: "ok"}
		events <- protocol.TurnCompleted{StopReason: "end_turn"}
	}()

	result, err := c.RunTurn(context.Background(), sdk.Turn{
		Text: "edit",
		OnPermission: func(a protocol.PermissionAsked) protocol.PermissionReply {
			return protocol.PermissionReply{Decision: protocol.DecisionOnce}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ok" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestRunTurnEngineError(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 4)
	c := sdk.New(ops, events)

	go func() {
		<-ops
		events <- protocol.EngineError{Message: "boom"}
		events <- protocol.TurnCompleted{StopReason: "error"}
	}()

	result, err := c.RunTurn(context.Background(), sdk.Turn{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if result.Err == nil {
		t.Fatal("want result.Err")
	}
}

func TestRunTurnEmptyText(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	events := make(chan protocol.Event)
	c := sdk.New(ops, events)
	_, err := c.RunTurn(context.Background(), sdk.Turn{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunTurnEventsClosed(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	events := make(chan protocol.Event)
	c := sdk.New(ops, events)
	close(events)
	// Send still works; RunTurn sees closed events after prompt.
	go func() {
		// Unblock Send if needed — ops is buffered.
	}()
	_, err := c.RunTurn(context.Background(), sdk.Turn{Text: "x"})
	if !errors.Is(err, sdk.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTurnCancel(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 4)
	c := sdk.New(ops, events)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ops // user input
		events <- protocol.TextDelta{Text: "partial"}
		// Wait until interrupt arrives, then complete.
		deadline := time.After(2 * time.Second)
		for {
			select {
			case op := <-ops:
				if _, ok := op.(protocol.Interrupt); ok {
					events <- protocol.TurnCompleted{StopReason: "canceled"}
					return
				}
			case <-deadline:
				t.Error("timeout waiting for interrupt")
				events <- protocol.TurnCompleted{}
				return
			}
		}
	}()

	// Cancel after prompt is in flight.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result, err := c.RunTurn(ctx, sdk.Turn{Text: "slow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if result.Text != "partial" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestCollectText(t *testing.T) {
	ch := make(chan protocol.Event, 3)
	ch <- protocol.TextDelta{Text: "a"}
	ch <- protocol.TurnStarted{}
	ch <- protocol.TextDelta{Text: "b"}
	close(ch)
	if got := sdk.CollectText(ch); got != "ab" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatEvent(t *testing.T) {
	if sdk.FormatEvent(nil) != "<nil>" {
		t.Fatal(sdk.FormatEvent(nil))
	}
	if !strings.Contains(sdk.FormatEvent(protocol.TextDelta{Text: "hi"}), "len=2") {
		t.Fatal(sdk.FormatEvent(protocol.TextDelta{Text: "hi"}))
	}
}
