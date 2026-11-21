package powers

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePack(t *testing.T, root, dir, manifest, mcp string, skills ...string) {
	t.Helper()
	writeFile(t, filepath.Join(root, dir, "plugin.json"), manifest)
	if mcp != "" {
		writeFile(t, filepath.Join(root, dir, "mcp.json"), mcp)
	}
	for _, s := range skills {
		writeFile(t, filepath.Join(root, dir, "skills", s, "SKILL.md"), "# "+s+"\n")
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "aws",
		`{"name":"aws","keywords":["AWS"," cloud  formation ","aws",""]}`,
		`{"mcpServers":{"aws-docs":{"command":"x"},"aws-api":{"command":"y"}}}`,
		"cdk", "iam")
	// A skills entry without SKILL.md is not a skill.
	if err := os.MkdirAll(filepath.Join(root, "aws", "skills", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := LoadDir(filepath.Join(root, "aws"), ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	want := Power{
		Name:     "aws",
		Keywords: []string{"aws", "cloud formation"},
		Servers:  []string{"aws-api", "aws-docs"},
		Skills:   []string{"cdk", "iam"},
		Dir:      filepath.Join(root, "aws"),
		Scope:    ScopeWorkspace,
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("LoadDir = %+v\nwant %+v", p, want)
	}
}

func TestLoadDirErrors(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		mcp      string
		wantErr  string
	}{
		{name: "missing manifest", wantErr: "plugin.json"},
		{name: "bad json", manifest: `{`, wantErr: "plugin.json"},
		{name: "bad name", manifest: `{"name":"Bad Name"}`, wantErr: "plugin.json"},
		{name: "mcp without servers", manifest: `{"name":"ok"}`, mcp: `{}`, wantErr: "missing mcpServers"},
		{name: "mcp bad json", manifest: `{"name":"ok"}`, mcp: `[`, wantErr: "mcp.json"},
		{name: "no mcp is fine", manifest: `{"name":"ok"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.manifest != "" {
				writeFile(t, filepath.Join(dir, "plugin.json"), tt.manifest)
			}
			if tt.mcp != "" {
				writeFile(t, filepath.Join(dir, "mcp.json"), tt.mcp)
			}
			_, err := LoadDir(dir, ScopeGlobal)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDiscoverWorkspaceWins(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePack(t, global, "db", `{"name":"db","keywords":["postgres"]}`, "")
	writePack(t, global, "web", `{"name":"web","keywords":["css"]}`, "")
	writePack(t, ws, "db-local", `{"name":"db","keywords":["sqlite"]}`, "")
	writePack(t, ws, "broken", `{`, "")
	writeFile(t, filepath.Join(ws, "README.md"), "not a pack")

	packs, errs := Discover(ws, global)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want one (broken pack)", errs)
	}
	if len(packs) != 2 || packs[0].Name != "db" || packs[1].Name != "web" {
		t.Fatalf("packs = %+v", packs)
	}
	if packs[0].Scope != ScopeWorkspace || packs[0].Keywords[0] != "sqlite" {
		t.Fatalf("workspace pack should win: %+v", packs[0])
	}
}

func TestDiscoverMissingRoots(t *testing.T) {
	packs, errs := Discover(filepath.Join(t.TempDir(), "nope"), "")
	if len(packs) != 0 || len(errs) != 0 {
		t.Fatalf("packs=%v errs=%v", packs, errs)
	}
}

func TestSuggest(t *testing.T) {
	packs := []Power{
		{Name: "aws", Keywords: []string{"aws", "cloud formation"}},
		{Name: "k8s", Keywords: []string{"k8s", "kubernetes"}},
		{Name: "next", Keywords: []string{"next.js"}},
		{Name: "muted", Keywords: []string{"deploy"}},
	}
	tests := []struct {
		name   string
		prompt string
		active map[string]bool
		want   []string
	}{
		{name: "word match case-insensitive", prompt: "Fix the AWS lambda", want: []string{"aws"}},
		{name: "substring does not match", prompt: "the laws of physics", want: nil},
		{name: "phrase", prompt: "update the Cloud Formation stack", want: []string{"aws"}},
		{name: "phrase split is not a match", prompt: "cloud and formation", want: nil},
		{name: "dotted keyword at sentence end", prompt: "migrate to next.js.", want: []string{"next"}},
		{name: "multiple packs in order", prompt: "deploy k8s on aws", want: []string{"aws", "k8s"}},
		{name: "active skipped", prompt: "aws and kubernetes", active: map[string]bool{"aws": true}, want: []string{"k8s"}},
		{name: "empty prompt", prompt: "  ", want: nil},
	}
	muted := map[string]bool{"muted": true}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Suggest(tt.prompt, packs, tt.active, muted)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Suggest(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

// fakeConn records live connections per session; it stands in for the MCP
// client boundary.
type fakeConn struct {
	live map[string]map[string]bool
	fail map[string]bool
	log  []string
}

func newFakeConn() *fakeConn {
	return &fakeConn{live: map[string]map[string]bool{}, fail: map[string]bool{}}
}

func (f *fakeConn) Connect(session, server string) error {
	f.log = append(f.log, "connect "+session+"/"+server)
	if f.fail[server] {
		return errors.New("dial " + server + ": connection refused")
	}
	if f.live[session] == nil {
		f.live[session] = map[string]bool{}
	}
	if f.live[session][server] {
		panic("double connect " + session + "/" + server)
	}
	f.live[session][server] = true
	return nil
}

func (f *fakeConn) Disconnect(session, server string) {
	f.log = append(f.log, "disconnect "+session+"/"+server)
	if !f.live[session][server] {
		panic("disconnect of non-live " + session + "/" + server)
	}
	delete(f.live[session], server)
}

func (f *fakeConn) isLive(session, server string) bool { return f.live[session][server] }

func TestOverlappingPacksShareServer(t *testing.T) {
	conn := newFakeConn()
	m := NewManager(conn, nil)
	a := Power{Name: "a", Servers: []string{"shared", "only-a"}, Skills: []string{"s"}}
	b := Power{Name: "b", Servers: []string{"shared"}, Skills: []string{"s"}}
	if _, err := m.Activate("s1", a); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("s1", b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("s1", b); err == nil {
		t.Fatal("double activation should fail")
	}
	if err := m.Deactivate("s1", "a"); err != nil {
		t.Fatal(err)
	}
	if !conn.isLive("s1", "shared") || conn.isLive("s1", "only-a") {
		t.Fatalf("after deactivating a: live = %v", conn.live["s1"])
	}
	if got := m.Servers("s1"); !reflect.DeepEqual(got, []string{"shared"}) {
		t.Fatalf("Servers = %v", got)
	}
	if got := m.Skills("s1"); !reflect.DeepEqual(got, []string{"s"}) {
		t.Fatalf("Skills = %v", got)
	}
	if err := m.Deactivate("s1", "b"); err != nil {
		t.Fatal(err)
	}
	if conn.isLive("s1", "shared") {
		t.Fatal("last reference released; shared should be disconnected")
	}
	if m.Servers("s1") != nil || m.Skills("s1") != nil || m.Active("s1") != nil {
		t.Fatal("session state should be empty")
	}
	if err := m.Deactivate("s1", "b"); err == nil {
		t.Fatal("deactivating an inactive pack should fail")
	}
}

func TestSessionEndIsolated(t *testing.T) {
	conn := newFakeConn()
	m := NewManager(conn, nil)
	p := Power{Name: "p", Servers: []string{"srv"}}
	q := Power{Name: "q", Servers: []string{"srv", "other"}}
	for _, s := range []string{"s1", "s2"} {
		if _, err := m.Activate(s, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Activate("s1", q); err != nil {
		t.Fatal(err)
	}
	m.EndSession("s1")
	if conn.isLive("s1", "srv") || conn.isLive("s1", "other") {
		t.Fatalf("s1 servers should be gone: %v", conn.live["s1"])
	}
	if !conn.isLive("s2", "srv") {
		t.Fatal("ending s1 must not disconnect s2's server")
	}
	if got := m.Active("s2"); len(got) != 1 || got[0].Name != "p" {
		t.Fatalf("s2 active = %+v", got)
	}
	m.EndSession("s1") // idempotent
}

func TestPartialConnectFailure(t *testing.T) {
	conn := newFakeConn()
	conn.fail["broken"] = true
	m := NewManager(conn, nil)
	res, err := m.Activate("s1", Power{Name: "p", Servers: []string{"broken", "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Admitted, []string{"ok"}) {
		t.Fatalf("admitted = %v", res.Admitted)
	}
	if !strings.Contains(res.Failed["broken"], "connection refused") {
		t.Fatalf("failed = %v", res.Failed)
	}
	// Chip (Active) and tool set (Servers) agree on what was admitted.
	act := m.Active("s1")
	if len(act) != 1 || !reflect.DeepEqual(act[0].Admitted, m.Servers("s1")) {
		t.Fatalf("active %+v vs servers %v", act, m.Servers("s1"))
	}
	if err := m.Deactivate("s1", "p"); err != nil {
		t.Fatal(err) // must not Disconnect the never-connected server (fake panics)
	}
}

func TestDeniedServerKeepsPackInactive(t *testing.T) {
	conn := newFakeConn()
	deny := func(_, server string) (bool, string) {
		if server == "exfil" {
			return true, "deny rule mcp:exfil"
		}
		return false, ""
	}
	m := NewManager(conn, deny)
	_, err := m.Activate("s1", Power{Name: "p", Servers: []string{"ok", "exfil"}})
	if !errors.Is(err, ErrDenied) || !strings.Contains(err.Error(), "deny rule mcp:exfil") {
		t.Fatalf("err = %v", err)
	}
	if len(conn.log) != 0 {
		t.Fatalf("nothing should connect for a denied pack: %v", conn.log)
	}
	if m.Active("s1") != nil {
		t.Fatal("denied pack must stay inactive")
	}
}

func TestActivateValidation(t *testing.T) {
	m := NewManager(newFakeConn(), nil)
	if _, err := m.Activate("", Power{Name: "p"}); err == nil {
		t.Fatal("empty session should fail")
	}
	if _, err := m.Activate("s", Power{}); err == nil {
		t.Fatal("empty pack name should fail")
	}
}

func TestActiveReturnsCopies(t *testing.T) {
	m := NewManager(newFakeConn(), nil)
	if _, err := m.Activate("s", Power{Name: "p", Servers: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	got := m.Active("s")
	got[0].Admitted[0] = "mutated"
	if m.Active("s")[0].Admitted[0] != "a" {
		t.Fatal("Active leaked internal state")
	}
}
