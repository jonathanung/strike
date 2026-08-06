package sandbox

import (
	"net"
	"strings"
	"testing"
)

func TestNormalizeNetworkAllow(t *testing.T) {
	got, err := NormalizeNetworkAllow([]string{
		" API.GitHub.com ",
		"*.Example.COM.",
		"10.0.0.1/8",
		"8.8.8.8",
		"api.github.com", // dup
		"",
		"  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.example.com", "10.0.0.0/8", "8.8.8.8", "api.github.com"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}

	empty, err := NormalizeNetworkAllow([]string{})
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty list: %#v (want non-nil empty)", empty)
	}

	nilOut, err := NormalizeNetworkAllow(nil)
	if err != nil || nilOut != nil {
		t.Fatalf("nil in → nil out: %#v err=%v", nilOut, err)
	}
}

func TestNormalizeNetworkAllowRejects(t *testing.T) {
	cases := []string{
		"*",
		"foo.*.com",
		"*.",
		"not a host",
		"http://example.com",
		"example.com/path",
		"10.0.0.0/99",
		"-bad.com",
	}
	for _, c := range cases {
		if _, err := NormalizeNetworkAllow([]string{c}); err == nil {
			t.Errorf("%q: want error", c)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	allow := []string{"api.github.com", "*.npmjs.org", "8.8.8.8", "10.0.0.0/8"}

	if !HostAllowed("api.github.com", nil) {
		t.Fatal("empty allow must permit all")
	}
	if !HostAllowed("api.github.com", allow) {
		t.Fatal("exact host")
	}
	if !HostAllowed("API.GITHUB.COM", allow) {
		t.Fatal("case fold")
	}
	if HostAllowed("github.com", allow) {
		t.Fatal("parent of exact must not match")
	}
	if !HostAllowed("registry.npmjs.org", allow) {
		t.Fatal("wildcard subdomain")
	}
	if !HostAllowed("npmjs.org", allow) {
		t.Fatal("wildcard base")
	}
	if HostAllowed("evilnpmjs.org", allow) {
		t.Fatal("suffix injection")
	}
	if !HostAllowed("8.8.8.8", allow) {
		t.Fatal("IP literal")
	}
	if !HostAllowed("10.1.2.3", allow) {
		t.Fatal("CIDR")
	}
	if HostAllowed("11.0.0.1", allow) {
		t.Fatal("outside CIDR")
	}
	// Hostname is not resolved against CIDR in HostAllowed.
	if HostAllowed("internal.corp", allow) {
		t.Fatal("hostname must not match CIDR without resolution")
	}
}

func TestIPAllowedAndDial(t *testing.T) {
	allow := []string{"api.github.com", "10.0.0.0/8", "1.1.1.1"}

	if !IPAllowed(net.ParseIP("10.9.9.9"), allow) {
		t.Fatal("CIDR IP")
	}
	if !IPAllowed(net.ParseIP("1.1.1.1"), allow) {
		t.Fatal("exact IP")
	}
	if IPAllowed(net.ParseIP("8.8.8.8"), allow) {
		t.Fatal("unlisted IP")
	}

	// Hostname pattern: dial IP need not be on CIDR list.
	if err := CheckNetworkDialAllow("api.github.com", "140.82.112.3", allow); err != nil {
		t.Fatalf("hostname allow: %v", err)
	}
	// No hostname match: IP must be allowlisted.
	if err := CheckNetworkDialAllow("evil.example", "8.8.8.8", allow); err == nil {
		t.Fatal("want dial deny")
	}
	if err := CheckNetworkDialAllow("evil.example", "10.0.0.5", allow); err != nil {
		t.Fatalf("CIDR dial: %v", err)
	}
	if err := CheckNetworkDialAllow("x", "1.2.3.4", nil); err != nil {
		t.Fatalf("empty allow: %v", err)
	}
}

func TestCheckNetworkAllow(t *testing.T) {
	allow := []string{"example.com"}
	if err := CheckNetworkAllow("example.com", allow); err != nil {
		t.Fatal(err)
	}
	err := CheckNetworkAllow("other.com", allow)
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckNetworkAllowHostnameViaCIDR(t *testing.T) {
	// localhost resolves to loopback; allowlist CIDR must match resolved IP.
	allow := []string{"127.0.0.0/8", "::1/128"}
	if err := CheckNetworkAllow("localhost", allow); err != nil {
		t.Fatalf("localhost via CIDR: %v", err)
	}
	// Hostname-only allowlist must not require DNS for unrelated hosts.
	if err := CheckNetworkAllow("no-such-host.invalid", []string{"example.com"}); err == nil {
		t.Fatal("want deny without needing successful DNS")
	} else if strings.Contains(err.Error(), "resolving") {
		t.Fatalf("hostname-only allow should not resolve: %v", err)
	}
}

func TestCloneNetworkAllow(t *testing.T) {
	if CloneNetworkAllow(nil) != nil {
		t.Fatal("nil")
	}
	in := []string{"a.com"}
	out := CloneNetworkAllow(in)
	out[0] = "b.com"
	if in[0] != "a.com" {
		t.Fatal("mutate leaked")
	}
}
