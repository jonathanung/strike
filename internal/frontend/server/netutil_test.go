package server

import (
	"net"
	"strings"
	"testing"
)

func TestResolveBindAddr(t *testing.T) {
	cases := []struct {
		addr string
		want string
		err  bool
	}{
		{"127.0.0.1:8787", "127.0.0.1:8787", false},
		{"localhost:9", "localhost:9", false},
		{"[::1]:8787", "[::1]:8787", false},
		{"0.0.0.0:8787", "", true},
		{"192.168.1.5:8787", "", true},
		{":8787", "", true},
		{"", "127.0.0.1:8787", false},
		{"not-an-addr", "", true},
	}
	for _, tc := range cases {
		got, err := ResolveBindAddr(tc.addr)
		if tc.err {
			if err == nil {
				t.Errorf("ResolveBindAddr(%q) = %q, want error", tc.addr, got)
			} else if !strings.Contains(err.Error(), "loopback") && !strings.Contains(err.Error(), "invalid") {
				t.Errorf("ResolveBindAddr(%q) err = %v, want loopback/invalid hint", tc.addr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveBindAddr(%q): %v", tc.addr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveBindAddr(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestParseCIDRsAndIPAllowed(t *testing.T) {
	nets, err := ParseCIDRs([]string{"192.168.0.0/16", "10.0.0.1", "2001:db8::/32"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != 3 {
		t.Fatalf("len = %d", len(nets))
	}
	if !IPAllowed(net.ParseIP("192.168.1.20"), nets) {
		t.Fatal("want 192.168.1.20 allowed")
	}
	if !IPAllowed(net.ParseIP("10.0.0.1"), nets) {
		t.Fatal("want 10.0.0.1 allowed")
	}
	if IPAllowed(net.ParseIP("10.0.0.2"), nets) {
		t.Fatal("10.0.0.2 should be denied")
	}
	if IPAllowed(nil, nets) {
		t.Fatal("nil ip denied")
	}
	if !IPAllowed(net.ParseIP("8.8.8.8"), nil) {
		t.Fatal("empty allowlist allows all")
	}
	_, err = ParseCIDRs([]string{"not-a-cidr"})
	if err == nil {
		t.Fatal("want parse error")
	}
	nets, err = ParseCIDRs([]string{"10.0.0.0/8, 172.16.0.0/12"})
	if err != nil || len(nets) != 2 {
		t.Fatalf("comma list: nets=%v err=%v", nets, err)
	}
}

func TestClientIP(t *testing.T) {
	if got := ClientIP("192.168.1.9:54321"); got == nil || got.String() != "192.168.1.9" {
		t.Fatalf("got %v", got)
	}
	if got := ClientIP("[::1]:80"); got == nil || !got.IsLoopback() {
		t.Fatalf("got %v", got)
	}
}

func TestIsPrivateOrLoopbackIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,
		"::1":         true,
		"192.168.0.1": true,
		"10.1.2.3":    true,
		"8.8.8.8":     false,
		"":            false,
	}
	for s, want := range cases {
		var ip net.IP
		if s != "" {
			ip = net.ParseIP(s)
		}
		if got := IsPrivateOrLoopbackIP(ip); got != want {
			t.Errorf("IsPrivateOrLoopbackIP(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLANIPsNoPanic(t *testing.T) {
	_ = LANIPs()
}
