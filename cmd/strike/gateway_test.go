package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/server"
)

func TestGatewayURLPrintsQRWithoutDaemonToken(t *testing.T) {
	const token = "daemon-secret-do-not-leak"
	srv, err := server.New(server.Options{
		Auth:       true,
		Token:      token,
		Addr:       "127.0.0.1:8787",
		SessionDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(srv.Handler())
	t.Cleanup(remote.Close)
	path := filepath.Join(t.TempDir(), "daemon.token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runGatewayCLI([]string{"url", "--qr", "--addr", remote.URL, "--token-file", path}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	text := out.String()
	if strings.Contains(text, token) || strings.Contains(text, "?token=") {
		t.Fatalf("output leaked the daemon token:\n%s", text)
	}
	if !strings.Contains(text, "/#pair=p1.") {
		t.Fatalf("missing pairing url:\n%s", text)
	}
	if !strings.Contains(text, "█") && !strings.Contains(text, "▀") && !strings.Contains(text, "▄") {
		t.Fatalf("missing QR:\n%s", text)
	}
	if !strings.Contains(text, "A phone cannot open") || !strings.Contains(text, "--allow-private-bind") {
		t.Fatalf("missing opt-in:\n%s", text)
	}
}

func TestGatewayURLMissingTokenFileDoesNotMint(t *testing.T) {
	var out, errb bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.token")
	code := runGatewayCLI([]string{"url", "--token-file", path}, &out, &errb)
	if code == 0 {
		t.Fatal("expected failure")
	}
	if strings.Contains(errb.String(), "p1.") {
		t.Fatalf("stderr minted a code: %s", errb.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("token file should not be created")
	}
}

func TestGatewayUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runGatewayCLI(nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "strike gateway url") || !strings.Contains(out.String(), "#pair=") {
		t.Fatalf("usage = %q", out.String())
	}
}
