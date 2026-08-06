package secret_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/secret"
)

func TestRedactEventToolEnd(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	ev := secret.RedactEvent(protocol.ToolCallEnd{
		CallID: "c1",
		Title:  "bash",
		Output: "export KEY=" + key + "\nok",
	})
	end, ok := ev.(protocol.ToolCallEnd)
	if !ok {
		t.Fatalf("type %T", ev)
	}
	if strings.Contains(end.Output, key) {
		t.Fatalf("tool end leaked key: %q", end.Output)
	}
	if end.CallID != "c1" || end.Title != "bash" {
		t.Fatalf("structural fields changed: %+v", end)
	}
}

func TestRedactEventNestedJSONArgs(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	args, _ := json.Marshal(map[string]any{
		"command": "echo " + key,
		"nested":  map[string]any{"apiKey": "literal-secret-value"},
	})
	ev := secret.RedactEvent(protocol.ToolCallBegin{
		CallID: "c1",
		Name:   "bash",
		Args:   args,
	})
	begin, ok := ev.(protocol.ToolCallBegin)
	if !ok {
		t.Fatalf("type %T", ev)
	}
	raw := string(begin.Args)
	if strings.Contains(raw, key) {
		t.Fatalf("args leaked key: %s", raw)
	}
	if strings.Contains(raw, "literal-secret-value") {
		t.Fatalf("args leaked apiKey field: %s", raw)
	}
	if !strings.Contains(raw, `"command"`) {
		t.Fatalf("args structure lost: %s", raw)
	}
}

func TestRedactEventUserMessage(t *testing.T) {
	tok := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	ev := secret.RedactEvent(protocol.UserMessage{Text: "my token is " + tok})
	um := ev.(protocol.UserMessage)
	if strings.Contains(um.Text, tok) {
		t.Fatalf("user message leaked: %q", um.Text)
	}
}

func TestRedactEventBypassNestedToolOutput(t *testing.T) {
	// Tier C: nested / echoed tool output must not bypass scrubbing.
	inner := "Authorization: Bearer tok_abc1234567890xyz"
	outer := "tool result wrapper:\n" + inner + "\nend"
	ev := secret.RedactEvent(protocol.ToolCallOutput{CallID: "c", Data: outer})
	out := ev.(protocol.ToolCallOutput)
	if strings.Contains(out.Data, "tok_abc1234567890xyz") {
		t.Fatalf("nested bearer leaked: %q", out.Data)
	}
	ev2 := secret.RedactEvent(protocol.ProcessOutput{Data: outer})
	po := ev2.(protocol.ProcessOutput)
	if strings.Contains(po.Data, "tok_abc1234567890xyz") {
		t.Fatalf("process output leaked: %q", po.Data)
	}
}

func TestRedactEventPreservesNonSensitive(t *testing.T) {
	ev := secret.RedactEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	if _, ok := ev.(protocol.TurnCompleted); !ok {
		t.Fatalf("got %T", ev)
	}
	tc := ev.(protocol.TurnCompleted)
	if tc.StopReason != "end_turn" {
		t.Fatalf("stop reason changed: %q", tc.StopReason)
	}
}

func TestRedactEventArtifactUpdated(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	ev := secret.RedactEvent(protocol.ArtifactUpdated{
		ID:      "ab12",
		Type:    "findings",
		Version: 2,
		Op:      "update",
		Title:   "token " + key,
	})
	got, ok := ev.(protocol.ArtifactUpdated)
	if !ok {
		t.Fatalf("type %T", ev)
	}
	if strings.Contains(got.Title, key) {
		t.Fatalf("title leaked key: %q", got.Title)
	}
	if got.ID != "ab12" || got.Type != "findings" || got.Version != 2 || got.Op != "update" {
		t.Fatalf("structural fields changed: %+v", got)
	}
}
