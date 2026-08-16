package tool

import (
	"strings"
	"testing"
)

func TestDetectSandboxDenial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		proc   ProcessResult
		wantOK bool
		sub    string
	}{
		{
			name: "applied read-only fs",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       1,
				Output:         "touch: cannot touch '/etc/x': Read-only file system",
				Status:         ProcessStatusExited,
			},
			wantOK: true,
			sub:    "read-only",
		},
		{
			name: "applied permission denied",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       1,
				Stderr:         "bash: /root/x: Permission denied",
				Status:         ProcessStatusExited,
			},
			wantOK: true,
			sub:    "permission denied",
		},
		{
			name: "seatbelt deny",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       1,
				Output:         "sandbox-exec: deny file-write-create /Users/x",
				Status:         ProcessStatusExited,
			},
			wantOK: true,
			sub:    "seatbelt",
		},
		{
			name: "not applied",
			proc: ProcessResult{
				SandboxApplied: false,
				ExitCode:       1,
				Output:         "Permission denied",
				Status:         ProcessStatusExited,
			},
			wantOK: false,
		},
		{
			name: "zero exit",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       0,
				Output:         "Permission denied in log noise",
				Status:         ProcessStatusExited,
			},
			wantOK: false,
		},
		{
			name: "timeout wins",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       -1,
				Output:         "Permission denied",
				Status:         ProcessStatusTimeout,
			},
			wantOK: false,
		},
		{
			name: "plain failure no pattern",
			proc: ProcessResult{
				SandboxApplied: true,
				ExitCode:       1,
				Output:         "command not found: foo",
				Status:         ProcessStatusExited,
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, ok := detectSandboxDenial(tc.proc)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v reason=%q", ok, tc.wantOK, reason)
			}
			if tc.wantOK && !strings.Contains(strings.ToLower(reason), tc.sub) {
				t.Fatalf("reason = %q, want substring %q", reason, tc.sub)
			}
		})
	}
}

func TestFormatSandboxDenial(t *testing.T) {
	t.Parallel()
	got := formatSandboxDenial("write blocked: filesystem is read-only under OS sandbox", "partial\n(exit code 1)")
	if !strings.HasPrefix(got, string(CodeSandboxDenied)+": ") {
		t.Fatalf("prefix = %q", got)
	}
	if !strings.Contains(got, "partial") {
		t.Fatalf("missing body: %q", got)
	}
	empty := formatSandboxDenial("", "(no output)")
	if empty != string(CodeSandboxDenied)+": blocked by OS sandbox" {
		t.Fatalf("empty = %q", empty)
	}
}
