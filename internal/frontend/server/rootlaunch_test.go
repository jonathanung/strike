package server

import (
	"context"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/internal/protocol"
)

func TestRootLauncherDefaultsToPlanAndContinues(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("root-9", "/work", nil, ops)
	defer live.Close()
	hub := NewLiveHub(nil, func(ctx context.Context, sessionID string) (string, bool, error) {
		if sessionID != "root-9" {
			t.Fatalf("resume %q", sessionID)
		}
		return "root-9", true, nil
	})
	hub.Add("root-9", live)
	res, err := NewRootLauncher(hub).StartRoot(context.Background(), rootstart.Request{
		Source:     rootstart.SourceTrigger,
		Prompt:     "keep going",
		ContinueID: "root-9",
		Title:      "trigger",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.SessionID != "root-9" || res.PermissionMode != string(protocol.PermissionModePlan) {
		t.Fatalf("result = %+v", res)
	}
	if hub.List()[0].Title != "trigger" {
		t.Fatalf("title = %q", hub.List()[0].Title)
	}
	mode := recvOp(t, ops)
	set, ok := mode.(protocol.SetPermissionMode)
	if !ok || set.Mode != protocol.PermissionModePlan {
		t.Fatalf("first op = %#v", mode)
	}
	input := recvOp(t, ops)
	ui, ok := input.(protocol.UserInput)
	if !ok || ui.Text != "keep going" {
		t.Fatalf("second op = %#v", input)
	}
	if _, err := NewRootLauncher(hub).StartRoot(context.Background(), rootstart.Request{Prompt: "  "}); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := NewRootLauncher(nil).StartRoot(context.Background(), rootstart.Request{Prompt: "x"}); err == nil {
		t.Fatal("nil hub accepted")
	}
}

func TestRootLauncherCreates(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	var hub *LiveHub
	hub = NewLiveHub(func(ctx context.Context) (string, error) {
		live := NewLive("root-new", "/work", nil, ops)
		hub.Add("root-new", live)
		return "root-new", nil
	}, nil)
	res, err := NewRootLauncher(hub).StartRoot(context.Background(), rootstart.Request{
		Source:         rootstart.SourceSchedule,
		Prompt:         "hi",
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.SessionID != "root-new" || res.PermissionMode != "default" {
		t.Fatalf("result = %+v", res)
	}
	_ = recvOp(t, ops)
	_ = recvOp(t, ops)
}

func recvOp(t *testing.T, ops <-chan protocol.Op) protocol.Op {
	t.Helper()
	select {
	case op := <-ops:
		return op
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for op")
		return nil
	}
}
