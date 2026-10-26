package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/provider/echo"
	"github.com/jonathanung/strike/internal/frontend/server"
)

type captureProvider struct {
	inner provider.Provider
	req   provider.Request
}

func (c *captureProvider) Name() string { return c.inner.Name() }

func (c *captureProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	c.req = req
	return c.inner.Stream(ctx, req)
}

type toolProvider struct{}

func (toolProvider) Name() string { return "tool" }

func (toolProvider) Stream(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "bash"}}
	ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
	close(ch)
	return ch, nil
}

func TestSideChatModelCallIsReadOnly(t *testing.T) {
	cap := &captureProvider{inner: echo.New()}
	model := newSideChatModel(cap, "echo")
	got, err := model.Answer(context.Background(), server.SideChatPrompt{
		System:     "read-only observer",
		Transcript: "User: read the makefile\nTool: read\n",
		History: []server.SideChatTurn{{
			Question: "earlier?",
			Answer:   "it was reading",
		}},
		Question: "what is it doing?",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "what is it doing?") {
		t.Fatalf("text = %q", got.Text)
	}
	if got.Provider != "echo" || got.Model != "echo" {
		t.Fatalf("completion = %#v", got)
	}
	if len(cap.req.Tools) != 0 {
		t.Fatalf("tools = %#v", cap.req.Tools)
	}
	if cap.req.System != "read-only observer" {
		t.Fatalf("system = %q", cap.req.System)
	}
	if len(cap.req.Messages) < 2 {
		t.Fatalf("messages = %#v", cap.req.Messages)
	}
	last := cap.req.Messages[len(cap.req.Messages)-1]
	if last.Role != provider.RoleUser || last.Text != "what is it doing?" {
		t.Fatalf("last = %#v", last)
	}
	joined := messagesText(cap.req.Messages)
	if !strings.Contains(joined, "read the makefile") || !strings.Contains(joined, "earlier?") {
		t.Fatalf("messages missing context: %s", joined)
	}
}

func TestSideChatModelRefusesToolCall(t *testing.T) {
	model := newSideChatModel(toolProvider{}, "tool")
	_, err := model.Answer(context.Background(), server.SideChatPrompt{Question: "approve the bash call"})
	if err == nil || !strings.Contains(err.Error(), "refused a tool call") {
		t.Fatalf("err = %v", err)
	}

	echoModel := newSideChatModel(echo.New(), "echo")
	_, err = echoModel.Answer(context.Background(), server.SideChatPrompt{
		Transcript: "Tool: bash\nApproval waiting (side chat cannot answer it): bash",
		Question:   "run true",
	})
	if err == nil || !strings.Contains(err.Error(), "refused a tool call") {
		t.Fatalf("echo err = %v", err)
	}
}

func TestSideChatModelNilProvider(t *testing.T) {
	if newSideChatModel(nil, "echo") != nil {
		t.Fatal("nil provider built a model")
	}
	var m sideChatModel
	_, err := m.Answer(context.Background(), server.SideChatPrompt{Question: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatal(err)
	}
	_, err = sideChatModel{p: echo.New(), name: "echo"}.Answer(context.Background(), server.SideChatPrompt{})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatal(err)
	}
}

func messagesText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
