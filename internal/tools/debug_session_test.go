package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
)

func TestDebugSessionForwardsToEngine(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		engErr   error
		want     tool.DebugOp
		wantOut  string
		wantErr  string
		noEngine bool
	}{
		{
			name: "hypotheses",
			args: `{"action":"hypotheses","hypotheses":[{"id":"h1","statement":"cache stale","confirms":"old value logged","refutes":"fresh value"},{"id":"h2","statement":"race"}]}`,
			want: tool.DebugOp{Action: "hypotheses", Hypotheses: []tool.DebugHypothesis{
				{ID: "h1", Statement: "cache stale", Confirms: "old value logged", Refutes: "fresh value"},
				{ID: "h2", Statement: "race"},
			}},
			wantOut: "phase hypothesis",
		},
		{
			name:    "evidence",
			args:    `{"action":"evidence","hypothesis_id":"h1","source":"repro","text":"got 3 want 4"}`,
			want:    tool.DebugOp{Action: "evidence", HypothesisID: "h1", Source: "repro", Text: "got 3 want 4"},
			wantOut: "phase hypothesis",
		},
		{
			name:    "mark",
			args:    `{"action":"mark","hypothesis_id":"h1","status":"confirmed"}`,
			want:    tool.DebugOp{Action: "mark", HypothesisID: "h1", Status: "confirmed"},
			wantOut: "phase hypothesis",
		},
		{
			name:    "engine refuses",
			args:    `{"action":"unlock","text":"trust me"}`,
			engErr:  errors.New("action \"unlock\" is not allowed"),
			want:    tool.DebugOp{Action: "unlock", Text: "trust me"},
			wantErr: "not allowed",
		},
		{
			name:     "bad json",
			args:     `{"action":`,
			wantErr:  "invalid arguments",
			noEngine: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := allowAll(t.TempDir())
			var got []tool.DebugOp
			tc.DebugSession = func(op tool.DebugOp) (string, error) {
				got = append(got, op)
				return "Debug phase hypothesis", tt.engErr
			}
			res, err := NewDebugSession().Execute(context.Background(), json.RawMessage(tt.args), tc)
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case !strings.Contains(res.Output, tt.wantOut):
				t.Errorf("output = %q, want %q", res.Output, tt.wantOut)
			}
			if tt.noEngine {
				if len(got) != 0 {
					t.Fatalf("bad args reached the engine: %+v", got)
				}
				return
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], tt.want) {
				t.Fatalf("DebugSession calls = %+v, want one %+v", got, tt.want)
			}
		})
	}
}

func TestDebugSessionWithoutEngineHook(t *testing.T) {
	_, err := NewDebugSession().Execute(context.Background(), json.RawMessage(`{"action":"mark"}`), allowAll(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestDebugSessionPermissionDenied(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Ask = func(context.Context, tool.AskRequest) error { return errors.New("denied") }
	called := false
	tc.DebugSession = func(tool.DebugOp) (string, error) { called = true; return "", nil }
	if _, err := NewDebugSession().Execute(context.Background(), json.RawMessage(`{"action":"mark"}`), tc); err == nil {
		t.Fatal("want permission error")
	}
	if called {
		t.Fatal("denied call reached the engine")
	}
}

func TestDebugSessionSchemaOffersOnlyAgentActions(t *testing.T) {
	var s struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewDebugSession().Schema(), &s); err != nil {
		t.Fatal(err)
	}
	want := []string{"hypotheses", "evidence", "mark"}
	if !reflect.DeepEqual(s.Properties.Action.Enum, want) {
		t.Fatalf("action enum = %v, want %v", s.Properties.Action.Enum, want)
	}
}

func TestDebugSessionContract(t *testing.T) {
	c := tool.LookupContract(NewDebugSession())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.SideEffect != tool.SideEffectNone {
		t.Fatalf("contract = %+v", c)
	}
}
