package container

import "testing"

func TestParseMemoryBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"512", 512},
		{"1k", 1000},
		{"1m", 1000 * 1000},
		{"2g", 2 * 1000 * 1000 * 1000},
		{"1Mi", 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
	}
	for _, tc := range cases {
		got, err := ParseMemoryBytes(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %d err=%v want %d", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseMemoryBytes("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseNanoCPUs(t *testing.T) {
	got, err := ParseNanoCPUs("0.5")
	if err != nil || got != 5e8 {
		t.Fatalf("got %d %v", got, err)
	}
	got, err = ParseNanoCPUs("")
	if err != nil || got != 0 {
		t.Fatalf("empty: %d %v", got, err)
	}
}

func TestParseGPURequest(t *testing.T) {
	g, err := ParseGPURequest("all")
	if err != nil || g == nil || !g.All || g.CLIFlag() != "all" {
		t.Fatalf("%v %v", g, err)
	}
	g, err = ParseGPURequest("2")
	if err != nil || g.Count != 2 || g.CLIFlag() != "2" {
		t.Fatalf("%v %v", g, err)
	}
	g, err = ParseGPURequest("device=0,1")
	if err != nil || g.CLIFlag() != "device=0,1" {
		t.Fatalf("%v %v", g, err)
	}
	g, err = ParseGPURequest("none")
	if err != nil || g != nil {
		t.Fatalf("%v %v", g, err)
	}
}

func TestParsePortBindings(t *testing.T) {
	p, err := ParsePortBindings([]string{"8080:80", "443:443"})
	if err != nil || len(p) != 2 || p[0].Host != "8080" || p[0].Container != "80" {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := ParsePortBindings([]string{"bad"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := ParsePortBindings([]string{"80:80", "80:8080"}); err == nil {
		t.Fatal("expected conflict")
	}
	if _, err := ParsePortBindings([]string{"0:80"}); err == nil {
		t.Fatal("port 0 invalid")
	}
}
