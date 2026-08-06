package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecPlugin(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal executable script (never started in unit tests that only compile).
	script := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(filepath.Join(bin, "mcp.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "harness.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "hook.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.tools",
  "version": "1.0.0",
  "name": "Tools",
  "strike": { "min": "0.1.0" },
  "capabilities": ["mcp.stdio", "harnesses", "hooks.command"],
  "contributions": {
    "agents": [{ "path": "agents/a.md" }],
    "mcp": [{
      "name": "acmeLint",
      "transport": "stdio",
      "command": "bin/mcp.sh",
      "args": ["--serve"],
      "env": { "ACME_TOKEN": "secret://env/ACME_TOKEN" }
    }],
    "harnesses": [{
      "name": "acme-choose",
      "command": "bin/harness.sh",
      "args": ["--jsonl"]
    }],
    "hooks": [
      {
        "event": "pre_tool_use",
        "matcher": "bash",
        "type": "command",
        "command": "bin/hook.sh"
      },
      {
        "event": "pre_tool_use",
        "matcher": "write",
        "action": "log",
        "message": "write observed"
      }
    ]
  }
}`,
		"agents/a.md": validAgentMD("a"),
	})
}

func TestCompileExecutables_UntrustedBlocksMCPHarnessShellHook(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writeExecPlugin(t, root)

	set := CompileExecutables(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.MCP) != 0 {
		t.Fatalf("untrusted MCP started/compiled: %+v", set.MCP)
	}
	if len(set.Harnesses) != 0 {
		t.Fatalf("untrusted harness compiled: %+v", set.Harnesses)
	}
	// Declarative hook still loads; shell hook does not.
	var shell, rule int
	for _, h := range append(set.GlobalHooks, set.ProjectHooks...) {
		if h.Command != "" {
			shell++
		}
		if h.Action != "" {
			rule++
		}
	}
	if shell != 0 {
		t.Fatalf("untrusted shell hooks=%d", shell)
	}
	if rule != 1 {
		t.Fatalf("declarative hooks=%d want 1", rule)
	}
	var untrusted bool
	for _, d := range set.Diagnostics {
		if d.Code == "executable_untrusted" {
			untrusted = true
		}
	}
	if !untrusted {
		t.Fatalf("expected executable_untrusted diagnostic: %v", set.Diagnostics)
	}
}

func TestTrust_ActivatesExecutablesAndInvalidatesOnDigestChange(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeExecPlugin(t, root)

	// Seed lockfile with local source (as install would).
	digest, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	lf := emptyLockfile()
	lf.Plugins["acme.tools"] = LockfileEntry{
		Enabled: boolPtr(true),
		Version: "1.0.0",
		Digest:  digest,
		Source:  &SourceIdentity{Type: SourceLocal, Path: root},
	}
	if err := WriteLockfile(filepath.Join(gRoot, LockfileName), lf); err != nil {
		t.Fatal(err)
	}

	res, err := Trust(TrustOptions{
		ID:            "acme.tools",
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != digest {
		t.Fatalf("trust digest %s want %s", res.Digest, digest)
	}
	if len(res.Capabilities) == 0 {
		t.Fatal("expected capabilities")
	}

	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.MCP) != 1 || set.MCP[0].Name != "acmeLint" {
		t.Fatalf("MCP: %+v", set.MCP)
	}
	if !filepath.IsAbs(set.MCP[0].Command) || !strings.HasSuffix(set.MCP[0].Command, "mcp.sh") {
		t.Fatalf("MCP command not resolved: %q", set.MCP[0].Command)
	}
	if set.MCP[0].Env["ACME_TOKEN"] != "secret://env/ACME_TOKEN" {
		t.Fatalf("env ref lost: %+v", set.MCP[0].Env)
	}
	if len(set.Harnesses) != 1 || set.Harnesses[0].Name != "acme-choose" {
		t.Fatalf("harnesses: %+v", set.Harnesses)
	}
	var shell int
	for _, h := range set.GlobalHooks {
		if h.Command != "" {
			shell++
			if !filepath.IsAbs(h.Command) {
				t.Fatalf("hook command not abs: %q", h.Command)
			}
		}
	}
	if shell != 1 {
		t.Fatalf("shell hooks=%d", shell)
	}

	// Mutate payload → digest change → trust stale → no executables.
	if err := os.WriteFile(filepath.Join(root, "extra.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	set2 := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set2.MCP) != 0 || len(set2.Harnesses) != 0 {
		t.Fatalf("stale trust still activated: mcp=%v harness=%v", set2.MCP, set2.Harnesses)
	}
	var stale bool
	for _, d := range set2.Diagnostics {
		if d.Code == "executable_untrusted" && strings.Contains(d.Message, "digest") {
			stale = true
		}
	}
	if !stale {
		t.Fatalf("expected digest invalidation diagnostic: %v", set2.Diagnostics)
	}
}

func TestCompileExecutables_DisabledContributesNothing(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeExecPlugin(t, root)
	digest, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	lf := emptyLockfile()
	src := &SourceIdentity{Type: SourceLocal, Path: root}
	lf.Plugins["acme.tools"] = LockfileEntry{
		Enabled: boolPtr(false),
		Version: "1.0.0",
		Digest:  digest,
		Source:  src,
		Trust: &TrustRecord{
			Digest:       digest,
			Source:       src,
			Capabilities: InferCapabilities(mustReadManifest(t, root)),
			TrustedAt:    nowRFC3339(),
		},
	}
	if err := WriteLockfile(filepath.Join(gRoot, LockfileName), lf); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.MCP)+len(set.Harnesses)+len(set.GlobalHooks)+len(set.ProjectHooks) != 0 {
		t.Fatalf("disabled plugin still contributed: %+v", set)
	}
}

func TestCompileExecutables_UserConfigWinsMCPName(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeExecPlugin(t, root)
	digest, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	src := &SourceIdentity{Type: SourceLocal, Path: root}
	m := mustReadManifest(t, root)
	lf := emptyLockfile()
	lf.Plugins["acme.tools"] = LockfileEntry{
		Enabled: boolPtr(true),
		Digest:  digest,
		Source:  src,
		Trust: &TrustRecord{
			Digest:       digest,
			Source:       cloneSource(src),
			Capabilities: InferCapabilities(m),
		},
	}
	if err := WriteLockfile(filepath.Join(gRoot, LockfileName), lf); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, map[string]struct{}{"acmeLint": {}}, nil)
	if len(set.MCP) != 0 {
		t.Fatalf("expected user override skip, got %+v", set.MCP)
	}
	var coll bool
	for _, d := range set.Diagnostics {
		if d.Code == "collision" && d.Collision == "acmeLint" {
			coll = true
		}
	}
	if !coll {
		t.Fatalf("expected collision diagnostic: %v", set.Diagnostics)
	}
}

func TestCompileExecutables_PluginMCPNameCollisionFailClosed(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	for _, id := range []string{"acme.one", "acme.two"} {
		root := filepath.Join(gRoot, "plugins", id)
		writePlugin(t, root, id, map[string]string{
			"plugin.json": `{
  "schemaVersion": 1,
  "id": "` + id + `",
  "version": "1.0.0",
  "name": "` + id + `",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "mcp": [{ "name": "shared", "transport": "stdio", "command": "bin/x.sh" }]
  }
}`,
			"bin/x.sh": "#!/bin/sh\n",
		})
		if err := os.Chmod(filepath.Join(root, "bin", "x.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		digest, err := ComputeDigest(root)
		if err != nil {
			t.Fatal(err)
		}
		src := &SourceIdentity{Type: SourceLocal, Path: root}
		m := mustReadManifest(t, root)
		// Merge into lockfile.
		lf, err := ReadLockfile(filepath.Join(gRoot, LockfileName))
		if err != nil {
			t.Fatal(err)
		}
		lf.Plugins[id] = LockfileEntry{
			Enabled: boolPtr(true),
			Digest:  digest,
			Source:  src,
			Trust: &TrustRecord{
				Digest:       digest,
				Source:       cloneSource(src),
				Capabilities: InferCapabilities(m),
			},
		}
		if err := WriteLockfile(filepath.Join(gRoot, LockfileName), lf); err != nil {
			t.Fatal(err)
		}
	}
	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.MCP) != 0 {
		t.Fatalf("collision should fail closed, got %+v", set.MCP)
	}
}

func TestUntrust_RemovesGrant(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeExecPlugin(t, root)
	digest, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	src := &SourceIdentity{Type: SourceLocal, Path: root}
	lf := emptyLockfile()
	lf.Plugins["acme.tools"] = LockfileEntry{
		Enabled: boolPtr(true),
		Digest:  digest,
		Source:  src,
		Trust: &TrustRecord{
			Digest:       digest,
			Source:       cloneSource(src),
			Capabilities: []string{CapMCPStdio, CapHarnesses, CapHooksCommand, CapHooksDeclarative},
		},
	}
	if err := WriteLockfile(filepath.Join(gRoot, LockfileName), lf); err != nil {
		t.Fatal(err)
	}
	if err := Untrust(TrustOptions{ID: "acme.tools", GlobalRoot: gRoot}); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"}, nil, nil)
	if len(set.MCP) != 0 {
		t.Fatal("untrust left MCP active")
	}
}

func TestMatchTrust_CapabilityGrowthInvalidates(t *testing.T) {
	src := &SourceIdentity{Type: SourceLocal, Path: "/tmp/p"}
	tr := &TrustRecord{
		Digest:       "sha256:" + strings.Repeat("a", 64),
		Source:       src,
		Capabilities: []string{CapMCPStdio},
	}
	m := MatchTrust(tr, tr.Digest, src, []string{CapMCPStdio, CapHarnesses})
	if m.OK || m.State != "stale" {
		t.Fatalf("got %+v", m)
	}
}

func TestInferCapabilities(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.caps",
  "version": "1.0.0",
  "name": "Caps",
  "strike": { "min": "0.1.0" },
  "capabilities": ["agents"],
  "contributions": {
    "mcp": [
      { "name": "a", "transport": "stdio", "command": "bin/a" },
      { "name": "b", "transport": "http", "url": "https://example.com" }
    ],
    "harnesses": [{ "name": "h", "command": "bin/h" }],
    "hooks": [
      { "event": "pre_tool_use", "command": "bin/h.sh" },
      { "event": "pre_tool_use", "action": "log" }
    ]
  }
}`
	m, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	caps := InferCapabilities(m)
	want := map[string]bool{
		"agents":            true,
		CapMCPStdio:         true,
		CapMCPHTTP:          true,
		CapHarnesses:        true,
		CapHooksCommand:     true,
		CapHooksDeclarative: true,
	}
	if len(caps) != len(want) {
		t.Fatalf("caps=%v", caps)
	}
	for _, c := range caps {
		if !want[c] {
			t.Fatalf("unexpected %q in %v", c, caps)
		}
	}
}

func mustReadManifest(t *testing.T, root string) Manifest {
	t.Helper()
	m, _, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
