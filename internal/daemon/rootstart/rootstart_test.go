package rootstart

import (
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		in      string
		want    protocol.PermissionMode
		wantErr bool
	}{
		{in: "", want: protocol.PermissionModePlan},
		{in: "  ", want: protocol.PermissionModePlan},
		{in: "plan", want: protocol.PermissionModePlan},
		{in: "Plan", want: protocol.PermissionModePlan},
		{in: "default", want: protocol.PermissionModeDefault},
		{in: "yolo", want: protocol.PermissionModeYolo},
		{in: "accept-edits", want: protocol.PermissionModeAcceptEdits},
		{in: "soft-approve", want: protocol.PermissionModeSoftApprove},
		{in: "nope", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ResolveMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ResolveMode(%q) ok, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveMode(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
