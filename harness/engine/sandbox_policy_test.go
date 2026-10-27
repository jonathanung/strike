package engine

import (
	"testing"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestBashSandboxPolicyRecompilesForWorkspace(t *testing.T) {
	svc := permission.New(func(protocol.Event) {})
	svc.SetPermissionMode(protocol.PermissionModePlan)
	e := &Engine{
		opts: Options{
			SandboxMode:         "workspace-write",
			WorkDir:             "/engine-root",
			NetworkAllow:        []string{"example.com"},
			SandboxAllowDegrade: false,
		},
		perms: svc,
	}
	p := e.BashSandboxPolicy("/ws")
	if p.WorkDir != "/ws" || !p.NoWorkspaceWrite || p.Mode != sandbox.ModeWorkspaceWrite {
		t.Fatalf("workspace policy = %+v", p)
	}
	if len(p.NetworkAllow) != 1 || p.NetworkAllow[0] != "example.com" {
		t.Fatalf("network allow = %#v", p.NetworkAllow)
	}
	same := e.bashSandboxPolicy()
	if same.WorkDir != "/engine-root" || !same.NoWorkspaceWrite {
		t.Fatalf("bash policy = %+v", same)
	}
}
