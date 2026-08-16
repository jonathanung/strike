package plugin

import (
	"fmt"
	"strconv"
	"strings"
)

// compareSemver returns -1 if a < b, 0 if equal, 1 if a > b (core + prerelease).
// Build metadata is ignored. Inputs must already match bundleVersionRE.
func compareSemver(a, b string) (int, error) {
	ac, err := parseSemver(a)
	if err != nil {
		return 0, fmt.Errorf("semver %q: %w", a, err)
	}
	bc, err := parseSemver(b)
	if err != nil {
		return 0, fmt.Errorf("semver %q: %w", b, err)
	}
	if c := cmpInt(ac.major, bc.major); c != 0 {
		return c, nil
	}
	if c := cmpInt(ac.minor, bc.minor); c != 0 {
		return c, nil
	}
	if c := cmpInt(ac.patch, bc.patch); c != 0 {
		return c, nil
	}
	// No prerelease > any prerelease.
	if ac.pre == "" && bc.pre == "" {
		return 0, nil
	}
	if ac.pre == "" {
		return 1, nil
	}
	if bc.pre == "" {
		return -1, nil
	}
	return comparePre(ac.pre, bc.pre), nil
}

type semverParts struct {
	major, minor, patch int
	pre                 string
}

func parseSemver(s string) (semverParts, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	// Drop build metadata.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semverParts{}, fmt.Errorf("want major.minor.patch")
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return semverParts{}, err
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return semverParts{}, err
	}
	pat, err := strconv.Atoi(parts[2])
	if err != nil {
		return semverParts{}, err
	}
	return semverParts{major: maj, minor: min, patch: pat, pre: pre}, nil
}

func comparePre(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, aNum := atoiOK(as[i])
		bi, bNum := atoiOK(bs[i])
		switch {
		case aNum && bNum:
			if c := cmpInt(ai, bi); c != 0 {
				return c
			}
		case aNum && !bNum:
			return -1
		case !aNum && bNum:
			return 1
		default:
			if as[i] < bs[i] {
				return -1
			}
			if as[i] > bs[i] {
				return 1
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

func atoiOK(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	// Leading zeros → not numeric ident per semver (treat as string); simplify: allow.
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// strikeCompatible reports whether runningVersion satisfies strike.min (inclusive)
// and optional strike.max (exclusive). "dev", "none", and empty running versions
// are treated as compatible (local builds).
func strikeCompatible(running, min, max string) (bool, string) {
	running = strings.TrimSpace(strings.TrimPrefix(running, "v"))
	if running == "" || running == "dev" || running == "none" {
		return true, ""
	}
	// Allow running versions that are not strict semver (git describe) only when
	// they parse; otherwise skip the check with a soft pass for local builds.
	if !bundleVersionRE.MatchString(running) {
		// Try core MAJOR.MINOR.PATCH prefix.
		if i := strings.IndexAny(running, "-+"); i >= 0 {
			running = running[:i]
		}
		parts := strings.Split(running, ".")
		if len(parts) >= 3 {
			running = parts[0] + "." + parts[1] + "." + parts[2]
		}
		if !bundleVersionRE.MatchString(running) {
			return true, ""
		}
	}
	if c, err := compareSemver(running, min); err != nil {
		return false, err.Error()
	} else if c < 0 {
		return false, fmt.Sprintf("requires Strike >= %s (running %s)", min, running)
	}
	if max = strings.TrimSpace(max); max != "" {
		if c, err := compareSemver(running, max); err != nil {
			return false, err.Error()
		} else if c >= 0 {
			return false, fmt.Sprintf("requires Strike < %s (running %s)", max, running)
		}
	}
	return true, ""
}
