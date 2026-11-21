package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/sandbox"
)

// forceMicroVMUnavailable pins the microvm probe so tests never touch /dev/kvm.
func forceMicroVMUnavailable(t *testing.T) {
	t.Helper()
	sandbox.ResetWarnForTest()
	sandbox.ForceMicroVMStatusForTest(sandbox.MicroVMStatus{Reason: "forced: no KVM"})
	t.Cleanup(sandbox.ResetWarnForTest)
}

func TestRunProcessMicroVMUnavailableFailsClosed(t *testing.T) {
	cases := []struct {
		name         string
		mode         sandbox.Mode
		allowDegrade bool
	}{
		{"workspace-write", sandbox.ModeWorkspaceWrite, false},
		{"allow degrade ignored", sandbox.ModeWorkspaceWrite, true},
		{"read-only", sandbox.ModeReadOnly, true},
		{"mode off config error", sandbox.ModeOff, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forceMicroVMUnavailable(t)
			dir := t.TempDir()
			marker := filepath.Join(dir, "ran-on-host")
			started := false
			res, err := RunProcess(context.Background(), ProcessSpec{
				Argv: []string{"bash", "-c", "touch " + marker},
				Dir:  dir,
				Sandbox: sandbox.Policy{
					Mode:         tc.mode,
					WorkDir:      dir,
					AllowDegrade: tc.allowDegrade,
					Runtime:      sandbox.RuntimeMicroVM,
				},
			}, ProcessObserver{Started: func(string, []string) { started = true }})
			var ce *CodedError
			if !errors.As(err, &ce) || ce.Code != CodeSandboxDenied {
				t.Fatalf("err = %v (%T), want sandbox_denied", err, err)
			}
			if !strings.Contains(ce.Message, "microvm") {
				t.Fatalf("message = %q, want microvm reason", ce.Message)
			}
			if !res.SandboxDegraded || res.SandboxApplied {
				t.Fatalf("result = %+v", res)
			}
			if started {
				t.Fatal("process started despite fail-closed microvm runtime")
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatal("command ran on the host (no host fallback allowed)")
			}
		})
	}
}

func TestBashMicroVMUnavailableNoHostFallback(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	forceMicroVMUnavailable(t)
	root := t.TempDir()
	marker := filepath.Join(root, "ran-on-host")
	tc := allowAll(root)
	tc.Sandbox = sandbox.Policy{
		Mode:         sandbox.ModeWorkspaceWrite,
		WorkDir:      root,
		AllowDegrade: true,
		Runtime:      sandbox.RuntimeMicroVM,
	}
	_, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "touch " + marker,
	}), tc)
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != CodeSandboxDenied {
		t.Fatalf("err = %v (%T), want sandbox_denied", err, err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("bash ran on the host under microvm runtime")
	}
}

func TestBashSandboxPolicyKeepsRuntime(t *testing.T) {
	wd := t.TempDir()
	// A runtime-only policy must not be replaced by the SandboxMode fallback,
	// or the microvm selection would silently drop back to the host launcher.
	p := bashSandboxPolicy(&Context{
		WorkDir:     wd,
		SandboxMode: "off",
		Sandbox:     sandbox.Policy{Runtime: sandbox.RuntimeMicroVM},
	})
	if p.Runtime != sandbox.RuntimeMicroVM {
		t.Fatalf("runtime = %v, want microvm", p.Runtime)
	}
	if p.WorkDir != wd {
		t.Fatalf("workdir = %q", p.WorkDir)
	}
}
