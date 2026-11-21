package local

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/harness/permission"
)

func writePowerPack(t *testing.T, root, name, keywords, mcpJSON string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "skills", name+"-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json": `{"name":"` + name + `","keywords":[` + keywords + `]}`,
		filepath.Join("skills", name+"-skill", "SKILL.md"): "# skill\n",
	}
	if mcpJSON != "" {
		files["mcp.json"] = mcpJSON
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type recConn struct {
	mu   sync.Mutex
	fail map[string]bool
	live map[string]bool
}

func (c *recConn) Connect(session, server string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail[server] {
		return errors.New("mcp " + server + ": connection refused")
	}
	c.live[session+"/"+server] = true
	return nil
}

func (c *recConn) Disconnect(session, server string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.live, session+"/"+server)
}

func (c *recConn) isLive(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live[key]
}

func TestPowersAdapterLifecycle(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePowerPack(t, ws, "aws", `"aws","cloudformation"`, `{"mcpServers":{"aws-docs":{"command":"x"},"broken":{"command":"y"}}}`)
	writePowerPack(t, global, "docs", `"docs"`, `{"mcpServers":{"aws-docs":{"command":"x"}}}`)
	if err := os.MkdirAll(filepath.Join(global, "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	conn := &recConn{fail: map[string]bool{"broken": true}, live: map[string]bool{}}
	p := NewPowers(ws, global, conn, nil)

	packs, loadErrs := p.Packs()
	if len(packs) != 2 || packs[0].Name != "aws" || packs[0].Scope != "workspace" || packs[1].Scope != "global" {
		t.Fatalf("packs = %+v", packs)
	}
	if len(loadErrs) != 1 {
		t.Fatalf("a pack without plugin.json should be a load error: %v", loadErrs)
	}
	if packs[0].LastUsed != nil {
		t.Fatal("unused pack has a last-used time")
	}

	if got := p.Suggest("s1", "Deploy the AWS stack"); !reflect.DeepEqual(got, []string{"aws"}) {
		t.Fatalf("suggest = %v", got)
	}

	res, err := p.Activate("s1", "aws")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Admitted, []string{"aws-docs"}) || !strings.Contains(res.Failed["broken"], "refused") {
		t.Fatalf("activation = %+v", res)
	}
	if got := p.Suggest("s1", "aws"); len(got) != 0 {
		t.Fatalf("active pack suggested again: %v", got)
	}
	if _, err := p.Activate("s1", "docs"); err != nil {
		t.Fatal(err)
	}
	// Two packs share aws-docs: deactivating one keeps the server.
	if err := p.Deactivate("s1", "aws"); err != nil {
		t.Fatal(err)
	}
	if !conn.isLive("s1/aws-docs") {
		t.Fatal("shared server dropped before the last pack deactivated")
	}
	if act := p.Active("s1"); len(act) != 1 || act[0].Name != "docs" {
		t.Fatalf("active = %+v", act)
	}
	p.EndSession("s1")
	if conn.isLive("s1/aws-docs") || len(p.Active("s1")) != 0 {
		t.Fatal("EndSession should unload everything")
	}

	if packs, _ := p.Packs(); packs[0].LastUsed == nil {
		t.Fatal("activated pack should report last-used time")
	}

	if err := p.SetMuted("aws", true); err != nil {
		t.Fatal(err)
	}
	if got := p.Suggest("s2", "aws"); len(got) != 0 {
		t.Fatalf("muted pack suggested: %v", got)
	}
	if packs, _ := p.Packs(); !packs[0].Muted {
		t.Fatal("Packs should report muted")
	}
	if err := p.SetMuted("aws", false); err != nil {
		t.Fatal(err)
	}
	if got := p.Suggest("s2", "aws"); len(got) != 1 {
		t.Fatalf("unmuted pack not suggested: %v", got)
	}
	if err := p.SetMuted("nope", true); err == nil {
		t.Fatal("muting an unknown pack should fail")
	}
	if _, err := p.Activate("s2", "nope"); err == nil {
		t.Fatal("activating an unknown pack should fail")
	}
}

func TestPowerServerConfig(t *testing.T) {
	global := t.TempDir()
	writePowerPack(t, global, "a", `"a"`, `{"mcpServers":{"srv":{"command":"run-a","args":["--x"]},"remote":{"url":"https://mcp.example/mcp"},"bad":{}}}`)
	writePowerPack(t, global, "b", `"b"`, `{"mcpServers":{"srv":{"command":"run-b"}}}`)
	resolve := PowerServerConfig("", global, "/work")

	cfg, ok := resolve("srv")
	if !ok || cfg.Command != "run-a" || cfg.WorkDir != "/work" || !reflect.DeepEqual(cfg.Args, []string{"--x"}) {
		t.Fatalf("srv = %+v %v", cfg, ok)
	}
	if cfg, ok := resolve("remote"); !ok || cfg.URL != "https://mcp.example/mcp" {
		t.Fatalf("remote = %+v %v", cfg, ok)
	}
	if _, ok := resolve("bad"); ok {
		t.Fatal("an entry without command or url should not resolve")
	}
	if _, ok := resolve("missing"); ok {
		t.Fatal("unknown server should not resolve")
	}
}

func TestPowersDeny(t *testing.T) {
	deny := PowersDeny([]permission.Ruleset{{
		{Permission: "mcp", Pattern: "exfil/*", Action: permission.Deny},
		{Permission: "mcp", Pattern: "docs/delete", Action: permission.Deny},
	}})
	if denied, reason := deny("s", "exfil"); !denied || !strings.Contains(reason, "exfil") {
		t.Fatalf("exfil = %v %q", denied, reason)
	}
	if denied, _ := deny("s", "docs"); denied {
		t.Fatal("a single-tool deny must not deny the whole server")
	}
}
