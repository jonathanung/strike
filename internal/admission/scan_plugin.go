package admission

import (
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/security"
)

// PluginSubject is one plugin/extension presented for admission at register.
type PluginSubject struct {
	ID   string
	Root string // absolute plugin root
	// Capabilities are inferred tags (mcp.stdio, harnesses, …).
	Capabilities []string
	// Trusted is true when executable trust grant matches.
	Trusted bool
	// HasExecutable is true when MCP/harness/shell-hook contributions exist.
	HasExecutable bool
}

// ScanPlugin returns findings for one plugin bundle (path + capability surface).
func ScanPlugin(pol Policy, sub PluginSubject) []security.Finding {
	var out []security.Finding
	id := strings.TrimSpace(sub.ID)
	if id == "" {
		id = "unnamed"
	}
	surface := "plugin"

	if sub.Root != "" {
		abs := filepath.Clean(sub.Root)
		// Real plugin roots: ~/.strike/plugins and <cwd>/.strike/plugins
		var roots []string
		if pol.Home != "" {
			roots = append(roots, filepath.Join(filepath.Clean(pol.Home), ".strike", "plugins"))
		}
		if PathSpoofsFirstParty(abs, roots, pol.AllowPaths) {
			// Only flag when the path contains a nested .strike/plugins marker
			// outside the real install roots (same helper as skills).
			out = append(out, security.Finding{
				Rule:     "plugin.path_spoof",
				Surface:  surface,
				Target:   id,
				Message:  "plugin root nests a first-party marker outside real install roots",
				Severity: security.SeverityHigh,
				Evidence: clipEvidence(abs, 160),
			})
		}
	}

	if sub.HasExecutable && !sub.Trusted {
		// Informational under admission — trust system already blocks start.
		// Elevate under strict so operators see quarantine/block if trust skipped.
		out = append(out, security.Finding{
			Rule:     "plugin.untrusted_executable",
			Surface:  surface,
			Target:   id,
			Message:  "plugin has executable contributions without a matching trust grant",
			Severity: security.SeverityMedium,
		})
	}

	for _, cap := range sub.Capabilities {
		switch strings.TrimSpace(cap) {
		case "mcp.stdio", "mcp.http":
			out = append(out, security.Finding{
				Rule:     "plugin.mcp_capability",
				Surface:  surface,
				Target:   id,
				Message:  "plugin declares MCP executable capability",
				Severity: security.SeverityLow,
				Evidence: cap,
			})
		case "harnesses", "hooks.command", "panes.process":
			out = append(out, security.Finding{
				Rule:     "plugin.exec_capability",
				Surface:  surface,
				Target:   id,
				Message:  "plugin declares process/shell capability",
				Severity: security.SeverityLow,
				Evidence: cap,
			})
		}
	}
	return out
}

// AdmitPlugin scans and decides for one plugin.
func AdmitPlugin(pol Policy, sub PluginSubject) Verdict {
	findings := ScanPlugin(pol, sub)
	return pol.Decide("plugin", strings.TrimSpace(sub.ID), findings)
}
