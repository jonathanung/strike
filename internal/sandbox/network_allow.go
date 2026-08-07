package sandbox

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"
)

// Network allowlist entries are hostnames, single-label left wildcards
// (*.example.com), IP literals, or CIDRs. Empty allow means unrestricted
// (callers still apply SSRF / private-IP blocks). When non-empty, a host must
// match at least one entry.
//
// This is the shared policy shape for application-layer egress (webfetch,
// websearch, and bash client preflight; container net later). Bash OS
// networking remains all-or-nothing via Policy.NetworkEnabled — per-host
// filters are not applied inside bwrap/seatbelt (documented platform gap).

// NormalizeNetworkAllow trims, lowercases hostnames, validates, dedupes, and
// sorts allowlist entries. Empty strings are dropped. Bare "*" is rejected
// (omit the list or use an empty list for unrestricted).
func NormalizeNetworkAllow(entries []string) ([]string, error) {
	if entries == nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		norm, err := normalizeNetworkAllowEntry(raw)
		if err != nil {
			return nil, err
		}
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	sort.Strings(out)
	// Preserve non-nil empty slice when caller passed a non-nil list (JSON []).
	if len(out) == 0 {
		return []string{}, nil
	}
	return out, nil
}

func normalizeNetworkAllowEntry(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if s == "*" {
		return "", fmt.Errorf("network.allow entry %q is invalid (omit network.allow or use [] for unrestricted)", raw)
	}
	// CIDR
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return "", fmt.Errorf("network.allow entry %q: %w", raw, err)
		}
		// Canonicalize (e.g. 10.0.0.1/8 → 10.0.0.0/8).
		ones, _ := n.Mask.Size()
		return fmt.Sprintf("%s/%d", n.IP.String(), ones), nil
	}
	// IP literal
	if ip := net.ParseIP(s); ip != nil {
		return ip.String(), nil
	}
	// Hostname / wildcard
	host := strings.ToLower(s)
	host = strings.TrimSuffix(host, ".")
	if err := validateAllowHostname(host); err != nil {
		return "", fmt.Errorf("network.allow entry %q: %w", raw, err)
	}
	return host, nil
}

func validateAllowHostname(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if strings.ContainsAny(host, ":/\\ ") {
		return fmt.Errorf("invalid hostname")
	}
	// Single leading wildcard label only: *.example.com
	if strings.HasPrefix(host, "*.") {
		rest := host[2:]
		if rest == "" || strings.Contains(rest, "*") {
			return fmt.Errorf("wildcard must be a single leading label (*.example.com)")
		}
		return validateDNSLabels(rest)
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("wildcard must be a single leading label (*.example.com)")
	}
	return validateDNSLabels(host)
}

func validateDNSLabels(host string) error {
	if len(host) > 253 {
		return fmt.Errorf("hostname too long")
	}
	labels := strings.Split(host, ".")
	if len(labels) == 0 {
		return fmt.Errorf("empty host")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("invalid DNS label %q", label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid DNS label %q", label)
		}
		for _, r := range label {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
				continue
			}
			return fmt.Errorf("invalid DNS label %q", label)
		}
	}
	return nil
}

// HostAllowed reports whether host (name or IP literal) matches allow.
// Empty allow always returns true. host should be url.URL.Hostname() form
// (no port, IPv6 without brackets).
//
// Matching:
//   - exact hostname (case-insensitive) or *.suffix wildcard
//   - IP literal equal to an allow IP
//   - IP literal contained in an allow CIDR
//
// Hostnames are not resolved here — callers that need CIDR matches against
// resolved addresses should also call IPAllowed after lookup / at dial time.
func HostAllowed(host string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	host = normalizeLookupHost(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return IPAllowed(ip, allow)
	}
	return hostnameAllowed(host, allow)
}

// IPAllowed reports whether ip matches an IP or CIDR allow entry.
// Empty allow always returns true. Hostname wildcards do not match IPs.
func IPAllowed(ip net.IP, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	if ip == nil {
		return false
	}
	for _, entry := range allow {
		if strings.Contains(entry, "/") {
			_, n, err := net.ParseCIDR(entry)
			if err != nil || n == nil {
				continue
			}
			if n.Contains(ip) {
				return true
			}
			continue
		}
		if allowIP := net.ParseIP(entry); allowIP != nil && allowIP.Equal(ip) {
			return true
		}
	}
	return false
}

// CheckNetworkAllow returns nil when host is permitted by allow.
// Empty allow is unrestricted. Hostname patterns match without DNS; when the
// allowlist has IP/CIDR entries and host is not an IP literal, host is resolved
// and any non-matching-only result is accepted if an address hits a CIDR/IP
// entry. Dial-time CheckNetworkDialAllow still re-validates the concrete IP.
func CheckNetworkAllow(host string, allow []string) error {
	if len(allow) == 0 {
		return nil
	}
	host = normalizeLookupHost(host)
	if host == "" {
		return fmt.Errorf("host %q is not on the network allowlist", host)
	}
	if HostAllowed(host, allow) {
		return nil
	}
	// Hostname did not match name patterns; try DNS against IP/CIDR entries.
	if net.ParseIP(host) == nil && allowHasIPOrCIDR(allow) {
		ips, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("resolving host %q for network allowlist: %w", host, err)
		}
		for _, ip := range ips {
			if IPAllowed(ip, allow) {
				return nil
			}
		}
	}
	return fmt.Errorf("host %q is not on the network allowlist", host)
}

func allowHasIPOrCIDR(allow []string) bool {
	for _, entry := range allow {
		if strings.Contains(entry, "/") || net.ParseIP(entry) != nil {
			return true
		}
	}
	return false
}

// CheckNetworkDialAllow validates a concrete dial target. Hostname pattern
// matches on host still pass (SSRF is separate). Otherwise the dialed IP must
// match an IP/CIDR allow entry. Empty allow is unrestricted.
func CheckNetworkDialAllow(host, ipStr string, allow []string) error {
	if len(allow) == 0 {
		return nil
	}
	host = normalizeLookupHost(host)
	if host != "" && net.ParseIP(host) == nil && hostnameAllowed(host, allow) {
		return nil
	}
	ip := net.ParseIP(ipStr)
	if ip != nil && IPAllowed(ip, allow) {
		return nil
	}
	// IP-literal host already covered by IPAllowed above when host parses as IP.
	if host != "" && net.ParseIP(host) != nil && IPAllowed(net.ParseIP(host), allow) {
		return nil
	}
	display := host
	if display == "" {
		display = ipStr
	}
	return fmt.Errorf("host %q is not on the network allowlist", display)
}

func normalizeLookupHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	// url.Hostname already strips brackets; be defensive.
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.ToLower(host)
}

func hostnameAllowed(host string, allow []string) bool {
	for _, entry := range allow {
		if strings.Contains(entry, "/") {
			continue
		}
		if net.ParseIP(entry) != nil {
			continue
		}
		if strings.HasPrefix(entry, "*.") {
			// *.example.com matches foo.example.com and example.com
			suffix := entry[1:] // .example.com
			base := entry[2:]   // example.com
			if host == base || strings.HasSuffix(host, suffix) {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}

// CloneNetworkAllow returns a defensive copy (nil stays nil).
func CloneNetworkAllow(allow []string) []string {
	if allow == nil {
		return nil
	}
	out := make([]string, len(allow))
	copy(out, allow)
	return out
}
