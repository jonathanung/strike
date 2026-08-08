package server

import (
	"fmt"
	"net"
	"strings"
)

// ResolveBindAddr validates a loopback-only host:port for strike serve.
// Non-loopback binds are rejected; remote access is via SSH local forward
// (ssh -L), not cleartext LAN expose.
func ResolveBindAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if !IsLocalhostBind(addr) {
		return "", fmt.Errorf("non-localhost --addr %s rejected: strike serve binds loopback only; use ssh -L 8787:127.0.0.1:8787 (see docs/web.md)", addr)
	}
	return net.JoinHostPort(host, port), nil
}

// ParseCIDRs parses comma-separated or repeated CIDR/IP allowlist entries.
// Bare IPs become /32 or /128.
func ParseCIDRs(specs []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, spec := range specs {
		for _, part := range strings.Split(spec, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "/") {
				_, n, err := net.ParseCIDR(part)
				if err != nil {
					return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
				}
				out = append(out, n)
				continue
			}
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP or CIDR %q", part)
			}
			if v4 := ip.To4(); v4 != nil {
				out = append(out, &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)})
			} else {
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
			}
		}
	}
	return out, nil
}

// IPAllowed reports whether ip is inside any of nets. Empty nets allow all.
func IPAllowed(ip net.IP, nets []*net.IPNet) bool {
	if len(nets) == 0 {
		return true
	}
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP extracts the remote IP from an HTTP RemoteAddr (host:port).
func ClientIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	return net.ParseIP(host)
}

// LANIPs returns non-loopback addresses on up interfaces. IPv4 first (phone-
// friendly); IPv6 only when no IPv4 was found (avoids privacy-address noise).
func LANIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4s, v6s []string
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				s := v4.String()
				if !seen[s] {
					seen[s] = true
					v4s = append(v4s, s)
				}
				continue
			}
			s := ip.String()
			if !seen[s] {
				seen[s] = true
				v6s = append(v6s, s)
			}
		}
	}
	if len(v4s) > 0 {
		return v4s
	}
	return v6s
}

// IsPrivateOrLoopbackIP reports whether ip is loopback or RFC1918/ULA/link-local.
func IsPrivateOrLoopbackIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// Unique local IPv6 (fc00::/7) — IsPrivate covers in Go 1.17+.
	return false
}
