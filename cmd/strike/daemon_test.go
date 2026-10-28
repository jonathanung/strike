package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrMintTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "daemon.token")
	tok, created, err := loadOrMintTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created || len(tok) < 16 {
		t.Fatalf("mint: created=%v tok=%q", created, tok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perm = %v, want 0600", perm)
	}
	again, created, err := loadOrMintTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if created || again != tok {
		t.Fatalf("reload: created=%v again=%q want %q", created, again, tok)
	}
}

func TestDaemonLogoutRotatesToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errb bytes.Buffer
	if code := runDaemonCLI([]string{"token"}, &out, &errb); code != 0 {
		t.Fatalf("token code = %d err=%s", code, errb.String())
	}
	first := strings.TrimSpace(out.String())
	out.Reset()
	if code := runDaemonCLI([]string{"logout"}, &out, &errb); code != 0 {
		t.Fatalf("logout code = %d err=%s", code, errb.String())
	}
	second := ""
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "Rotated") && !strings.HasPrefix(line, "A serve") {
			second = line
			break
		}
	}
	if second == "" || second == first {
		t.Fatalf("logout token = %q, first = %q", second, first)
	}
	info, err := os.Stat(filepath.Join(home, ".strike", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(home, ".strike", "usage-audit", "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), first) || strings.Contains(string(raw), second) {
		t.Fatalf("audit log contains a token: %s", raw)
	}
	if !strings.Contains(string(raw), "daemon token file rotated") {
		t.Fatalf("audit log = %s", raw)
	}
}

func TestRunDaemonCLIUsageAndUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runDaemonCLI(nil, &out, &errb); code != 0 {
		t.Fatalf("usage code = %d", code)
	}
	if !strings.Contains(out.String(), "strike daemon install") {
		t.Fatalf("usage output: %q", out.String())
	}
	out.Reset()
	if code := runDaemonCLI([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("unknown code = %d, want 2", code)
	}
}

func TestBareStrikePrintsHint(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCLI(nil, &out, &errb); code != 0 {
		t.Fatalf("bare code = %d", code)
	}
	if !strings.Contains(out.String(), "strike daemon install") || !strings.Contains(out.String(), "strike tui") {
		t.Fatalf("bare hint: %q", out.String())
	}
}
