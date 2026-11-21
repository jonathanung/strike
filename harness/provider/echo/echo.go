// Package echo is an offline development provider that exercises the full
// engine loop without an API key. Plain input is echoed back word-by-word
// (simulating streaming); input starting with "run " triggers a bash tool
// call, which exercises tool dispatch and the permission ask flow end-to-end;
// input starting with "ask " triggers a question tool call (one choice
// question plus one free-form question) so frontends can exercise the
// question card end-to-end.
package echo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike/harness/provider"
)

type Provider struct{}

func New() Provider { return Provider{} }

func (Provider) Name() string { return "echo" }

func (Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		if len(req.Messages) == 0 {
			ch <- provider.StreamEvent{
				Type:       provider.EventDone,
				StopReason: "end_turn",
				Usage:      estimateUsage(req, ""),
			}
			return
		}
		last := req.Messages[len(req.Messages)-1]
		var emitted string
		switch {
		case last.Role == provider.RoleTool:
			status := "succeeded"
			if last.ToolResult.IsError {
				status = "failed"
			}
			emitted = fmt.Sprintf("The tool call %s. Result:\n\n%s", status, truncate(last.ToolResult.Output, 800))
			emitText(ctx, ch, emitted)
		case strings.HasPrefix(last.Text, "run "):
			args, _ := json.Marshal(map[string]string{"command": strings.TrimPrefix(last.Text, "run ")})
			ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID:   fmt.Sprintf("call_%d", time.Now().UnixNano()),
				Name: "bash",
				Args: args,
			}}
			ch <- provider.StreamEvent{
				Type:       provider.EventDone,
				StopReason: "tool_use",
				Usage:      estimateUsage(req, ""),
			}
			return
		case strings.HasPrefix(last.Text, "ask "):
			ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID:   fmt.Sprintf("call_%d", time.Now().UnixNano()),
				Name: "question",
				Args: questionArgs(strings.TrimPrefix(last.Text, "ask ")),
			}}
			ch <- provider.StreamEvent{
				Type:       provider.EventDone,
				StopReason: "tool_use",
				Usage:      estimateUsage(req, ""),
			}
			return
		default:
			emitted = "You said: " + last.Text + "\n\nTip: start a message with `run <command>` to exercise the bash tool and the permission prompt."
			emitText(ctx, ch, emitted)
		}
		ch <- provider.StreamEvent{
			Type:       provider.EventDone,
			StopReason: "end_turn",
			Usage:      estimateUsage(req, emitted),
		}
	}()
	return ch, nil
}

// questionArgs builds the question tool arguments for an "ask " message:
// a two-option question carrying the user's text, then a free-form one.
func questionArgs(text string) json.RawMessage {
	type option struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	}
	type item struct {
		ID       string   `json:"id"`
		Header   string   `json:"header,omitempty"`
		Question string   `json:"question"`
		Options  []option `json:"options,omitempty"`
	}
	args, _ := json.Marshal(map[string][]item{"questions": {
		{ID: "choice", Header: "Choice", Question: text, Options: []option{
			{Label: "Yes (Recommended)", Description: "Go ahead as described"},
			{Label: "No", Description: "Do not do this"},
		}},
		{ID: "note", Header: "Note", Question: "Anything else the agent should know?"},
	}})
	return args
}

// estimateUsage is a deterministic offline stand-in: ~4 runes per token.
// Always non-nil with Estimated=true so the engine can emit UsageReported.
func estimateUsage(req provider.Request, emitted string) *provider.Usage {
	var inputRunes int
	for _, m := range req.Messages {
		inputRunes += len([]rune(m.Text))
		if m.ToolResult != nil {
			inputRunes += len([]rune(m.ToolResult.Output))
		}
	}
	input := 0
	if len(req.Messages) > 0 {
		input = max(1, inputRunes/4)
	}
	output := 0
	if emitted != "" {
		output = max(1, len([]rune(emitted))/4)
	}
	return &provider.Usage{
		InputTokens:  input,
		OutputTokens: output,
		Estimated:    true,
	}
}

func emitText(ctx context.Context, ch chan<- provider.StreamEvent, text string) {
	for _, word := range strings.SplitAfter(text, " ") {
		select {
		case <-ctx.Done():
			return
		case ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: word}:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
