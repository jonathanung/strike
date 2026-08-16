package harnesses

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestChooseBest(t *testing.T) {
	candidates := []string{"short", "the longest response", "medium"}
	calls := 0
	progress := 0
	result, err := ChooseBest(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Text: "solve"}}},
	}, fn.Provider{
		Call: func(req provider.Request) (fn.ModelResponse, error) {
			if len(req.Messages) != 2 {
				t.Fatalf("messages = %#v", req.Messages)
			}
			response := fn.ModelResponse{Text: candidates[calls]}
			calls++
			return response, nil
		},
	}, func(raw json.RawMessage) {
		progress++
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "the longest response" || result.StopReason != "end_turn" {
		t.Fatalf("result = %#v", result)
	}
	if calls != 3 || progress != 3 {
		t.Fatalf("calls = %d, progress = %d", calls, progress)
	}
}
