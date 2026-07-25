package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const sleepMaxSeconds = 300.0

type sleepTool struct{}

func NewSleep() Tool { return sleepTool{} }

func (sleepTool) Name() string { return "sleep" }

func (sleepTool) Description() string {
	return `Pauses execution for a given number of seconds.

Use this instead of bash sleep when you need to wait (e.g. for a service to become ready). Maximum 300 seconds.`
}

func (sleepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"seconds": {"type": "number", "description": "Seconds to sleep (greater than 0, maximum 300)"}
		},
		"required": ["seconds"]
	}`)
}

type sleepArgs struct {
	Seconds float64 `json:"seconds"`
}

func (sleepTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a sleepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Seconds <= 0 || a.Seconds > sleepMaxSeconds {
		return Result{}, fmt.Errorf("seconds must be in (0, 300]")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "sleep", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}

	d := time.Duration(a.Seconds * float64(time.Second))
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-timer.C:
	}
	return Result{
		Title:  fmt.Sprintf("slept %.3gs", a.Seconds),
		Output: fmt.Sprintf("Slept for %g seconds", a.Seconds),
	}, nil
}
