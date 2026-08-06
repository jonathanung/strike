package permission

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

// CompileSandbox builds an OS sandbox Policy from the dial mode, workspace
// root, and permission ruleset layers (last-match-wins, same as Evaluate).
//
// Mapping:
//   - write/edit deny "*" → NoWorkspaceWrite (no writable workspace bind)
//   - write/edit deny globs → DenyWriteGlobs + expanded DenyWritePaths
//   - Host networking on by default (Policy.NoNetwork zero value). Opt into
//     OS network isolation only when both webfetch and mcp are hard-Deny on
//     "*" (patterned rules do not flip full-network posture). Host/CIDR
//     allowlists remain #527.
//
// Mode upgrades (yolo / accept-edits) are not applied: the OS boundary is
// independent of ask-prompt posture. ModeOff returns a minimal policy.
func CompileSandbox(mode sandbox.Mode, workDir string, sets ...Ruleset) sandbox.Policy {
	p := sandbox.Policy{
		Mode:    mode,
		WorkDir: workDir,
	}
	if mode == sandbox.ModeOff {
		return p
	}

	// Zero-value Policy keeps host networking (gh, git push, package managers).
	// Opt into --unshare-net / seatbelt network deny only with an explicit dual
	// deny on the network-capable tool families.
	wf := Evaluate("webfetch", "*", sets...)
	mcp := Evaluate("mcp", "*", sets...)
	p.NoNetwork = wf == Deny && mcp == Deny

	if Evaluate("write", "*", sets...) == Deny || Evaluate("edit", "*", sets...) == Deny {
		p.NoWorkspaceWrite = true
	}

	patterns := collectWriteEditPatterns(sets)
	seenGlob := map[string]struct{}{}
	seenPath := map[string]struct{}{}
	var globs []string
	var paths []string
	for _, pat := range patterns {
		if pat == "*" || pat == "" {
			continue
		}
		if Evaluate("write", pat, sets...) != Deny && Evaluate("edit", pat, sets...) != Deny {
			continue
		}
		if _, ok := seenGlob[pat]; !ok {
			seenGlob[pat] = struct{}{}
			globs = append(globs, pat)
		}
		for _, abs := range expandWriteDenyPaths(workDir, pat) {
			if _, ok := seenPath[abs]; ok {
				continue
			}
			seenPath[abs] = struct{}{}
			paths = append(paths, abs)
		}
	}
	sort.Strings(globs)
	sort.Strings(paths)
	p.DenyWriteGlobs = globs
	p.DenyWritePaths = paths
	return p
}

// CompileSandbox returns the OS policy for the service's current layers
// (base → project → agent → granted → modeLate → phase).
func (s *Service) CompileSandbox(mode sandbox.Mode, workDir string) sandbox.Policy {
	if s == nil {
		return CompileSandbox(mode, workDir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := make([]Ruleset, 0, len(s.base)+5)
	sets = append(sets, s.base...)
	sets = append(sets, s.project, s.agent, s.granted, s.modeLate, s.phase)
	return CompileSandbox(mode, workDir, sets...)
}

func collectWriteEditPatterns(sets []Ruleset) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, set := range sets {
		for _, r := range set {
			switch r.Permission {
			case "write", "edit", "*":
			default:
				continue
			}
			pat := r.Pattern
			if pat == "" {
				pat = "*"
			}
			if _, ok := seen[pat]; ok {
				continue
			}
			seen[pat] = struct{}{}
			out = append(out, pat)
		}
	}
	return out
}

// expandWriteDenyPaths resolves a permission pattern under workDir to absolute
// paths suitable for bwrap --ro-bind. Missing targets are omitted.
func expandWriteDenyPaths(workDir, pattern string) []string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil
	}
	absWD, err := filepath.Abs(filepath.Clean(workDir))
	if err != nil {
		absWD = filepath.Clean(workDir)
	}
	if real, err := filepath.EvalSymlinks(absWD); err == nil && real != "" {
		absWD = real
	}

	pat := strings.TrimSpace(pattern)
	pat = strings.TrimPrefix(pat, "./")
	pat = strings.TrimPrefix(pat, "/")
	if pat == "" || pat == "*" {
		return nil
	}

	// Directory prefix forms: secrets/**, secrets/*, secrets/
	if base, ok := dirPrefix(pat); ok {
		p := filepath.Join(absWD, filepath.FromSlash(base))
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if real, err := filepath.EvalSymlinks(p); err == nil && real != "" {
				p = real
			}
			return []string{p}
		}
		return nil
	}

	// Exact relative path (no metacharacters).
	if !hasGlobMeta(pat) {
		p := filepath.Join(absWD, filepath.FromSlash(pat))
		if _, err := os.Stat(p); err != nil {
			return nil
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && real != "" {
			p = real
		}
		return []string{p}
	}

	// Glob expand under the workspace (best-effort; new files after compile
	// are not covered on Linux until the next CompileSandbox call).
	matches, err := doublestar.Glob(os.DirFS(absWD), filepath.ToSlash(pat))
	if err != nil || len(matches) == 0 {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, m := range matches {
		p := filepath.Join(absWD, filepath.FromSlash(m))
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && real != "" {
			p = real
		}
		if _, ok := seen[p]; ok {
			continue
		}
		// Skip the workspace root itself (would undo the writable bind).
		if p == absWD {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func dirPrefix(pat string) (string, bool) {
	pat = strings.TrimSuffix(pat, "/")
	for _, suf := range []string{"/**", "/*"} {
		if strings.HasSuffix(pat, suf) {
			base := strings.TrimSuffix(pat, suf)
			if base != "" && !hasGlobMeta(base) {
				return base, true
			}
		}
	}
	return "", false
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[{")
}
