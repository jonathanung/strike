package main

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// Probe is the guest's isolation report. Missing keys fail closed.
type Probe struct {
	Hostname              string
	NonLoIfaces           string
	DefaultRoute          string
	NetPing               string
	NetTCP                string
	NetMetadata           string
	NetDNS                string
	HostCanary            string
	HostAuth              string
	HostHome              string
	WorkspaceParentEscape string
	WorkspaceMarker       string
	StrikeAuth            string
	RunSecrets            string
	EnvNames              []string
	Entries               []string
	MemTotalKiB           int64
	MemAvailableKiB       int64
	Mounts                []string
	Complete              bool
	seen                  map[string]bool
}

func parseProbe(text string) (Probe, error) {
	p := Probe{seen: map[string]bool{}}
	var envOn, entriesOn bool
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "env_names_begin":
			envOn = true
		case line == "env_names_end":
			envOn = false
		case line == "workspace_entries_begin":
			entriesOn = true
		case line == "workspace_entries_end":
			entriesOn = false
		case envOn && strings.HasPrefix(line, "env_name "):
			p.EnvNames = append(p.EnvNames, strings.TrimPrefix(line, "env_name "))
		case entriesOn && strings.HasPrefix(line, "entry "):
			p.Entries = append(p.Entries, strings.TrimPrefix(line, "entry "))
		case strings.HasPrefix(line, "mount "):
			p.Mounts = append(p.Mounts, strings.TrimPrefix(line, "mount "))
		case strings.Contains(line, "="):
			k, v, _ := strings.Cut(line, "=")
			p.seen[k] = true
			switch k {
			case "hostname":
				p.Hostname = v
			case "non_lo_ifaces":
				p.NonLoIfaces = v
			case "default_route":
				p.DefaultRoute = v
			case "net_ping":
				p.NetPing = v
			case "net_tcp":
				p.NetTCP = v
			case "net_metadata":
				p.NetMetadata = v
			case "net_dns":
				p.NetDNS = v
			case "host_canary":
				p.HostCanary = v
			case "host_auth":
				p.HostAuth = v
			case "host_home":
				p.HostHome = v
			case "workspace_parent_escape":
				p.WorkspaceParentEscape = v
			case "workspace_marker":
				p.WorkspaceMarker = v
			case "strike_auth":
				p.StrikeAuth = v
			case "run_secrets":
				p.RunSecrets = v
			case "mem_total_kb":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					return Probe{}, fmt.Errorf("mem_total_kb: %w", err)
				}
				p.MemTotalKiB = n
			case "mem_available_kb":
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					return Probe{}, fmt.Errorf("mem_available_kb: %w", err)
				}
				p.MemAvailableKiB = n
			case "probe_end":
				p.Complete = v == "ok"
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Probe{}, err
	}
	if !p.Complete {
		return p, fmt.Errorf("probe incomplete")
	}
	return p, nil
}

// Failures returns isolation failures. An empty slice is a pass.
func (p Probe) Failures() []string {
	var bad []string
	want := []struct{ name, got, want string }{
		{"non_lo_ifaces", p.NonLoIfaces, ""},
		{"default_route", p.DefaultRoute, "absent"},
		{"net_ping", p.NetPing, "blocked"},
		{"net_tcp", p.NetTCP, "blocked"},
		{"net_metadata", p.NetMetadata, "blocked"},
		{"net_dns", p.NetDNS, "blocked"},
		{"host_canary", p.HostCanary, "absent"},
		{"host_auth", p.HostAuth, "absent"},
		{"host_home", p.HostHome, "absent"},
		{"workspace_parent_escape", p.WorkspaceParentEscape, "absent"},
		{"workspace_marker", p.WorkspaceMarker, "present"},
		{"strike_auth", p.StrikeAuth, "absent"},
		{"run_secrets", p.RunSecrets, "absent"},
	}
	for _, w := range want {
		if !p.seen[w.name] {
			bad = append(bad, w.name+" missing")
			continue
		}
		if w.got != w.want {
			bad = append(bad, fmt.Sprintf("%s=%q want %q", w.name, w.got, w.want))
		}
	}
	if !p.seen["hostname"] || p.Hostname == "" || p.Hostname == "localhost" {
		bad = append(bad, fmt.Sprintf("hostname=%q", p.Hostname))
	}
	if len(p.EnvNames) > 0 {
		bad = append(bad, "env names: "+strings.Join(p.EnvNames, ","))
	}
	for _, e := range p.Entries {
		switch e {
		case "host-canary", "auth.json", "secrets":
			bad = append(bad, "workspace entry "+e)
		}
	}
	mounted := false
	for _, m := range p.Mounts {
		if strings.HasPrefix(m, "/workspace ") || strings.HasPrefix(m, "/workspace\t") {
			mounted = true
		}
	}
	if !mounted {
		bad = append(bad, "workspace mount missing")
	}
	if p.MemTotalKiB <= 0 {
		bad = append(bad, "guest MemTotal unmeasured")
	}
	return bad
}
