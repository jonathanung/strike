package admission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NormalizeAllowPaths validates and expands allow-list entries.
//
// Rules (fail closed on spoofable markers):
//   - Empty entries are skipped.
//   - Bare relative markers (no leading / or ~) are rejected — e.g. ".strike/skills"
//     would match evil/.strike/skills via substring/path tricks.
//   - "~/..." expands against home.
//   - Absolute paths must clean to a path equal to home or nested under home+sep.
//   - Results are cleaned absolute paths without a trailing separator (except root).
func NormalizeAllowPaths(in []string, home string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	home = strings.TrimSpace(home)
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	home = filepath.Clean(home)
	var out []string
	seen := map[string]struct{}{}
	for i, raw := range in {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// Reject bare relative / spoofable markers.
		if !strings.HasPrefix(raw, "~") && !filepath.IsAbs(raw) {
			return nil, fmt.Errorf("admission.allowPaths[%d]: %q must be home-anchored (~/… or absolute under $HOME); bare relative markers are spoofable", i, raw)
		}
		expanded, err := expandHomeAnchored(raw, home)
		if err != nil {
			return nil, fmt.Errorf("admission.allowPaths[%d]: %w", i, err)
		}
		if _, ok := seen[expanded]; ok {
			continue
		}
		seen[expanded] = struct{}{}
		out = append(out, expanded)
	}
	return out, nil
}

func expandHomeAnchored(raw, home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("%q: home directory unavailable for anchoring", raw)
	}
	var path string
	switch {
	case raw == "~":
		path = home
	case strings.HasPrefix(raw, "~/"):
		path = filepath.Join(home, raw[2:])
	case filepath.IsAbs(raw):
		path = raw
	default:
		return "", fmt.Errorf("%q is not home-anchored", raw)
	}
	path = filepath.Clean(path)
	if !underHome(path, home) {
		return "", fmt.Errorf("%q resolves outside home %q", raw, home)
	}
	return path, nil
}

func underHome(path, home string) bool {
	path = filepath.Clean(path)
	home = filepath.Clean(home)
	if path == home {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, home+sep)
}

// PathAllowed reports whether absPath is under any allow-list prefix.
// absPath should be cleaned absolute. Empty allow list → false.
func PathAllowed(allow []string, absPath string) bool {
	if len(allow) == 0 || strings.TrimSpace(absPath) == "" {
		return false
	}
	absPath = filepath.Clean(absPath)
	for _, prefix := range allow {
		prefix = filepath.Clean(prefix)
		if absPath == prefix {
			return true
		}
		sep := string(filepath.Separator)
		if strings.HasPrefix(absPath, prefix+sep) {
			return true
		}
	}
	return false
}

// FirstPartySkillRoots returns the real first-party skill directories for home
// and optional project workDir (strike-native only — not .claude/.opencode).
func FirstPartySkillRoots(home, workDir string) []string {
	var roots []string
	if home != "" {
		roots = append(roots, filepath.Join(filepath.Clean(home), ".strike", "skills"))
	}
	if workDir != "" {
		// Project layout: <workDir>/.strike/skills
		roots = append(roots, filepath.Join(filepath.Clean(workDir), ".strike", "skills"))
	}
	return roots
}

// FirstPartyPluginRoots returns real plugin install directories.
func FirstPartyPluginRoots(home, workDir string) []string {
	var roots []string
	if home != "" {
		roots = append(roots, filepath.Join(filepath.Clean(home), ".strike", "plugins"))
	}
	if workDir != "" {
		roots = append(roots, filepath.Join(filepath.Clean(workDir), ".strike", "plugins"))
	}
	return roots
}

// FirstPartyAgentRoots returns real first-party agent directories.
func FirstPartyAgentRoots(home, workDir string) []string {
	var roots []string
	if home != "" {
		roots = append(roots, filepath.Join(filepath.Clean(home), ".strike", "agents"))
	}
	if workDir != "" {
		roots = append(roots, filepath.Join(filepath.Clean(workDir), ".strike", "agents"))
	}
	return roots
}

// PathSpoofsFirstParty reports whether path looks like a first-party location
// via a nested marker (e.g. .../evil/.strike/skills/...) without actually
// residing under a real first-party root or an explicit allow-list entry.
//
// realRoots must include every legitimate install root (global + project).
// A path under those roots is never a spoof even though it contains the marker.
func PathSpoofsFirstParty(path string, realRoots, allow []string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	// Already under a real root or allow-list → not a spoof.
	if pathUnderAny(clean, realRoots) || PathAllowed(allow, clean) {
		return false
	}
	// Detect nested first-party markers that are not the real root prefix.
	markers := []string{
		filepath.Join(".strike", "skills"),
		filepath.Join(".strike", "agents"),
		filepath.Join(".strike", "plugins"),
	}
	// Normalize to slash for marker search without requiring the path to exist.
	slash := filepath.ToSlash(clean)
	for _, m := range markers {
		ms := "/" + filepath.ToSlash(m)
		idx := strings.Index(slash, ms)
		if idx < 0 {
			// also allow marker at start (unlikely for abs paths)
			if strings.HasPrefix(slash, filepath.ToSlash(m)) {
				return true
			}
			continue
		}
		// Marker appears as a path segment sequence not at a real root.
		// e.g. /tmp/evil/.strike/skills/foo
		return true
	}
	return false
}

func pathUnderAny(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, r := range roots {
		r = filepath.Clean(r)
		if path == r {
			return true
		}
		sep := string(filepath.Separator)
		if strings.HasPrefix(path, r+sep) {
			return true
		}
	}
	return false
}
