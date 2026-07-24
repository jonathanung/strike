package echo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func collect(t *testing.T, ch <-chan provider.StreamEvent) []provider.StreamEvent {
	t.Helper()
	var out []provider.StreamEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out collecting events")
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		}
	}
}

func TestEchoPlainText(t *testing.T) {
	ch, err := New().Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var text strings.Builder
	var done bool
	for _, ev := range evs {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.Text)
		case provider.EventDone:
			done = true
			if ev.StopReason != "end_turn" {
				t.Errorf("stop = %q", ev.StopReason)
			}
		case provider.EventToolCall:
			t.Fatal("unexpected tool call")
		}
	}
	if !done || !strings.Contains(text.String(), "You said: hello") {
		t.Fatalf("text=%q done=%v", text.String(), done)
	}
}

func TestEchoRunToolCall(t *testing.T) {
	ch, err := New().Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "run echo hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var sawTool, sawDone bool
	for _, ev := range evs {
		switch ev.Type {
		case provider.EventToolCall:
			sawTool = true
			if ev.ToolCall == nil || ev.ToolCall.Name != "bash" {
				t.Fatalf("tool = %#v", ev.ToolCall)
			}
			if !strings.Contains(string(ev.ToolCall.Args), "echo hi") {
				t.Errorf("args = %s", ev.ToolCall.Args)
			}
		case provider.EventDone:
			sawDone = true
			if ev.StopReason != "tool_use" {
				t.Errorf("stop = %q", ev.StopReason)
			}
		}
	}
	if !sawTool || !sawDone {
		t.Fatalf("tool=%v done=%v events=%d", sawTool, sawDone, len(evs))
	}
}

func TestEchoToolResult(t *testing.T) {
	ch, err := New().Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{
			Role:       provider.RoleTool,
			ToolResult: &provider.ToolResult{Output: "ok-result", IsError: false},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	var text strings.Builder
	for _, ev := range evs {
		if ev.Type == provider.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	if !strings.Contains(text.String(), "succeeded") || !strings.Contains(text.String(), "ok-result") {
		t.Errorf("text = %q", text.String())
	}
}

func TestEchoEmptyMessages(t *testing.T) {
	ch, err := New().Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	if len(evs) != 1 || evs[0].Type != provider.EventDone {
		t.Fatalf("events = %#v", evs)
	}
}

func TestName(t *testing.T) {
	if New().Name() != "echo" {
		t.Fatal(New().Name())
	}
}
