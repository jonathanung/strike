// Package harnesses contains embedded Go harness examples. These functions are
// not part of the stock Strike binary; a custom binary must import and register
// them at compile time. Strike's integration tests do so directly.
package harnesses

import (
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// ChooseBest requests three candidates and returns the longest response.
func ChooseBest(input fn.Input, p fn.Provider, emit fn.Emit) (fn.Result, error) {
	best := ""
	for i := 0; i < 3; i++ {
		if input.Context != nil {
			if err := input.Context.Err(); err != nil {
				return fn.Result{}, err
			}
		}
		req := input.Request
		req.Messages = append(append([]provider.Message(nil), req.Messages...), provider.Message{
			Role: provider.RoleUser,
			Text: fmt.Sprintf("Generate candidate %d", i+1),
		})
		response, err := p.Call(req)
		if err != nil {
			return fn.Result{}, err
		}
		if len(response.Text) > len(best) {
			best = response.Text
		}
		if emit != nil {
			progress, _ := json.Marshal(map[string]any{
				"kind":    "candidate",
				"current": i + 1,
				"total":   3,
			})
			emit(progress)
		}
	}
	return fn.Result{Text: best, StopReason: "end_turn"}, nil
}

var _ fn.Func = ChooseBest
