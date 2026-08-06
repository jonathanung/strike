package permission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/sandbox"
)

func TestCompileSandboxOff(t *testing.T) {
	p := CompileSandbox(sandbox.ModeOff, t.TempDir(), Defaults(), Ruleset{
		{Permission: "write", Pattern: "**/*.env", Action: Deny},
		{Permission: "webfetch", Pattern: "*", Action: Allow},
	})
	if p.Mode != sandbox.ModeOff || p.Network || p.NoWorkspaceWrite || len(p.DenyWriteGlobs) != 0 {
		t.Fatalf("off policy = %+v", p)
	}
}

func TestCompileSandboxWriteDenyGlob(t *testing.T) {
	wd := t.TempDir()
	envPath := filepath.Join(wd, ".env")
	if err := os.WriteFile(envPath, []byte("x=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretDir := filepath.Join(wd, "secrets")
	if err := os.Mkdir(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}

	p := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "write", Pattern: "**/*.env", Action: Deny},
		{Permission: "write", Pattern: "secrets/**", Action: Deny},
	})
	if p.NoWorkspaceWrite {
		t.Fatal("specific denies must not clear workspace-write")
	}
	if !p.Network {
		t.Fatal("default webfetch/mcp ask must keep network on (gh/git/npm)")
	}
	if !containsStr(p.DenyWriteGlobs, "**/*.env") || !containsStr(p.DenyWriteGlobs, "secrets/**") {
		t.Fatalf("globs = %v", p.DenyWriteGlobs)
	}
	if !containsPath(p.DenyWritePaths, envPath) {
		t.Fatalf("paths missing .env: %v", p.DenyWritePaths)
	}
	if !containsPath(p.DenyWritePaths, secretDir) {
		t.Fatalf("paths missing secrets/: %v", p.DenyWritePaths)
	}
	if !p.WorkspaceWritable() {
		t.Fatal("expected workspace writable")
	}
}

func TestCompileSandboxWriteStarDeny(t *testing.T) {
	wd := t.TempDir()
	p := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "write", Pattern: "*", Action: Deny},
		{Permission: "edit", Pattern: "*", Action: Deny},
	})
	if !p.NoWorkspaceWrite {
		t.Fatal("write/edit deny * should set NoWorkspaceWrite")
	}
	if p.WorkspaceWritable() {
		t.Fatal("workspace must not be writable")
	}
}

func TestCompileSandboxNetworkDefaultOn(t *testing.T) {
	wd := t.TempDir()
	// Defaults alone (webfetch/mcp Ask) — coding agents need host network.
	p := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults())
	if !p.Network {
		t.Fatal("default permissions must enable bash network")
	}
	// Explicit allow still on.
	pAllow := CompileSandbox(sandbox.ModeReadOnly, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Allow},
	})
	if !pAllow.Network {
		t.Fatal("webfetch allow * should keep network on")
	}
	pMCP := CompileSandbox(sandbox.ModeReadOnly, wd, Defaults(), Ruleset{
		{Permission: "mcp", Pattern: "*", Action: Allow},
	})
	if !pMCP.Network {
		t.Fatal("mcp allow * should keep network on")
	}
	// Patterned allow does not change * posture (still Ask → on).
	pPat := CompileSandbox(sandbox.ModeReadOnly, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "https://example.com/*", Action: Allow},
	})
	if !pPat.Network {
		t.Fatal("patterned webfetch allow must not disable default network")
	}
}

func TestCompileSandboxNetworkOffOnlyOnDualDeny(t *testing.T) {
	wd := t.TempDir()
	// Single-family deny must not air-gap bash (webfetch tool deny ≠ no git).
	pWF := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Deny},
	})
	if !pWF.Network {
		t.Fatal("webfetch deny alone must keep bash network on")
	}
	pMCP := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "mcp", Pattern: "*", Action: Deny},
	})
	if !pMCP.Network {
		t.Fatal("mcp deny alone must keep bash network on")
	}
	// Both hard-deny on * → --unshare-net / no network-*.
	pBoth := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Deny},
		{Permission: "mcp", Pattern: "*", Action: Deny},
	})
	if pBoth.Network {
		t.Fatal("webfetch+mcp deny * should disable bash network")
	}
	// Patterned deny must not flip full-network posture after dual deny on *.
	// (Evaluate("*") stays Deny; patterned rules do not match subject "*".)
	pPat := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Deny},
		{Permission: "mcp", Pattern: "*", Action: Deny},
		{Permission: "webfetch", Pattern: "https://example.com/*", Action: Allow},
	})
	if pPat.Network {
		t.Fatal("patterned webfetch allow must not re-open full bash network after dual deny")
	}
}

func TestCompileSandboxDefaultNetworkOmitsUnshareNet(t *testing.T) {
	// When the Linux backend is available, default compile must not inject
	// --unshare-net (regression for gh/git DNS failures under bwrap).
	if sandbox.BackendName() != "bwrap" || !sandbox.Available() {
		t.Skip("bwrap backend not available")
	}
	wd := t.TempDir()
	p := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults())
	if !p.Network {
		t.Fatal("expected Network true from Defaults")
	}
	argv := sandbox.Wrap([]string{"true"}, p)
	joined := strings.Join(argv, "\x00")
	if strings.Contains(joined, "\x00--unshare-net\x00") || strings.HasSuffix(joined, "\x00--unshare-net") || strings.HasPrefix(joined, "--unshare-net\x00") {
		t.Fatalf("default policy must omit --unshare-net: %#v", argv)
	}
	// Dual deny still gets isolation.
	off := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Defaults(), Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Deny},
		{Permission: "mcp", Pattern: "*", Action: Deny},
	})
	offArgv := sandbox.Wrap([]string{"true"}, off)
	offJoined := "\x00" + strings.Join(offArgv, "\x00") + "\x00"
	if !strings.Contains(offJoined, "\x00--unshare-net\x00") {
		t.Fatalf("dual-deny policy must include --unshare-net: %#v", offArgv)
	}
}

func TestCompileSandboxLastMatchWins(t *testing.T) {
	wd := t.TempDir()
	envPath := filepath.Join(wd, ".env")
	if err := os.WriteFile(envPath, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Deny then later allow the same pattern — should not appear in denials.
	p := CompileSandbox(sandbox.ModeWorkspaceWrite, wd, Ruleset{
		{Permission: "write", Pattern: "**/*.env", Action: Deny},
		{Permission: "write", Pattern: "**/*.env", Action: Allow},
	})
	if containsStr(p.DenyWriteGlobs, "**/*.env") {
		t.Fatalf("allow-after-deny should drop glob: %v", p.DenyWriteGlobs)
	}
}

func TestServiceCompileSandboxIncludesAgent(t *testing.T) {
	wd := t.TempDir()
	secret := filepath.Join(wd, "agent-secret")
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(func(protocol.Event) {}, Defaults())
	svc.SetAgentRules(Ruleset{
		{Permission: "write", Pattern: "agent-secret/**", Action: Deny},
	})
	p := svc.CompileSandbox(sandbox.ModeWorkspaceWrite, wd)
	if !containsStr(p.DenyWriteGlobs, "agent-secret/**") {
		t.Fatalf("agent deny missing: %v", p.DenyWriteGlobs)
	}
	if !containsPath(p.DenyWritePaths, secret) {
		t.Fatalf("agent path missing: %v", p.DenyWritePaths)
	}
}

func TestCompileSandboxPlanModeLate(t *testing.T) {
	wd := t.TempDir()
	svc := New(func(protocol.Event) {}, Defaults())
	svc.SetPermissionMode(protocol.PermissionModePlan)
	p := svc.CompileSandbox(sandbox.ModeWorkspaceWrite, wd)
	if !p.NoWorkspaceWrite {
		t.Fatal("plan mode should compile NoWorkspaceWrite")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func containsPath(list []string, want string) bool {
	want = filepath.Clean(want)
	if real, err := filepath.EvalSymlinks(want); err == nil && real != "" {
		want = real
	}
	for _, s := range list {
		got := filepath.Clean(s)
		if real, err := filepath.EvalSymlinks(got); err == nil && real != "" {
			got = real
		}
		if got == want || strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}
