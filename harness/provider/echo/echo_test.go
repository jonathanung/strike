package echo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/provider"
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

func TestEchoDoneUsageIsEstimatedAndDeterministic(t *testing.T) {
	req := provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hello"}},
	}
	var usages []*provider.Usage
	for i := 0; i < 2; i++ {
		ch, err := New().Stream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		evs := collect(t, ch)
		var done *provider.StreamEvent
		for i := range evs {
			if evs[i].Type == provider.EventDone {
				done = &evs[i]
			}
		}
		if done == nil {
			t.Fatal("missing EventDone")
		}
		if done.Usage == nil {
			t.Fatal("Usage is nil on normal stream; want non-nil estimated usage")
		}
		if !done.Usage.Estimated {
			t.Error("Estimated = false, want true for echo")
		}
		if done.Usage.InputTokens <= 0 {
			t.Errorf("InputTokens = %d, want > 0", done.Usage.InputTokens)
		}
		if done.Usage.OutputTokens <= 0 {
			t.Errorf("OutputTokens = %d, want > 0", done.Usage.OutputTokens)
		}
		usages = append(usages, done.Usage)
	}
	if usages[0].InputTokens != usages[1].InputTokens || usages[0].OutputTokens != usages[1].OutputTokens {
		t.Errorf("usage not deterministic: first=%+v second=%+v", usages[0], usages[1])
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

func TestEchoAskQuestionToolCall(t *testing.T) {
	ch, err := New().Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "ask Use Postgres?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var call *provider.ToolCall
	var stop string
	for _, ev := range collect(t, ch) {
		switch ev.Type {
		case provider.EventToolCall:
			call = ev.ToolCall
		case provider.EventDone:
			stop = ev.StopReason
		case provider.EventTextDelta:
			t.Fatalf("unexpected text %q", ev.Text)
		}
	}
	if call == nil || call.Name != "question" {
		t.Fatalf("tool = %#v", call)
	}
	if stop != "tool_use" {
		t.Fatalf("stop = %q", stop)
	}
	var args struct {
		Questions []struct {
			ID       string `json:"id"`
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(args.Questions))
	}
	if q := args.Questions[0]; q.Question != "Use Postgres?" || len(q.Options) != 2 {
		t.Fatalf("first question = %#v", q)
	}
	if q := args.Questions[1]; q.Question == "" || len(q.Options) != 0 {
		t.Fatalf("second question should be free-form, got %#v", q)
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
