package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBwrapArgsOneWorkspaceAndNoNet(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, workspaceName), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte(fallbackProbe), 0o755); err != nil {
		t.Fatal(err)
	}
	strike := filepath.Join(dir, "strike")
	if err := os.WriteFile(strike, []byte("not-a-real-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rpc := range []bool{false, true} {
		args, err := bwrapArgs(dir, strike, probe, rpc)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(args, "\x00")
		if strings.Contains(text, "--share-net") {
			t.Fatal("share-net")
		}
		if !strings.Contains(text, "--unshare-all") {
			t.Fatal("missing unshare-all")
		}
		if strings.Contains(text, filepath.Join(dir, "secrets")) || strings.Contains(text, "host-canary") {
			t.Fatal("host state leaked into args")
		}
		if rpc {
			if !strings.Contains(text, "/strike\x00rpc\x00--provider\x00echo\x00--auto") &&
				!strings.Contains(text, "exec /strike rpc --provider echo --auto") {
				t.Fatalf("rpc command missing: %q", args)
			}
		}
	}
}

func TestBwrapArgsRejectsExtraBind(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, workspaceName)
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--bind", ws, "/workspace", "--bind", dir, "/host"}
	if err := auditBwrapArgs(dir, "", "", args); err == nil {
		t.Fatal("expected extra bind to fail")
	}
}
