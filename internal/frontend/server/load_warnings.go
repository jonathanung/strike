package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/internal/product/config"
)

// loadWarning is one skill or agent file that failed to load, or loaded with
// a problem (#1386). It names the file and the loader's reason only.
type loadWarning struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Path        string `json:"path"`
	File        string `json:"file"`
	Message     string `json:"message"`
	Fatal       bool   `json:"fatal,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// loadWarnings rescans the daemon workspace's skill and agent roots with the
// real loaders. It reads disk on each call so a fixed file stops warning
// without a daemon restart.
func (s *Server) loadWarnings() []loadWarning {
	dir := strings.TrimSpace(s.opts.WorkDir)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		dir = wd
	}
	_, skillDiags, _ := config.LoadSkillsReport(dir)
	_, agentDiags, _ := config.LoadAgentsReport(dir)
	return toLoadWarnings(append(skillDiags, agentDiags...))
}

func toLoadWarnings(diags []config.LoadDiagnostic) []loadWarning {
	if len(diags) == 0 {
		return nil
	}
	out := make([]loadWarning, 0, len(diags))
	for _, d := range diags {
		out = append(out, loadWarning{
			Kind:        d.Kind,
			Severity:    d.Severity,
			Code:        d.Code,
			Path:        d.Path,
			File:        filepath.Base(d.Path),
			Message:     d.Message,
			Fatal:       d.Fatal,
			Fingerprint: d.Fingerprint,
		})
	}
	return out
}
