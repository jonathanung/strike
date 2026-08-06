package plugin

import (
	"fmt"
	"strings"
)

// Severity ranks a diagnostic.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Scope is where a plugin was discovered.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Diagnostic is one loader finding with optional plugin provenance.
type Diagnostic struct {
	Severity  Severity
	Code      string // e.g. malformed, collision, path, version, disabled, digest
	Message   string
	PluginID  string
	Version   string
	Source    Scope  // global | project
	Path      string // contribution or manifest relative/absolute path
	Collision string // name that collided, when relevant
}

// String formats a diagnostic for stderr / tests.
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(string(d.Severity))
	if d.Code != "" {
		b.WriteString("[")
		b.WriteString(d.Code)
		b.WriteString("]")
	}
	b.WriteString(": ")
	if d.PluginID != "" {
		fmt.Fprintf(&b, "plugin=%s", d.PluginID)
		if d.Version != "" {
			fmt.Fprintf(&b, "@%s", d.Version)
		}
		if d.Source != "" {
			fmt.Fprintf(&b, " source=%s", d.Source)
		}
		if d.Path != "" {
			fmt.Fprintf(&b, " path=%s", d.Path)
		}
		b.WriteString(": ")
	} else if d.Path != "" {
		fmt.Fprintf(&b, "%s: ", d.Path)
	}
	b.WriteString(d.Message)
	return b.String()
}

// FormatProvenance returns the contract diagnostic stamp.
func FormatProvenance(id, version string, source Scope, relPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "plugin=%s", id)
	if version != "" {
		fmt.Fprintf(&b, "@%s", version)
	}
	if source != "" {
		fmt.Fprintf(&b, " source=%s", source)
	}
	if relPath != "" {
		fmt.Fprintf(&b, " path=%s", relPath)
	}
	return b.String()
}
