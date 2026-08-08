package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/server"
	"github.com/jonathanung/strike-cli/internal/session"
)

func TestParseServeArgs(t *testing.T) {
	opts, err := parseServeArgs([]string{"--addr", "127.0.0.1:0", "--auth", "--token", "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.addr != "127.0.0.1:0" || opts.token != "abc" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.sessionDir != session.DefaultDir() {
		t.Fatalf("sessionDir = %q, want default", opts.sessionDir)
	}
	if opts.provider != "echo" || opts.attachOnly || opts.readOnly || opts.dangerouslySkipPermissions {
		t.Fatalf("defaults provider/attach/readOnly/danger = %+v", opts)
	}

	opts, err = parseServeArgs([]string{"--read-only"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.readOnly {
		t.Fatalf("--read-only not set: %+v", opts)
	}

	opts, err = parseServeArgs([]string{"--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.dangerouslySkipPermissions {
		t.Fatalf("--auto should set dangerouslySkipPermissions: %+v", opts)
	}
	opts, err = parseServeArgs([]string{"--dangerously-skip-permissions"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.dangerouslySkipPermissions {
		t.Fatalf("--dangerously-skip-permissions should set dangerouslySkipPermissions: %+v", opts)
	}

	opts, err = parseServeArgs([]string{"--session-dir", "/tmp/sessions", "--provider", "anthropic", "--attach-only"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sessionDir != "/tmp/sessions" || opts.provider != "anthropic" || !opts.attachOnly {
		t.Fatalf("opts = %+v", opts)
	}
	_, err = parseServeArgs([]string{"--session-dir", session.DefaultDir()})
	if err == nil || !strings.Contains(err.Error(), "--attach-only") {
		t.Fatalf("explicit live --session-dir err = %v", err)
	}

	_, err = parseServeArgs([]string{"--auth", "--expose", "--token", "t"})
	if err == nil || !strings.Contains(err.Error(), "--expose was removed") {
		t.Fatalf("legacy --expose err = %v", err)
	}
	_, err = parseServeArgs([]string{"--allow-cidr", "10.0.0.0/8"})
	if err == nil || !strings.Contains(err.Error(), "--expose was removed") {
		t.Fatalf("legacy --allow-cidr err = %v", err)
	}

	_, err = parseServeArgs([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help err = %v", err)
	}

	_, err = parseServeArgs([]string{"extra"})
	if err == nil {
		t.Fatal("want unexpected arg error")
	}
}

func TestRunCLIServeHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"serve", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "strike serve") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "/health") {
		t.Fatalf("usage missing /health: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "/v1/ws") {
		t.Fatalf("usage missing /v1/ws: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "--expose") {
		t.Fatalf("usage must not advertise --expose: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ssh -L") {
		t.Fatalf("usage missing ssh -L remote path: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--auto") || !strings.Contains(stdout.String(), "--dangerously-skip-permissions") {
		t.Fatalf("usage missing --auto / --dangerously-skip-permissions: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--read-only") {
		t.Fatalf("usage missing --read-only: %q", stdout.String())
	}
}

func TestRunCLIServeDefaultsToLocalNoAuth(t *testing.T) {
	opts, err := parseServeArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.token != "" {
		t.Fatalf("token = %q, want empty", opts.token)
	}
	if opts.addr != "127.0.0.1:8787" {
		t.Fatalf("addr = %q", opts.addr)
	}
}

func TestServeResolveLoopbackOnly(t *testing.T) {
	opts, err := parseServeArgs([]string{"--addr", "0.0.0.0:8787", "--auth", "--token", "t", "--attach-only"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.ResolveBindAddr(opts.addr)
	if err == nil {
		t.Fatal("want resolve error for non-loopback")
	}
	if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "ssh -L") {
		t.Fatalf("resolve err = %v", err)
	}

	opts, err = parseServeArgs([]string{"--auth", "--token", "t", "--attach-only"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := server.ResolveBindAddr(opts.addr)
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:8787" {
		t.Fatalf("got %q", got)
	}
}

func TestPrintServeBanner(t *testing.T) {
	var buf bytes.Buffer
	printServeBanner(&buf, serveBanner{
		listenAddr: "127.0.0.1:8787",
		token:      "tok123",
		auth:       true,
		minted:     true,
		sessionDir: "/tmp/s",
	})
	out := buf.String()
	if !strings.Contains(out, "tok123") {
		t.Fatalf("missing token in banner: %s", out)
	}
	if !strings.Contains(out, "ssh -L") {
		t.Fatalf("missing remote ssh hint: %s", out)
	}
	if strings.Contains(out, "EXPOSE") {
		t.Fatalf("banner must not advertise EXPOSE: %s", out)
	}
}

func TestRunServeRejectsNonLocal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runServe(serveOptions{
		addr:       "0.0.0.0:0",
		auth:       true,
		token:      "test-token",
		attachOnly: true,
		sessionDir: t.TempDir(),
		provider:   "echo",
	}, &stdout, &stderr)
	if err == nil || (!strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "ssh -L") && !strings.Contains(err.Error(), "non-localhost")) {
		t.Fatalf("err = %v", err)
	}
}
