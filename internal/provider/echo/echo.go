// Package echo is an offline development provider that exercises the full
// engine loop without an API key. Plain input is echoed back word-by-word
// (simulating streaming); input starting with "run " triggers a bash tool
// call, which exercises tool dispatch and the permission ask flow end-to-end.
package echo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
)

type Provider struct{}

func New() Provider { return Provider{} }

func (Provider) Name() string { return "echo" }

func (Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		if len(req.Messages) == 0 {
			ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
			return
		}
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role == provider.RoleTool:
			status := "succeeded"
			if last.ToolResult.IsError {
				status = "failed"
			}
			emitText(ctx, ch, fmt.Sprintf("The tool call %s. Result:\n\n%s", status, truncate(last.ToolResult.Output, 800)))
		case strings.HasPrefix(last.Text, "run "):
			args, _ := json.Marshal(map[string]string{"command": strings.TrimPrefix(last.Text, "run ")})
			ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID:   fmt.Sprintf("call_%d", time.Now().UnixNano()),
				Name: "bash",
				Args: args,
			}}
			ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
			return
		default:
			emitText(ctx, ch, "You said: "+last.Text+"\n\nTip: start a message with `run <command>` to exercise the bash tool and the permission prompt.")
		}
		ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
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
