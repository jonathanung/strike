package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
)

func TestSteerAckForwardsToEngine(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		ackErr  error
		want    tool.SteerAck
		wantOut string
		wantErr string
	}{
		{
			name:    "summary only",
			args:    `{"summary":"Now: v2 api"}`,
			want:    tool.SteerAck{Summary: "Now: v2 api"},
			wantOut: "Continue with the new direction",
		},
		{
			name:    "conflict",
			args:    `{"summary":"dropping refactor","conflict":true,"reason":"plan was mid-refactor"}`,
			want:    tool.SteerAck{Summary: "dropping refactor", Conflict: true, Reason: "plan was mid-refactor"},
			wantOut: "with a conflict",
		},
		{
			name:    "engine refuses",
			args:    `{"summary":"x"}`,
			ackErr:  errors.New("no unacknowledged steer"),
			want:    tool.SteerAck{Summary: "x"},
			wantErr: "no unacknowledged steer",
		},
		{
			name:    "bad json",
			args:    `{"summary":`,
			wantErr: "invalid arguments",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := allowAll(t.TempDir())
			var got []tool.SteerAck
			tc.AckSteer = func(a tool.SteerAck) error {
				got = append(got, a)
				return tt.ackErr
			}
			res, err := NewSteerAck().Execute(context.Background(), json.RawMessage(tt.args), tc)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if !strings.Contains(res.Output, tt.wantOut) {
				t.Errorf("output = %q, want %q", res.Output, tt.wantOut)
			}
			if tt.wantErr == "invalid arguments" {
				if len(got) != 0 {
					t.Fatalf("bad args reached the engine: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("AckSteer calls = %+v, want one %+v", got, tt.want)
			}
		})
	}
}

func TestSteerAckWithoutEngineHook(t *testing.T) {
	_, err := NewSteerAck().Execute(context.Background(), json.RawMessage(`{"summary":"x"}`), allowAll(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestSteerAckContract(t *testing.T) {
	c := tool.LookupContract(NewSteerAck())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.SideEffect != tool.SideEffectNone {
		t.Fatalf("contract = %+v", c)
	}
}
