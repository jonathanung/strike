package main

import (
	"fmt"
	"log"

	strikeharness "github.com/jonathanung/strike-cli/sdk/go/harness"
)

func chooseBest(input strikeharness.Input, provider strikeharness.Provider, emit strikeharness.Emit) (strikeharness.Result, error) {
	best := ""
	for i := 0; i < 3; i++ {
		if err := input.Context.Err(); err != nil {
			return strikeharness.Result{}, err
		}
		request := input.Request
		request.Messages = append(append([]strikeharness.Message(nil), request.Messages...), strikeharness.Message{
			Role: "user",
			Text: fmt.Sprintf("Generate candidate %d", i+1),
		})
		response, err := provider.Call(request)
		if err != nil {
			return strikeharness.Result{}, err
		}
		if len(response.Text) > len(best) {
			best = response.Text
		}
		if err := emit(map[string]any{"kind": "candidate", "current": i + 1, "total": 3}); err != nil {
			return strikeharness.Result{}, err
		}
	}
	return strikeharness.Result{Text: best, StopReason: "end_turn"}, nil
}

func main() {
	if err := strikeharness.Run(chooseBest); err != nil {
		log.Fatal(err)
	}
}
