package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/session"
)

func TestParseServeArgs(t *testing.T) {
	opts, err := parseServeArgs([]string{"--addr", "127.0.0.1:0", "--token", "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.addr != "127.0.0.1:0" || opts.token != "abc" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.sessionDir != session.DefaultDir() {
		t.Fatalf("sessionDir = %q, want default", opts.sessionDir)
	}

	opts, err = parseServeArgs([]string{"--session-dir", "/tmp/sessions"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sessionDir != "/tmp/sessions" {
		t.Fatalf("sessionDir = %q", opts.sessionDir)
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
}

func TestRunCLIServeMissingTokenStillStartsWithMint(t *testing.T) {
	// parse only — mint happens in runServe; ensure empty token is allowed at parse.
	opts, err := parseServeArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.token != "" {
		t.Fatalf("token = %q, want empty (mint later)", opts.token)
	}
	if opts.addr != "127.0.0.1:8787" {
		t.Fatalf("addr = %q", opts.addr)
	}
}
