package fn_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestRegistryRegisterAndResolveFunc(t *testing.T) {
	r := fn.NewRegistry()
	r.Register("choose", func(input fn.Input, p fn.Provider, emit fn.Emit) (fn.Result, error) {
		response, err := p.Call(input.Request)
		if err != nil {
			return fn.Result{}, err
		}
		emit(json.RawMessage(`{"status":"complete"}`))
		return fn.Result{Text: response.Text, StopReason: response.StopReason}, nil
	})

	run, err := r.Resolve("choose")
	if err != nil {
		t.Fatal(err)
	}
	progress := 0
	result, err := run(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "fixture"},
	}, fn.Provider{
		Call: func(req provider.Request) (fn.ModelResponse, error) {
			if req.Model != "fixture" {
				t.Fatalf("provider request = %#v", req)
			}
			return fn.ModelResponse{Text: "candidate", StopReason: "end_turn"}, nil
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
		reg  *fn.Registry
		want string
	}{
		{name: "empty registry", reg: fn.NewRegistry(), want: `unknown harness "missing"`},
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
	var r fn.Registry
	if r.Known("custom") || r.Get("custom") != nil {
		t.Fatal("zero-value registry contains a function")
	}
	r.Register("custom", func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
		return fn.Result{StopReason: "complete"}, nil
	})
	if !r.Known("custom") {
		t.Fatal("registered function is not known")
	}
}

func TestRegistryRegisterPanics(t *testing.T) {
	tests := []struct {
		name string
		key  string
		fn   fn.Func
	}{
		{name: "empty name", fn: func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
			return fn.Result{}, nil
		}},
		{name: "leading whitespace", key: " custom", fn: func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
			return fn.Result{}, nil
		}},
		{name: "trailing whitespace", key: "custom ", fn: func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
			return fn.Result{}, nil
		}},
		{name: "control character", key: "custom\nname", fn: func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
			return fn.Result{}, nil
		}},
		{name: "invalid UTF-8", key: string([]byte{0xff}), fn: func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
			return fn.Result{}, nil
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
			fn.NewRegistry().Register(tt.key, tt.fn)
		})
	}
}
