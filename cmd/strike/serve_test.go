package main

import (
	"bytes"
	"errors"
	"flag"
	"net"
	"strconv"
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
	if opts.provider != "echo" || opts.attachOnly || opts.expose || opts.dangerouslySkipPermissions {
		t.Fatalf("defaults provider/attach/expose/danger = %+v", opts)
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

	opts, err = parseServeArgs([]string{"--auth", "--expose", "--allow-cidr", "192.168.0.0/16", "--allow-cidr", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.expose || len(opts.allowCIDR) != 2 {
		t.Fatalf("expose/cidr opts = %+v", opts)
	}

	opts, err = parseServeArgs([]string{"--auth", "--expose", "--allow-cidr", "10.0.0.0/8,172.16.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.allowCIDR) != 2 {
		t.Fatalf("comma cidr = %v", opts.allowCIDR)
	}

	_, err = parseServeArgs([]string{"--allow-cidr", "10.0.0.0/8"})
	if err == nil || !strings.Contains(err.Error(), "--expose") {
		t.Fatalf("allow-cidr without expose err = %v", err)
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
	if !strings.Contains(stdout.String(), "--expose") {
		t.Fatalf("usage missing --expose: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--auto") || !strings.Contains(stdout.String(), "--dangerously-skip-permissions") {
		t.Fatalf("usage missing --auto / --dangerously-skip-permissions: %q", stdout.String())
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

func TestServeResolveExposeGuard(t *testing.T) {
	opts, err := parseServeArgs([]string{"--addr", "0.0.0.0:8787", "--auth", "--token", "t", "--attach-only"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.ResolveBindAddr(opts.addr, opts.expose)
	if err == nil {
		t.Fatal("want resolve error without --expose")
	}

	opts, err = parseServeArgs([]string{"--auth", "--expose", "--token", "t", "--attach-only"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := server.ResolveBindAddr(opts.addr, opts.expose)
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.0.0.0:8787" {
		t.Fatalf("got %q", got)
	}
}

func TestCockpitURL(t *testing.T) {
	u := cockpitURL("192.168.1.10", "8787", "sec ret")
	if !strings.HasPrefix(u, "http://192.168.1.10:8787/attach?token=") {
		t.Fatalf("url = %q", u)
	}
	if !strings.Contains(u, "sec+ret") && !strings.Contains(u, "sec%20ret") {
		t.Fatalf("token not escaped: %q", u)
	}
}

func TestPrintServeBannerExpose(t *testing.T) {
	var buf bytes.Buffer
	printServeBanner(&buf, serveBanner{
		listenAddr: "0.0.0.0:8787",
		port:       "8787",
		token:      "tok123",
		auth:       true,
		minted:     true,
		exposed:    true,
		sessionDir: "/tmp/s",
		allowCIDR:  []string{"192.168.0.0/16"},
	})
	out := buf.String()
	if !strings.Contains(out, "EXPOSE") {
		t.Fatalf("missing EXPOSE: %s", out)
	}
	if !strings.Contains(out, "tok123") {
		t.Fatalf("missing token in banner: %s", out)
	}
	if !strings.Contains(out, "192.168.0.0/16") {
		t.Fatalf("missing allow: %s", out)
	}
}

func TestWriteExposeWarning(t *testing.T) {
	var buf bytes.Buffer
	writeExposeWarning(&buf)
	if !strings.Contains(buf.String(), "WARNING") || !strings.Contains(buf.String(), "NO TLS") {
		t.Fatalf("warning = %q", buf.String())
	}
}

func TestRunServeRejectsNonLocalWithoutExpose(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runServe(serveOptions{
		addr:       "0.0.0.0:0",
		auth:       true,
		token:      "test-token",
		attachOnly: true,
		sessionDir: t.TempDir(),
		provider:   "echo",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--expose") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeExposeResolvePortPreserved(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	got, err := server.ResolveBindAddr(addr, false)
	if err != nil || got != addr {
		t.Fatalf("resolve localhost = %q err=%v", got, err)
	}
	got, err = server.ResolveBindAddr(addr, true)
	if err != nil {
		t.Fatal(err)
	}
	want := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	if got != want {
		t.Fatalf("expose resolve = %q, want %q", got, want)
	}
}
