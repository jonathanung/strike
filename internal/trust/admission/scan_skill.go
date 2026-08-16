package admission

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jonathanung/strike-cli/internal/trust/security"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// SkillSubject is one skill presented for admission at load time.
type SkillSubject struct {
	Name     string
	Path     string // absolute path on disk; empty for builtins
	Template string
	// Builtin skips path spoof checks (shipping skills).
	Builtin bool
	// Source labels the discovery root (strike|claude|opencode|plugin|builtin).
	Source string
	// WorkDir is the project working directory (for first-party project roots).
	WorkDir string
}

var (
	reSkillExfil = regexp.MustCompile(`(?i)\b(ignore (all )?(previous|prior) (instructions|rules)|exfiltrat|send (all )?secrets?|dump (env|credentials)|curl .{0,40}\$.{0,20}(KEY|TOKEN|SECRET))\b`)
)

// ScanSkill returns findings for one skill (path + lightweight content checks).
func ScanSkill(pol Policy, sub SkillSubject) []security.Finding {
	var out []security.Finding
	name := strings.TrimSpace(sub.Name)
	if name == "" {
		name = "unnamed"
	}
	surface := "skill"

	if !sub.Builtin && sub.Path != "" {
		abs := sub.Path
		if !filepath.IsAbs(abs) {
			if a, err := filepath.Abs(abs); err == nil {
				abs = a
			}
		}
		abs = filepath.Clean(abs)
		// Include project workDir so legitimate <cwd>/.strike/skills is not spoofed.
		roots := FirstPartySkillRoots(pol.Home, sub.WorkDir)
		// Also treat allow-list as trusted first-party.
		if PathSpoofsFirstParty(abs, roots, pol.AllowPaths) {
			out = append(out, security.Finding{
				Rule:     "skill.path_spoof",
				Surface:  surface,
				Target:   name,
				Message:  "skill path nests a first-party marker outside real first-party roots",
				Severity: security.SeverityHigh,
				Evidence: clipEvidence(abs, 160),
			})
		}
	}

	body := sub.Template
	if body != "" {
		if redact.ContainsSecret(body) {
			out = append(out, security.Finding{
				Rule:     "skill.credential_content",
				Surface:  surface,
				Target:   name,
				Message:  "skill template contains credential-shaped material",
				Severity: security.SeverityCritical,
			})
		}
		if reSkillExfil.MatchString(body) {
			out = append(out, security.Finding{
				Rule:     "skill.suspicious_instruction",
				Surface:  surface,
				Target:   name,
				Message:  "skill template matches high-risk instruction patterns",
				Severity: security.SeverityHigh,
			})
		}
	}
	return out
}

// AdmitSkill scans and decides for one skill.
func AdmitSkill(pol Policy, sub SkillSubject) Verdict {
	if sub.Builtin {
		return Verdict{
			Surface: "skill",
			Target:  strings.TrimSpace(sub.Name),
			Action:  ActionAllow,
			Reason:  "builtin skill",
		}
	}
	findings := ScanSkill(pol, sub)
	return pol.Decide("skill", strings.TrimSpace(sub.Name), findings)
}
