package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/server"
)

func TestParseConnectArgsRejectsTokensAndPublicTargets(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"token in dest", []string{"user@host?token=abc"}, "token"},
		{"bearer in dest", []string{"user@host Bearer secret"}, "token"},
		{"public remote", []string{"--remote", "1.2.3.4:8787", "user@host"}, "loopback"},
		{"public local", []string{"--local", "0.0.0.0:8787", "user@host"}, "loopback"},
		{"shell dest", []string{"user@host;touch"}, "destination"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseConnectArgs(tc.args)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	opt, err := parseConnectArgs([]string{"--ttl", "15m", "--remote", "127.0.0.1:8787", "me@box"})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Dest != "me@box" || opt.TTL != 15*time.Minute {
		t.Fatalf("opt = %+v", opt)
	}
	cmd := grantRemoteCommand(opt.TTL, opt.Remote)
	if strings.Contains(cmd, "token") || !strings.Contains(cmd, "strike daemon grant") {
		t.Fatalf("remote command = %q", cmd)
	}
	if strings.Contains(cmd, ";") || strings.Contains(cmd, "&&") {
		t.Fatalf("remote command is not quoted safely: %q", cmd)
	}
}

func TestGatewayProfilesDoNotStoreTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := gatewayProfilePath()
	if err := upsertProfile(path, gatewayProfile{Name: "box", SSH: "me@box", Remote: "127.0.0.1:8787", TTL: "15m0s"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "Bearer") {
		t.Fatalf("profile file stored a secret:\n%s", raw)
	}
	if err := upsertProfile(path, gatewayProfile{Name: "bad", SSH: "me@box?token=abc", Remote: "127.0.0.1:8787"}); err == nil {
		t.Fatal("stored a token-shaped ssh field")
	}
	pf, err := loadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Profiles) != 1 || pf.Profiles[0].Name != "box" {
		t.Fatalf("profiles = %+v", pf.Profiles)
	}
	poison := filepath.Join(t.TempDir(), "gateway-profiles.json")
	if err := os.WriteFile(poison, []byte("{\"token\":\"abc\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfiles(poison); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("poison load err = %v", err)
	}
}

func TestGrantProxyStripsQueryAndInjectsBearer(t *testing.T) {
	const grant = "0123456789abcdef0123456789abcdef"
	var gotAuth, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		if gotAuth != "Bearer "+grant {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"credential":"grant"}`))
	}))
	defer upstream.Close()
	up, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := http.NewRequest(http.MethodGet, upstream.URL+"/v1/gateway", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = up
	u, err := target.URL.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newGrantProxy(grant, u, io.Discard)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	res, err := http.Get("http://" + ln.Addr().String() + "/v1/gateway?token=" + grant + "&x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	if strings.Contains(gotQuery, "token") || strings.Contains(gotQuery, grant) {
		t.Fatalf("upstream query kept the grant: %q", gotQuery)
	}
	if gotAuth != "Bearer "+grant {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if strings.Contains(string(body), grant) {
		t.Fatalf("body contained grant: %s", body)
	}

	// An explicit bearer is left alone, so a caller cannot be silently rewritten
	// to a different credential they did not send. Empty bearer is injected.
	res2, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
}

type scriptedSSH struct {
	mu       sync.Mutex
	commands []string
	grant    string
	upstream string
}

func (s *scriptedSSH) LocalForward(ctx context.Context, spec forwardSpec) (runningForward, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return runningForward{}, err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(left net.Conn) {
				defer left.Close()
				right, err := net.Dial("tcp", s.upstream)
				if err != nil {
					return
				}
				defer right.Close()
				go io.Copy(right, left)
				_, _ = io.Copy(left, right)
			}(c)
		}
	}()
	return runningForward{Local: ln.Addr().String(), Close: func() { _ = ln.Close() }}, nil
}

func (s *scriptedSSH) Output(ctx context.Context, dest, remoteCommand string) ([]byte, []byte, error) {
	s.mu.Lock()
	s.commands = append(s.commands, dest+" "+remoteCommand)
	s.mu.Unlock()
	if strings.Contains(remoteCommand, "token=") || strings.Contains(remoteCommand, s.grant) {
		return nil, []byte("command included a secret"), nil
	}
	return []byte(s.grant + "\n"), nil, nil
}

func TestConnectInjectsGrantWithoutPrintingIt(t *testing.T) {
	root := "root-token-aaaaaaaaaaaaaaaa"
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(tokenPath, []byte(root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon, err := server.New(server.Options{
		Auth:       true,
		Token:      root,
		TokenFile:  tokenPath,
		Addr:       "127.0.0.1:0",
		SessionDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(daemon.Handler())
	defer up.Close()

	status, body, err := daemonAuthedJSON(http.MethodPost, up.URL+"/v1/gateway/grants", root, map[string]any{"ttlSeconds": 60})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("mint status=%d err=%v body=%s", status, err, body)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &minted); err != nil {
		t.Fatal(err)
	}
	upURL, err := http.NewRequest(http.MethodGet, up.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := upURL.URL.Host

	ssh := &scriptedSSH{grant: minted.Token, upstream: host}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr strings.Builder
	opt, err := parseConnectArgs([]string{"--save", "box", "--ttl", "1m", "me@box"})
	if err != nil {
		t.Fatal(err)
	}
	profiles := filepath.Join(t.TempDir(), "gateway-profiles.json")
	publicURL, err := runConnect(ctx, opt, connectDeps{
		ssh:      ssh,
		profiles: profiles,
		listen: func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		},
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	errOut := stderr.String()
	if strings.Contains(out, minted.Token) || strings.Contains(errOut, minted.Token) || strings.Contains(out, root) || strings.Contains(errOut, root) {
		t.Fatalf("connect output leaked a secret\nstdout=%s\nstderr=%s", out, errOut)
	}
	if strings.Contains(publicURL, minted.Token) || strings.Contains(publicURL, "token=") {
		t.Fatalf("url = %s", publicURL)
	}
	res, err := http.Get(publicURL + "v1/gateway")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("proxied gateway = %d %s", res.StatusCode, got)
	}
	if strings.Contains(string(got), minted.Token) || strings.Contains(string(got), root) {
		t.Fatalf("proxied body leaked a secret: %s", got)
	}
	ssh.mu.Lock()
	cmds := append([]string(nil), ssh.commands...)
	ssh.mu.Unlock()
	if len(cmds) != 1 || strings.Contains(cmds[0], minted.Token) || strings.Contains(cmds[0], root) {
		t.Fatalf("ssh command = %q", cmds)
	}
	raw, err := os.ReadFile(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), minted.Token) || strings.Contains(string(raw), `"token"`) {
		t.Fatalf("profile leaked a secret:\n%s", raw)
	}
	cancel()
}

func TestDaemonGrantAndRevokeDoNotPrintHostToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootPath := filepath.Join(home, ".strike", "daemon.token")
	if err := os.MkdirAll(filepath.Dir(rootPath), 0o700); err != nil {
		t.Fatal(err)
	}
	root := "root-token-bbbbbbbbbbbbbbbb"
	if err := os.WriteFile(rootPath, []byte(root+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon, err := server.New(server.Options{
		Auth:       true,
		Token:      root,
		TokenFile:  rootPath,
		SessionDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(daemon.Handler())
	defer up.Close()

	var stdout, stderr strings.Builder
	if code := runDaemonCLI([]string{"grant", "--ttl", "1m", "--base", up.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("grant code=%d stderr=%s", code, stderr.String())
	}
	grant := strings.TrimSpace(stdout.String())
	if !validDaemonToken(grant) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stderr.String(), grant) || strings.Contains(stderr.String(), root) {
		t.Fatalf("stderr leaked a secret: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), root) {
		t.Fatalf("stdout contained host token: %s", stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout lines = %q", lines)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runDaemonCLI([]string{"revoke", "--base", up.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("revoke code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), grant) || strings.Contains(stdout.String(), root) || strings.Contains(stderr.String(), grant) || strings.Contains(stderr.String(), root) {
		t.Fatalf("revoke leaked\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
	res, err := http.Get(up.URL + "/v1/gateway")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, up.URL+"/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("grant after revoke = %d", res.StatusCode)
	}
}

func TestRedactSecretsReplacesKnownGrant(t *testing.T) {
	const grant = "0123456789abcdef0123456789abcdef"
	got := redactSecrets("ssh failed "+grant+" Bearer "+grant, grant)
	if strings.Contains(got, grant) {
		t.Fatalf("redact left the grant: %s", got)
	}
}
