package terminal

import (
	"errors"
	"testing"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/tool"
)

func TestTerminalMicroVMUnavailableFailsClosed(t *testing.T) {
	sandbox.ResetWarnForTest()
	sandbox.ForceMicroVMStatusForTest(sandbox.MicroVMStatus{Reason: "forced: no KVM"})
	t.Cleanup(sandbox.ResetWarnForTest)
	root := t.TempDir()
	m := NewManager(ManagerConfig{
		Scrollback:  64 * 1024,
		MaxSessions: 4,
		Sandbox: sandbox.Policy{
			Mode:         sandbox.ModeWorkspaceWrite,
			WorkDir:      root,
			AllowDegrade: true, // must not apply to the microvm runtime
			Runtime:      sandbox.RuntimeMicroVM,
		},
		LookPath: func(string) (string, error) { return "/bin/bash", nil },
	})
	t.Cleanup(m.Close)
	_, err := m.Create(CreateSpec{WorkspacePath: root})
	var ce *tool.CodedError
	if !errors.As(err, &ce) || ce.Code != tool.CodeSandboxDenied {
		t.Fatalf("err = %v (%T), want sandbox_denied", err, err)
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a host shell was started: %+v", got)
	}
}
