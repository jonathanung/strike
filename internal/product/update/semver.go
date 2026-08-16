package update

import (
	"fmt"
	"strconv"
	"strings"
)

// normalizeTag strips a leading "v" and any "+metadata" / pre-release suffix
// beyond the first '-' for numeric comparison of major.minor.patch.
func normalizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.TrimPrefix(tag, "v")
	tag = strings.TrimPrefix(tag, "V")
	if i := strings.IndexByte(tag, '+'); i >= 0 {
		tag = tag[:i]
	}
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// parseSemver parses "major.minor.patch" (missing parts default to 0).
func parseSemver(tag string) (major, minor, patch int, ok bool) {
	tag = normalizeTag(tag)
	if tag == "" || tag == "dev" || tag == "none" || tag == "unknown" {
		return 0, 0, 0, false
	}
	parts := strings.Split(tag, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// IsNewer reports whether latest is a higher semver than current.
// Non-semver current (dev/empty) treats any valid latest as newer.
// Non-semver latest is never newer.
func IsNewer(latest, current string) bool {
	lm, ln, lp, lok := parseSemver(latest)
	if !lok {
		return false
	}
	cm, cn, cp, cok := parseSemver(current)
	if !cok {
		return true
	}
	if lm != cm {
		return lm > cm
	}
	if ln != cn {
		return ln > cn
	}
	return lp > cp
}

// AssetName builds the release artifact basename for os/arch.
// Example: strike_v0.1.0_linux_amd64.tar.gz
func AssetName(version, goos, goarch string) string {
	v := strings.TrimSpace(version)
	if v != "" && !strings.HasPrefix(v, "v") && !strings.HasPrefix(v, "V") {
		v = "v" + v
	}
	return fmt.Sprintf("strike_%s_%s_%s.tar.gz", v, goos, goarch)
}
