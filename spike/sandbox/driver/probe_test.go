package main

import "testing"

const goodProbe = `probe_format=1
hostname=strike-phase0
non_lo_ifaces=
default_route=absent
net_ping=blocked
net_tcp=blocked
net_metadata=blocked
net_dns=blocked
host_canary=absent
host_auth=absent
host_home=absent
workspace_parent_escape=absent
workspace_marker=present
strike_auth=absent
run_secrets=absent
env_names_begin
env_names_end
workspace_entries_begin
entry marker.txt
entry probe.txt
workspace_entries_end
mem_total_kb=1008684
mem_available_kb=700000
mount /workspace virtiofs workspace
mount /nix/store squashfs /dev/vda
probe_end=ok
`

func TestParseProbePass(t *testing.T) {
	p, err := parseProbe(goodProbe)
	if err != nil {
		t.Fatal(err)
	}
	if p.MemTotalKiB != 1008684 || p.MemAvailableKiB != 700000 {
		t.Fatalf("memory %+v", p)
	}
	if len(p.Entries) != 2 || p.Entries[0] != "marker.txt" {
		t.Fatalf("entries %#v", p.Entries)
	}
	if f := p.Failures(); len(f) != 0 {
		t.Fatalf("failures: %v", f)
	}
}

func TestParseProbeFailClosed(t *testing.T) {
	cases := []struct {
		name string
		edit string
		want string
	}{
		{"reachable", "net_ping=blocked", "net_ping=reachable"},
		{"canary", "host_canary=absent", "host_canary=present"},
		{"nic", "non_lo_ifaces=", "non_lo_ifaces=eth0"},
		{"route", "default_route=absent", "default_route=present"},
		{"metadata", "net_metadata=blocked", "net_metadata=reachable"},
		{"auth", "host_auth=absent", "host_auth=present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := stringsReplace(goodProbe, tc.edit, tc.want, 1)
			p, err := parseProbe(text)
			if err != nil {
				t.Fatal(err)
			}
			f := p.Failures()
			if len(f) == 0 {
				t.Fatal("expected failures")
			}
		})
	}
	if _, err := parseProbe("hostname=strike-phase0\n"); err == nil {
		t.Fatal("incomplete probe accepted")
	}
	secret := goodProbe + "env_names_begin\nenv_name OPENAI_API_KEY\nenv_names_end\n"
	// The good probe already closed the env section. Append a second name
	// by replacing the empty section.
	secret = stringsReplace(goodProbe, "env_names_begin\nenv_names_end\n", "env_names_begin\nenv_name OPENAI_API_KEY\nenv_names_end\n", 1)
	p, err := parseProbe(secret)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range p.Failures() {
		if stringsContainsFold(f, "OPENAI_API_KEY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("env name not reported: %v", p.Failures())
	}
}

func stringsReplace(s, old, new string, n int) string {
	return replaceN(s, old, new, n)
}

func replaceN(s, old, new string, n int) string {
	out := s
	for i := 0; i < n; i++ {
		idx := indexOf(out, old)
		if idx < 0 {
			break
		}
		out = out[:idx] + new + out[idx+len(old):]
	}
	return out
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func stringsContainsFold(s, sub string) bool {
	return indexOf(s, sub) >= 0
}
