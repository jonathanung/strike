package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/harness/permission"
)

// tokenFileDenyRules is the managed deny ceiling strike serve adds for its
// configured --token-file (docs/design/script-schedules.md, "Agent reach").
// The path is user-chosen, so neither Defaults() nor ClassifyPath covers it;
// without this an agent on a host-mode machine could read the cockpit token
// and drive /v1/schedules itself. Rules land in engine.Options.ManagedRules,
// which no session grant, yolo, or --dangerously-skip-permissions overrides.
//
// Each spelling of the path (absolute, symlink-resolved, ~/…) yields:
//   - read <path>: the read tool asks with the absolute path outside WorkDir
//   - bash <path>: matched through action-fact path keys (cat <path>, …)
//   - bash **<path>* and **<path>*/**: raw command text with the path
//     anywhere in it, when the command does not parse authoritatively
//
// When the file sits under cwd (serve's directory, the default workspace),
// the cwd-relative spelling is denied too: the read tool asks with a path
// relative to the session WorkDir. Only exact and single-command forms are
// added for it, since a relative glob would match same-named files elsewhere.
func tokenFileDenyRules(tokenFile, cwd string) permission.Ruleset {
	tokenFile = strings.TrimSpace(tokenFile)
	if tokenFile == "" {
		return nil
	}
	var spellings []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		spellings = append(spellings, p)
	}
	abs, err := filepath.Abs(tokenFile)
	if err != nil {
		return nil
	}
	add(filepath.Clean(abs))
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		add(filepath.Clean(real))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, p := range append([]string(nil), spellings...) {
			if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				add("~/" + filepath.ToSlash(rel))
			}
		}
	}
	var out permission.Ruleset
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		relSeen := map[string]bool{}
		for _, p := range spellings {
			if strings.HasPrefix(p, "~") {
				continue
			}
			rel, err := filepath.Rel(cwd, p)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || relSeen[rel] {
				continue
			}
			relSeen[rel] = true
			q := escapeGlob(filepath.ToSlash(rel))
			out = append(out,
				permission.Rule{Permission: "read", Pattern: q, Action: permission.Deny},
				permission.Rule{Permission: "bash", Pattern: q, Action: permission.Deny},
				permission.Rule{Permission: "bash", Pattern: "* " + q, Action: permission.Deny},
				permission.Rule{Permission: "bash", Pattern: "* ./" + q, Action: permission.Deny},
			)
		}
	}
	for _, p := range spellings {
		q := escapeGlob(p)
		out = append(out,
			permission.Rule{Permission: "read", Pattern: q, Action: permission.Deny},
			permission.Rule{Permission: "bash", Pattern: q, Action: permission.Deny},
		)
		// "**" + "/abs…" is a whole-segment globstar that crosses the "/"
		// in any command prefix; "~/…" needs the separator added.
		prefix := "**"
		if !strings.HasPrefix(q, "/") {
			prefix = "**/"
		}
		out = append(out,
			permission.Rule{Permission: "bash", Pattern: prefix + q + "*", Action: permission.Deny},
			permission.Rule{Permission: "bash", Pattern: prefix + q + "*/**", Action: permission.Deny},
		)
	}
	return out
}

// escapeGlob quotes doublestar metacharacters so a path matches literally.
func escapeGlob(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '*', '?', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sessionManagedRules is the managed deny ceiling one session gets: the
// managed config's denies, then serve's own (token file). Defensive copy.
func sessionManagedRules(configured, extra permission.Ruleset) permission.Ruleset {
	out := make(permission.Ruleset, 0, len(configured)+len(extra))
	out = append(out, configured...)
	out = append(out, extra...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// serveCwd is the daemon directory (default workspace path); empty on error.
func serveCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}
