package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

func TestRunTrustCLIGrantStatusRevokeList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	canon, err := workspacetrust.CanonicalRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runCLI(append([]string{"trust"}, args...), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, out, errOut := run("status", "--dir", repo)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"State: untrusted", "evil: npx -y evil", "repo-a (", "AGENTS.md", "permission rules: 1", "pre_tool_use: echo hook"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Error("status leaked an MCP env value")
	}

	code, out, errOut = run("list")
	if code != 0 || !strings.Contains(out, "No trusted workspaces") {
		t.Fatalf("list exit=%d out=%q stderr=%q", code, out, errOut)
	}

	// Bare `strike trust --dir` grants.
	code, out, errOut = run("--dir", repo)
	if code != 0 {
		t.Fatalf("grant exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "Trusted "+canon) || !strings.Contains(out, "does not widen permissions") {
		t.Errorf("grant out = %q", out)
	}
	store, err := workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if !store.Trusted(repo) {
		t.Fatal("grant did not persist")
	}

	code, out, _ = run("status", "--dir", repo)
	if code != 0 || !strings.Contains(out, "State: trusted since ") {
		t.Fatalf("status after grant exit=%d out=%q", code, out)
	}
	code, out, _ = run("list")
	if code != 0 || !strings.Contains(out, canon) {
		t.Fatalf("list after grant exit=%d out=%q", code, out)
	}

	code, out, errOut = run("revoke", "--dir", repo)
	if code != 0 || !strings.Contains(out, "Revoked trust for "+canon) {
		t.Fatalf("revoke exit=%d out=%q stderr=%q", code, out, errOut)
	}
	store, err = workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if store.Trusted(repo) {
		t.Fatal("revoke did not persist")
	}
}

func TestRunTrustCLIDefaultsToCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	t.Chdir(repo)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"trust", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	canon, _ := workspacetrust.CanonicalRoot(repo)
	if !strings.Contains(stdout.String(), "Workspace: "+canon) || !strings.Contains(stdout.String(), "no repo-supplied config") {
		t.Errorf("out = %q", stdout.String())
	}
}

func TestRunTrustCLIRefusesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"trust", "--dir", home}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be trusted") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(workspacetrust.DefaultPath(filepath.Join(home, ".strike"))); err == nil {
		t.Error("refused grant wrote the store")
	}
}

func TestRunTrustCLIUsageAndErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string // substring in stdout (code 0) or stderr
	}{
		{"help", []string{"trust", "help"}, 0, "strike trust status"},
		{"-h", []string{"trust", "-h"}, 0, "Usage:"},
		{"unknown sub", []string{"trust", "bogus"}, 2, "unknown subcommand"},
		{"extra arg", []string{"trust", "status", "extra"}, 2, "unexpected arguments"},
		{"bad flag", []string{"trust", "--nope"}, 2, "flag provided but not defined"},
		{"missing dir", []string{"trust", "status", "--dir", filepath.Join(home, "does-not-exist")}, 1, "strike trust:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit=%d want %d; stdout=%q stderr=%q", code, tc.wantCode, stdout.String(), stderr.String())
			}
			got := stderr.String()
			if tc.wantCode == 0 {
				got = stdout.String()
			}
			if !strings.Contains(got, tc.wantOut) {
				t.Errorf("output %q missing %q", got, tc.wantOut)
			}
		})
	}
}

func TestUsageListsTrust(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf)
	if !strings.Contains(buf.String(), "strike trust [grant|status|revoke|list]") {
		t.Errorf("usage missing trust line:\n%s", buf.String())
	}
}
