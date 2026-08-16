package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestPromptText(t *testing.T) {
	got := promptText([]ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "resource_link", Name: "main.go", URI: "file:///tmp/main.go"},
		{Type: "resource", Resource: &EmbeddedResource{URI: "file:///a.py", Text: "print(1)"}},
	})
	if !strings.Contains(got, "hello") {
		t.Fatalf("missing text: %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Fatalf("missing link: %q", got)
	}
	if !strings.Contains(got, "print(1)") {
		t.Fatalf("missing resource: %q", got)
	}
}

func TestPromptTextEmpty(t *testing.T) {
	if got := promptText(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"":            StopEndTurn,
		"end_turn":    StopEndTurn,
		"interrupted": StopCancelled,
		"canceled":    StopCancelled,
		"max_tokens":  StopMaxTokens,
		"error":       StopEndTurn,
		"refusal":     StopRefusal,
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q)=%q want %q", in, got, want)
		}
	}
}

func TestToolKind(t *testing.T) {
	cases := map[string]string{
		"read":         "read",
		"edit":         "edit",
		"write":        "edit",
		"bash":         "execute",
		"grep":         "search",
		"webfetch":     "fetch",
		"browser":      "fetch",
		"websearch":    "search",
		"todowrite":    "think",
		"agent_roster": "other",
	}
	for in, want := range cases {
		if got := toolKind(in); got != want {
			t.Errorf("toolKind(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEventUpdatesTextAndTool(t *testing.T) {
	ups := eventUpdates(protocol.TextDelta{Text: "hi"})
	if len(ups) != 1 || ups[0]["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("text updates = %#v", ups)
	}
	content, _ := ups[0]["content"].(map[string]any)
	if content["text"] != "hi" {
		t.Fatalf("content = %#v", content)
	}

	args, _ := json.Marshal(map[string]string{"path": "/tmp/x.go"})
	ups = eventUpdates(protocol.ToolCallBegin{CallID: "c1", Name: "read", Args: args})
	if len(ups) != 1 || ups[0]["sessionUpdate"] != "tool_call" {
		t.Fatalf("begin = %#v", ups)
	}
	if ups[0]["kind"] != "read" || ups[0]["toolCallId"] != "c1" {
		t.Fatalf("begin fields = %#v", ups[0])
	}

	ups = eventUpdates(protocol.ToolCallEnd{CallID: "c1", Output: "ok", IsError: false})
	if len(ups) != 1 || ups[0]["status"] != "completed" {
		t.Fatalf("end = %#v", ups)
	}

	ups = eventUpdates(protocol.ToolCallEnd{CallID: "c1", Output: "nope", IsError: true})
	if ups[0]["status"] != "failed" {
		t.Fatalf("failed = %#v", ups[0])
	}

	ups = eventUpdates(protocol.ReasoningDelta{Text: "think"})
	if ups[0]["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("thought = %#v", ups)
	}
}

func TestEventUpdatesEngineError(t *testing.T) {
	ups := eventUpdates(protocol.EngineError{Message: "boom"})
	if len(ups) != 1 || ups[0]["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("engine error = %#v", ups)
	}
	c, _ := ups[0]["content"].(map[string]any)
	if !strings.Contains(c["text"].(string), "boom") {
		t.Fatalf("text = %#v", c)
	}
}

func TestDecisionFromOption(t *testing.T) {
	if got := decisionFromOption("allow-once"); got != protocol.DecisionOnce {
		t.Fatalf("once = %v", got)
	}
	if got := decisionFromOption("allow-always"); got != protocol.DecisionAlways {
		t.Fatalf("always = %v", got)
	}
	if got := decisionFromOption("reject-once"); got != protocol.DecisionReject {
		t.Fatalf("reject = %v", got)
	}
	if got := decisionFromOption("unknown"); got != protocol.DecisionReject {
		t.Fatalf("default = %v", got)
	}
}
