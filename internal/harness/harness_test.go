package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestRegistryRegisterAndResolveFunc(t *testing.T) {
	r := harness.NewRegistry()
	r.Register("choose", func(input harness.Input, p harness.Provider, emit harness.Emit) (harness.Result, error) {
		response, err := p.Call(input.Request)
		if err != nil {
			return harness.Result{}, err
		}
		emit(json.RawMessage(`{"status":"complete"}`))
		return harness.Result{Text: response.Text, StopReason: response.StopReason}, nil
	})

	fn, err := r.Resolve("choose")
	if err != nil {
		t.Fatal(err)
	}
	progress := 0
	result, err := fn(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "fixture"},
	}, harness.Provider{
		Call: func(req provider.Request) (harness.ModelResponse, error) {
			if req.Model != "fixture" {
				t.Fatalf("provider request = %#v", req)
			}
			return harness.ModelResponse{Text: "candidate", StopReason: "end_turn"}, nil
		},
	}, func(json.RawMessage) { progress++ })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "candidate" || result.StopReason != "end_turn" || progress != 1 {
		t.Fatalf("result = %#v, progress = %d", result, progress)
	}
}

func TestRegistryUnknown(t *testing.T) {
	tests := []struct {
		name string
		reg  *harness.Registry
		want string
	}{
		{name: "empty registry", reg: harness.NewRegistry(), want: `unknown harness "missing"`},
		{name: "nil registry", want: "no registry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn, err := tt.reg.Resolve("missing")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Resolve error = %v, want substring %q", err, tt.want)
			}
			if fn != nil {
				t.Fatal("Resolve returned a function")
			}
		})
	}
}

func TestRegistryZeroValue(t *testing.T) {
	var r harness.Registry
	if r.Known("custom") || r.Get("custom") != nil {
		t.Fatal("zero-value registry contains a function")
	}
	r.Register("custom", func(harness.Input, harness.Provider, harness.Emit) (harness.Result, error) {
		return harness.Result{StopReason: "complete"}, nil
	})
	if !r.Known("custom") {
		t.Fatal("registered function is not known")
	}
}

func TestRegistryRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		key  string
		fn   harness.Func
	}{
		{name: "empty name", fn: func(harness.Input, harness.Provider, harness.Emit) (harness.Result, error) {
			return harness.Result{}, nil
		}},
		{name: "nil function", key: "custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			harness.NewRegistry().Register(tt.key, tt.fn)
		})
	}
}
