package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Mute state and last-used times survive a restart (#1943).
func TestScopedPowersPersistMuteAndLastUsed(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePowerPack(t, global, "docs", `"docs"`, `{"mcpServers":{"docs-srv":{"command":"x"}}}`)
	statePath := filepath.Join(t.TempDir(), "powers-state.json")
	conn := &recConn{fail: map[string]bool{}, live: map[string]bool{}}

	p, _ := NewScopedPowers(ws, global, statePath, conn, nil)
	if err := p.SetMuted("docs", true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Activate("s1", "docs"); err != nil {
		t.Fatal(err)
	}

	// A fresh adapter (restart) reads both back.
	p2, _ := NewScopedPowers(ws, global, statePath, conn, nil)
	packs, loadErrs := p2.Packs()
	if len(loadErrs) != 0 {
		t.Fatalf("load errors = %v", loadErrs)
	}
	if len(packs) != 1 || !packs[0].Muted {
		t.Fatalf("mute did not persist: %+v", packs)
	}
	if packs[0].LastUsed == nil || time.Since(*packs[0].LastUsed) > time.Hour {
		t.Fatalf("last-used did not persist: %+v", packs[0].LastUsed)
	}
	if got := p2.Suggest("s2", "docs please"); len(got) != 0 {
		t.Fatalf("muted pack suggested after restart: %v", got)
	}

	if err := p2.SetMuted("docs", false); err != nil {
		t.Fatal(err)
	}
	p3, _ := NewScopedPowers(ws, global, statePath, conn, nil)
	if packs, _ := p3.Packs(); packs[0].Muted {
		t.Fatal("unmute did not persist")
	}
}

func TestScopedPowersStateErrors(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePowerPack(t, global, "docs", `"docs"`, "")
	conn := &recConn{fail: map[string]bool{}, live: map[string]bool{}}

	// A corrupt state file is reported, and Powers still work.
	corrupt := filepath.Join(t.TempDir(), "powers-state.json")
	if err := os.WriteFile(corrupt, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, _ := NewScopedPowers(ws, global, corrupt, conn, nil)
	packs, loadErrs := p.Packs()
	if len(packs) != 1 || len(loadErrs) != 1 {
		t.Fatalf("packs=%v loadErrs=%v, want the pack plus one state error", packs, loadErrs)
	}

	// A mute that cannot be saved is refused and not kept in memory, so it
	// cannot silently vanish on restart.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p2, _ := NewScopedPowers(ws, global, filepath.Join(blocker, "powers-state.json"), conn, nil)
	if err := p2.SetMuted("docs", true); err == nil {
		t.Fatal("SetMuted succeeded with an unwritable state path")
	}
	if packs, _ := p2.Packs(); packs[0].Muted {
		t.Fatal("unsaved mute kept in memory")
	}
}

// Activating a pack in one session supplies its skills and servers to that
// session only, and only while it is active (#1943).
func TestPowersScopeSkillsAndToolsPerSession(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePowerPack(t, ws, "aws", `"aws"`, `{"mcpServers":{"aws-docs":{"command":"x"},"broken":{"command":"y"}}}`)
	conn := &recConn{fail: map[string]bool{"broken": true}, live: map[string]bool{}}
	p, scope := NewScopedPowers(ws, global, "", conn, nil)

	scopedServers := map[string]bool{"aws-docs": true, "broken": true}
	toolServer := map[string]string{
		"mcp_aws_docs_search": "aws-docs",
		"mcp_broken_x":        "broken",
		"mcp_global_read":     "global", // configured outside Powers
	}
	visible := PowersToolVisible(scope, func(tool string) (string, bool) {
		srv, ok := toolServer[tool]
		if !ok || !scopedServers[srv] {
			return "", false
		}
		return srv, true
	})

	if got := scope.Skills("s1"); len(got) != 0 {
		t.Fatalf("skills before activation = %v", got)
	}
	res, err := p.Activate("s1", "aws")
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed["broken"] == "" {
		t.Fatalf("activation should report the failed server: %+v", res)
	}

	tests := []struct {
		session, tool string
		want          bool
	}{
		{"s1", "mcp_aws_docs_search", true},
		{"s2", "mcp_aws_docs_search", false}, // the other session never sees it
		{"s1", "mcp_broken_x", false},        // failed server is not in the set
		{"s2", "mcp_global_read", true},      // shared config stays shared
		{"s2", "read", true},
	}
	for _, tt := range tests {
		if got := visible(tt.session, tt.tool); got != tt.want {
			t.Errorf("visible(%s, %s) = %v, want %v", tt.session, tt.tool, got, tt.want)
		}
	}

	skills := scope.Skills("s1")
	if len(skills) != 1 || skills[0].Name != "aws-skill" || skills[0].Template == "" || skills[0].Path == "" {
		t.Fatalf("s1 skills = %+v", skills)
	}
	if got := scope.Skills("s2"); len(got) != 0 {
		t.Fatalf("s2 got s1's pack skills: %v", got)
	}

	if err := p.Deactivate("s1", "aws"); err != nil {
		t.Fatal(err)
	}
	if visible("s1", "mcp_aws_docs_search") {
		t.Error("tool still visible after deactivate")
	}
	if got := scope.Skills("s1"); len(got) != 0 {
		t.Errorf("skills still loaded after deactivate: %v", got)
	}

	var nilScope *PowersScope
	if nilScope.Servers("s1") != nil || nilScope.Skills("s1") != nil {
		t.Error("nil scope should report nothing")
	}
}
