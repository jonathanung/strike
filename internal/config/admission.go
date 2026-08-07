package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/admission"
	"github.com/jonathanung/strike-cli/internal/plugin"
)

// ResolveAdmission builds an admission.Policy from cfg.Admission.
// Empty config resolves to the default preset.
func ResolveAdmission(cfg Config) (admission.Policy, error) {
	home, _ := os.UserHomeDir()
	return admission.Resolve(admission.Config{
		Preset:     cfg.Admission.Preset,
		AllowPaths: append([]string(nil), cfg.Admission.AllowPaths...),
		FailClosed: cfg.Admission.FailClosed,
	}, home)
}

// FilterSkills runs admission on each skill and returns those that bind
// (allow|warn). Blocked/quarantined skills are omitted. Verdicts are returned
// for operator/timeline emission (including allow when findings empty only if
// non-allow — actually all non-allow and warn are included; pure allow with
// no findings are omitted to reduce noise).
func FilterSkills(pol admission.Policy, skills []Skill) (admitted []Skill, verdicts []admission.Verdict) {
	admitted = make([]Skill, 0, len(skills))
	for _, s := range skills {
		v := admission.AdmitSkill(pol, admission.SkillSubject{
			Name:     s.Name,
			Path:     s.Path,
			Template: s.Template,
			Builtin:  s.Builtin,
		})
		if v.Action != admission.ActionAllow || len(v.Findings) > 0 || v.ScanError != "" {
			verdicts = append(verdicts, v)
		}
		if !v.BindsTools() {
			fmt.Fprintf(os.Stderr, "%s\n", admission.FormatVerdict(v))
			continue
		}
		if v.Action == admission.ActionWarn {
			fmt.Fprintf(os.Stderr, "%s\n", admission.FormatVerdict(v))
		}
		admitted = append(admitted, s)
	}
	return admitted, verdicts
}

// AdmitPlugins runs admission on discovered plugins and returns verdicts.
// Path spoof and capability surfaces are scanned. Executable trust remains
// enforced by plugin.CompileExecutables; admission does not replace it.
// HasExecutable+Trusted are left false/true respectively so the untrusted
// finding is not double-counted here (trust diagnostics already cover that).
func AdmitPlugins(pol admission.Policy, res plugin.Result) []admission.Verdict {
	var out []admission.Verdict
	for _, p := range res.Plugins {
		caps := plugin.InferCapabilitiesAt(p.Manifest, p.Root)
		v := admission.AdmitPlugin(pol, admission.PluginSubject{
			ID:           p.ID,
			Root:         p.Root,
			Capabilities: caps,
			// Trust is a separate gate; do not emit untrusted_executable here.
			Trusted:       true,
			HasExecutable: false,
		})
		if v.Action != admission.ActionAllow || len(v.Findings) > 0 {
			out = append(out, v)
			if v.Action != admission.ActionAllow {
				fmt.Fprintf(os.Stderr, "%s\n", admission.FormatVerdict(v))
			}
		}
	}
	return out
}

// VerdictToEventFields extracts protocol-friendly fields from a verdict.
func VerdictToEventFields(v admission.Verdict, preset string) (surface, target, action, reason string, findings []string) {
	surface = v.Surface
	target = v.Target
	action = string(v.Action)
	reason = v.Reason
	for _, f := range v.Findings {
		if f.Rule != "" {
			findings = append(findings, f.Rule)
		}
	}
	if reason == "" && len(findings) > 0 {
		reason = strings.Join(findings, ",")
	}
	_ = preset
	return
}
