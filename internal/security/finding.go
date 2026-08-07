// Package security holds shared severity and finding types for trust-boundary
// scanners (admission #889, write-time content guards #890, and future sinks).
//
// Action vocabularies differ by surface (admission: allow|warn|block|quarantine;
// content guards: allow|ask|deny) — only Finding and Severity are shared here.
package security

import "strings"

// Severity ranks a scanner finding. Stable strings for config matrices and
// protocol/timeline export.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// ParseSeverity normalizes s (case-insensitive). ok is false when unknown.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return SeverityInfo, true
	case "low":
		return SeverityLow, true
	case "medium", "med":
		return SeverityMedium, true
	case "high":
		return SeverityHigh, true
	case "critical", "crit":
		return SeverityCritical, true
	default:
		return "", false
	}
}

// Rank returns a comparable rank (higher = worse). Unknown severities rank 0.
func (s Severity) Rank() int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityLow:
		return 2
	case SeverityMedium:
		return 3
	case SeverityHigh:
		return 4
	case SeverityCritical:
		return 5
	default:
		return 0
	}
}

// Finding is one scanner hit. Rule is a stable machine id (e.g. mcp.network_tool).
// Surface is the load path (mcp|skill|plugin|content). Target identifies the
// subject (server name, skill name, path). Evidence is optional short context
// and must not hold raw secrets (callers redact before export).
type Finding struct {
	Rule     string   `json:"rule"`
	Surface  string   `json:"surface"`
	Target   string   `json:"target,omitempty"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
	Evidence string   `json:"evidence,omitempty"`
}

// MaxSeverity returns the highest-ranked severity among findings (empty → info).
func MaxSeverity(findings []Finding) Severity {
	max := SeverityInfo
	for _, f := range findings {
		if f.Severity.Rank() > max.Rank() {
			max = f.Severity
		}
	}
	return max
}
