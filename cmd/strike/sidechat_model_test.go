package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	model := newSideChatModel(cap, "echo", nil)
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
	model := newSideChatModel(toolProvider{}, "tool", nil)
	_, err := model.Answer(context.Background(), server.SideChatPrompt{Question: "approve the bash call"})
	if err == nil || !strings.Contains(err.Error(), "refused a tool call") {
		t.Fatalf("err = %v", err)
	}

	echoModel := newSideChatModel(echo.New(), "echo", nil)
	_, err = echoModel.Answer(context.Background(), server.SideChatPrompt{
		Transcript: "Tool: bash\nApproval waiting (side chat cannot answer it): bash",
		Question:   "run true",
	})
	if err == nil || !strings.Contains(err.Error(), "refused a tool call") {
		t.Fatalf("echo err = %v", err)
	}
}

func TestSideChatModelNilProvider(t *testing.T) {
	if newSideChatModel(nil, "echo", nil) != nil {
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

type holdProvider struct {
	started  chan struct{}
	release  chan struct{}
	released chan struct{}
}

func (p *holdProvider) Name() string { return "hold" }

func (p *holdProvider) Stream(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	go func() {
		close(p.started)
		<-p.release
		ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
		close(ch)
		close(p.released)
	}()
	return ch, nil
}

func TestSideChatModelDrainsOnCancel(t *testing.T) {
	p := &holdProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		released: make(chan struct{}),
	}
	model := newSideChatModel(p, "hold", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := model.Answer(ctx, server.SideChatPrompt{Question: "hi"})
		done <- err
	}()
	select {
	case <-p.started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Answer did not return on cancel")
	}
	close(p.release)
	select {
	case <-p.released:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled ask did not drain the provider stream")
	}
}

func TestSideChatModelFollowsLiveProvider(t *testing.T) {
	boot := &captureProvider{inner: echo.New()}
	other := &captureProvider{inner: echo.New()}
	selected := ""
	model := newSideChatModel(boot, "echo", func(name string) (provider.Provider, string, error) {
		selected = name
		return other, "claude-default", nil
	})
	got, err := model.Answer(context.Background(), server.SideChatPrompt{
		Question: "what changed?",
		Provider: "anthropic",
		Model:    "claude-opus",
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "anthropic" {
		t.Fatalf("select = %q", selected)
	}
	if boot.req.Model != "" {
		t.Fatal("boot provider was called for a different live provider")
	}
	if other.req.Model != "claude-opus" || len(other.req.Tools) != 0 {
		t.Fatalf("request = %#v", other.req)
	}
	if !strings.Contains(got.Text, "what changed?") || got.Model != "claude-opus" || got.Provider != "anthropic" {
		t.Fatalf("completion = %#v", got)
	}

	selected = ""
	same, err := model.Answer(context.Background(), server.SideChatPrompt{
		Question: "still echo?",
		Provider: "echo",
		Model:    "echo-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "" {
		t.Fatalf("same provider reselected %q", selected)
	}
	if boot.req.Model != "echo-1" || len(boot.req.Tools) != 0 {
		t.Fatalf("same request = %#v", boot.req)
	}
	if same.Model != "echo-1" {
		t.Fatalf("same completion = %#v", same)
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
