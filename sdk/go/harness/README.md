# Go Subprocess Harness SDK

This package turns an ordinary Go function into an external Strike harness. It
handles stdin/stdout JSONL, provider call correlation, progress messages, and
cancellation.

```go
package main

import (
	"log"

	strikeharness "github.com/jonathanung/strike-cli/sdk/go/harness"
)

func chooseBest(
	input strikeharness.Input,
	provider strikeharness.Provider,
	emit strikeharness.Emit,
) (strikeharness.Result, error) {
	response, err := provider.Call(input.Request)
	if err != nil {
		return strikeharness.Result{}, err
	}
	_ = emit(map[string]any{"status": "complete"})
	return strikeharness.Result{
		Text:       response.Text,
		StopReason: response.StopReason,
	}, nil
}

func main() {
	if err := strikeharness.Run(chooseBest); err != nil {
		log.Fatal(err)
	}
}
```

Compile the program and configure its executable under `harnesses` in Strike
config. This is a subprocess integration and does not link the function into
the Strike binary.
