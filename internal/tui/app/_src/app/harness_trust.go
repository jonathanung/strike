package tui

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// lastDenialInfo remembers the most recent hard deny / user reject so the
// notice and /permission explain path stay one hop away (#809).
type lastDenialInfo struct {
	Permission string
	Pattern    string
	Layer      string
	RuleAction string
	// Source is "deny", "reject", or "tool" for copy tone.
	Source string
}

// permissionExplainHint is the short discoverability trailer for denial UI.
func permissionExplainHint(permission, pattern string) string {
	permission = strings.TrimSpace(permission)
	if permission == "" {
		return "/permission explain <tool> [pattern]"
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return "/permission explain " + permission
	}
	// Quote when the pattern has spaces so paste-back works in the composer.
	if strings.ContainsAny(pattern, " \t") {
		return fmt.Sprintf("/permission explain %s %q", permission, pattern)
	}
	return "/permission explain " + permission + " " + pattern
}

// firstDenialPattern picks a stable sample pattern from a deny/ask event.
func firstDenialPattern(patterns []string) string {
	for _, p := range patterns {
		if s := strings.TrimSpace(p); s != "" {
			return s
		}
	}
	return "*"
}

// formatDenialNotice builds the one-line (or short multi-line) denial notice
// with an explain entry point when rule metadata is available.
func formatDenialNotice(info lastDenialInfo, reason string) string {
	perm := strings.TrimSpace(info.Permission)
	if perm == "" {
		perm = "tool"
	}
	var b strings.Builder
	switch info.Source {
	case "reject":
		b.WriteString("permission rejected: ")
	default:
		b.WriteString("permission denied: ")
	}
	b.WriteString(perm)
	if pat := strings.TrimSpace(info.Pattern); pat != "" && pat != "*" {
		b.WriteString(" ")
		b.WriteString(pat)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString(": ")
		b.WriteString(reason)
	} else if layer := strings.TrimSpace(info.Layer); layer != "" {
		b.WriteString(": ")
		b.WriteString(layer)
		if ra := strings.TrimSpace(info.RuleAction); ra != "" {
			b.WriteString(" ")
			b.WriteString(ra)
		}
	}
	b.WriteString("\n")
	b.WriteString(permissionExplainHint(info.Permission, info.Pattern))
	return b.String()
}

// verificationBadgeLabel returns the short claim-vs-verified chip text and tone.
// ok is false when no verification should be shown (nil / empty report).
func verificationBadgeLabel(rep *protocol.VerificationReport) (label string, tone ui.Tone, ok bool) {
	if rep == nil {
		return "", ui.ToneMuted, false
	}
	// Prefer harness-owned verified/passed over model claim.
	switch {
	case rep.Verified && rep.Passed:
		return "verified", ui.ToneSuccess, true
	case rep.Claimed && !rep.Verified:
		// Implementer said done; gates did not confirm (or none passed).
		if len(rep.Checks) > 0 && !rep.Passed {
			return "claimed", ui.ToneWarning, true
		}
		if !rep.Passed {
			return "claimed", ui.ToneWarning, true
		}
		// Claimed with empty gates accepted as verified by harness — still
		// surface verified when Passed.
		if rep.Passed {
			return "verified", ui.ToneSuccess, true
		}
		return "claimed", ui.ToneWarning, true
	case !rep.Passed:
		return "unverified", ui.ToneError, true
	case rep.Passed:
		return "verified", ui.ToneSuccess, true
	default:
		return "", ui.ToneMuted, false
	}
}

// formatVerificationNotice is the sticky notice after gates finish.
func formatVerificationNotice(rep protocol.VerificationReport) (text string, isErr bool) {
	label, _, ok := verificationBadgeLabel(&rep)
	if !ok {
		label = "verification"
	}
	summary := strings.TrimSpace(rep.Summary)
	if summary == "" {
		switch {
		case rep.Verified && rep.Passed:
			summary = "gates passed"
		case rep.Claimed && !rep.Verified:
			summary = "claimed done, not harness-verified"
		case !rep.Passed:
			summary = "gates failed"
		default:
			summary = "complete"
		}
	}
	// Lead with the claim/verified vocabulary so the two states stay distinct.
	text = label + ": " + summary
	if rep.Claimed && rep.Verified && rep.Passed {
		text = "verified: " + summary
	} else if rep.Claimed && !rep.Verified {
		text = "claimed (not verified): " + summary
	}
	return text, !rep.Passed
}

// formatVerificationCell is a durable transcript row for claim vs verified.
func formatVerificationCell(rep protocol.VerificationReport) string {
	text, _ := formatVerificationNotice(rep)
	if n := len(rep.Checks); n > 0 {
		passed := 0
		for _, c := range rep.Checks {
			if c.Passed {
				passed++
			}
		}
		text = fmt.Sprintf("%s (%d/%d checks)", text, passed, n)
	}
	return text
}

// verificationHeaderChip builds an optional status-bar badge for the last
// harness verification on this session (cleared on the next turn start).
func verificationHeaderChip(th theme.Theme, rep *protocol.VerificationReport) (headerChip, bool) {
	label, tone, ok := verificationBadgeLabel(rep)
	if !ok {
		return headerChip{}, false
	}
	th = th.Resolve()
	return headerChip{70, ui.Badge(th, tone, label)}, true
}
