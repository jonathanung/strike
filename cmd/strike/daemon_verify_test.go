package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/unit"
	"github.com/jonathanung/strike/internal/frontend/server"
)

// startHookCmd is a daemonCommander that runs a hook on every service
// command, so a test can simulate the service manager starting the daemon
// the installed unit describes.
type startHookCmd struct {
	calls   [][]string
	onStart func()
}

func (c *startHookCmd) CombinedOutput(name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, append([]string{name}, args...))
	if c.onStart != nil {
		c.onStart()
	}
	return nil, nil
}

func shrinkDaemonProbe(t *testing.T) {
	t.Helper()
	oldBudget, oldInterval := daemonProbeBudget, daemonProbeInterval
	daemonProbeBudget = 2 * time.Second
	daemonProbeInterval = 25 * time.Millisecond
	t.Cleanup(func() {
		daemonProbeBudget = oldBudget
		daemonProbeInterval = oldInterval
	})
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func serveOn(t *testing.T, opts server.Options) {
	t.Helper()
	srv, err := server.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}

// TestDaemonInstallStartVerifiesTokenAgreement is the #1679 acceptance
// test: after install --start, the token file the installed unit points
// serve at and the token source the CLI client reads must agree, and the
// daemon started from the unit must accept that token.
func TestDaemonInstallStartVerifiesTokenAgreement(t *testing.T) {
	shrinkDaemonProbe(t)
	host := testHost(t, "darwin")
	t.Setenv("HOME", host.Home)
	addr := freeLoopbackAddr(t)
	tokenPath := unit.TokenPath(host.Home)

	var once sync.Once
	cmd := &startHookCmd{onStart: func() {
		once.Do(func() {
			// Simulate launchd running the installed unit: read the
			// definition install wrote and serve with its --token-file.
			def, err := unit.DefinitionPath("darwin", host.Home, "")
			if err != nil {
				t.Error(err)
				return
			}
			body, err := os.ReadFile(def)
			if err != nil {
				t.Error(err)
				return
			}
			if !strings.Contains(string(body), "<string>--token-file</string>") ||
				!strings.Contains(string(body), "<string>"+tokenPath+"</string>") {
				t.Errorf("plist does not point serve at %s:\n%s", tokenPath, body)
				return
			}
			tok, err := readDaemonToken(tokenPath)
			if err != nil {
				t.Error(err)
				return
			}
			serveOn(t, server.Options{
				Addr:       addr,
				Auth:       true,
				Token:      tok,
				TokenFile:  tokenPath,
				SessionDir: t.TempDir(),
			})
		})
	}}

	var out, errb bytes.Buffer
	opts := daemonInstallOpts{addr: addr, start: true}
	if code := installDaemon(host, opts, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "Verified: the daemon at http://"+addr+" accepts the token in "+tokenPath) {
		t.Fatalf("install did not verify token agreement:\n%s", out.String())
	}

	// The client's token source (daemonTokenPath under this HOME) must be
	// the same file with the same contents, and the daemon must accept it.
	clientTok, _, err := loadOrMintTokenFile(daemonTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	fileTok, err := readDaemonToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if clientTok != fileTok {
		t.Fatalf("client token source %s disagrees with the installed unit's token file %s", daemonTokenPath(), tokenPath)
	}
	status, _, err := daemonAuthedJSON(http.MethodGet, "http://"+addr+"/v1/gateway", clientTok, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("daemon rejected the client token: HTTP %d", status)
	}
}

// TestDaemonInstallStartReportsTokenMismatch is the #1679 regression test:
// a stale serve already holding the address with a different token must
// make install --start fail loudly with remediation text, instead of
// leaving the user at a status board that only says "unauthorized".
func TestDaemonInstallStartReportsTokenMismatch(t *testing.T) {
	shrinkDaemonProbe(t)
	host := testHost(t, "darwin")
	addr := freeLoopbackAddr(t)

	// A stale serve holds the address with another token. The launchctl
	// commands "succeed" (the fake runs nothing), so the stale daemon is
	// the one that keeps answering — exactly the reported failure.
	serveOn(t, server.Options{
		Addr:       addr,
		Auth:       true,
		Token:      "0123456789abcdef0123456789abcdef",
		SessionDir: t.TempDir(),
	})
	cmd := &startHookCmd{}

	var out, errb bytes.Buffer
	opts := daemonInstallOpts{addr: addr, start: true}
	if code := installDaemon(host, opts, cmd, &out, &errb); code == 0 {
		t.Fatalf("install --start succeeded against a daemon with a different token\nstdout=%s", out.String())
	}
	for _, want := range []string{
		"does not accept the token in " + unit.TokenPath(host.Home),
		"strike daemon logout",
		"strike daemon install --start",
	} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr missing %q\n%s", want, errb.String())
		}
	}
}

// TestDaemonInstallStartUnreachableDaemonWarnsOnly pins the other half of
// the probe contract: a daemon that has not answered yet (the service
// manager may still be starting it) must not fail the install.
func TestDaemonInstallStartUnreachableDaemonWarnsOnly(t *testing.T) {
	shrinkDaemonProbe(t)
	host := testHost(t, "darwin")
	addr := freeLoopbackAddr(t)
	cmd := &startHookCmd{}

	var out, errb bytes.Buffer
	opts := daemonInstallOpts{addr: addr, start: true}
	if code := installDaemon(host, opts, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "could not verify the daemon") {
		t.Fatalf("expected an unverified-daemon warning:\n%s", errb.String())
	}
}
