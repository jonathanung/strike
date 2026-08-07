package container

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseMemoryBytes converts "512m"/"2g"/bytes into a byte count.
// Empty or "0" means unlimited (returns 0).
func ParseMemoryBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	// bare integer = bytes
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("memory %q: must be non-negative", s)
		}
		return n, nil
	}
	s = strings.ToLower(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "ki") || strings.HasSuffix(s, "kib"):
		mult = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "kib"), "ki")
	case strings.HasSuffix(s, "mi") || strings.HasSuffix(s, "mib"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "mib"), "mi")
	case strings.HasSuffix(s, "gi") || strings.HasSuffix(s, "gib"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "gib"), "gi")
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "kb"):
		mult = 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "kb"), "k")
	case strings.HasSuffix(s, "m") || strings.HasSuffix(s, "mb"):
		mult = 1000 * 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "mb"), "m")
	case strings.HasSuffix(s, "g") || strings.HasSuffix(s, "gb"):
		mult = 1000 * 1000 * 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "gb"), "g")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	default:
		return 0, fmt.Errorf("memory %q: unknown unit", s)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("memory %q: invalid number", s)
	}
	return int64(f * float64(mult)), nil
}

// ParseNanoCPUs converts "0.5"/"2" into Docker --cpus nanocpus (1 CPU = 1e9).
// Empty or "0" means unlimited (returns 0).
func ParseNanoCPUs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("cpus %q: %w", s, err)
	}
	if f < 0 {
		return 0, fmt.Errorf("cpus %q: must be non-negative", s)
	}
	return int64(f * 1e9), nil
}

// GPURequest is a simplified --gpus request for CLI flag generation.
type GPURequest struct {
	// All requests every GPU (--gpus all).
	All bool
	// Count requests the first N GPUs (--gpus N). Mutually exclusive with DeviceIDs.
	Count int
	// DeviceIDs lists specific GPU ids (--gpus device=0,1).
	DeviceIDs []string
}

// ParseGPURequest parses "", "none", "all", "2", "device=0,1", "0,1".
func ParseGPURequest(s string) (*GPURequest, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "none" {
		return nil, nil
	}
	if s == "all" {
		return &GPURequest{All: true}, nil
	}
	if strings.HasPrefix(s, "device=") {
		ids := splitTrim(strings.TrimPrefix(s, "device="))
		if len(ids) == 0 {
			return nil, fmt.Errorf("gpus %q: no device IDs", s)
		}
		return &GPURequest{DeviceIDs: ids}, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return nil, fmt.Errorf("gpus %q: count must be positive", s)
		}
		return &GPURequest{Count: n}, nil
	}
	ids := splitTrim(s)
	if len(ids) == 0 {
		return nil, fmt.Errorf("gpus %q: empty", s)
	}
	return &GPURequest{DeviceIDs: ids}, nil
}

// CLIFlag returns the value for docker --gpus (without the flag name).
func (g *GPURequest) CLIFlag() string {
	if g == nil {
		return ""
	}
	if g.All {
		return "all"
	}
	if len(g.DeviceIDs) > 0 {
		return "device=" + strings.Join(g.DeviceIDs, ",")
	}
	if g.Count > 0 {
		return strconv.Itoa(g.Count)
	}
	return ""
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PortBinding is a host:container TCP publish pair.
type PortBinding struct {
	Host      string
	Container string
}

// ParsePortBindings validates "host:container" entries.
func ParsePortBindings(ports []string) ([]PortBinding, error) {
	seenHost := map[string]bool{}
	var out []PortBinding
	for _, p := range ports {
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid port entry %q: expected hostPort:containerPort", p)
		}
		host, cont := parts[0], parts[1]
		if err := validatePort(host); err != nil {
			return nil, fmt.Errorf("invalid host port in %q: %w", p, err)
		}
		if err := validatePort(cont); err != nil {
			return nil, fmt.Errorf("invalid container port in %q: %w", p, err)
		}
		if seenHost[host] {
			return nil, fmt.Errorf("conflicting port binding: host port %s appears more than once", host)
		}
		seenHost[host] = true
		out = append(out, PortBinding{Host: host, Container: cont})
	}
	return out, nil
}

func validatePort(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("port %q is not a valid number", s)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("port %d is out of range (1-65535)", n)
	}
	return nil
}
