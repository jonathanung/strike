package engine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Residue extraction caps (cost + marker size).
const (
	maxResidueItemsPerKind = 24
	maxResidueItemText     = 500
	maxResidueMarkerChars  = 12_000
	maxFileRefsPerItem     = 8
)

// Explicit line markers: DECISION: … / [decision] … / fact: …
// Captures kind + body. Kind group is normalized later.
var residueMarkerLine = regexp.MustCompile(`(?i)^\s*(?:\[(decision|fact|open(?:[_\s-]?question)?|assumption|constraint|todo|question)\]|(decision|fact|open(?:[_\s-]?question)?|assumption|constraint|todo|question)\s*:)\s*(.+?)\s*$`)

// path-ish tokens for file refs (relative or absolute, with common extensions).
var residuePathToken = regexp.MustCompile(`(?:^|[\s"'` + "`" + `(])((?:[\w./-]+/)+[\w./-]+\.[a-zA-Z0-9]{1,8})`)

// buildCompactionResidue extracts a schema-versioned residual from dropped
// history, optional active ledger slice, and session pin kinds.
// baseIndex is the pre-compaction index of dropped[0] (usually 0).
func buildCompactionResidue(
	dropped []provider.Message,
	baseIndex int,
	appliedStrategy, reason string,
	summary string,
	pinnedKinds []string,
	ledgerEntries []LedgerEntry,
) *protocol.CompactionResidue {
	r := &protocol.CompactionResidue{
		SchemaVersion: protocol.CompactionResidueSchemaVersion,
		Strategy:      appliedStrategy,
		Reason:        reason,
		Removed:       len(dropped),
		PinnedKinds:   append([]string(nil), pinnedKinds...),
		Summary:       strings.TrimSpace(summary),
	}
	if len(r.PinnedKinds) > 0 {
		sort.Strings(r.PinnedKinds)
	}

	// Ledger first so marked multi-agent decisions are never silently dropped.
	for _, e := range ledgerEntries {
		if e.Status != "" && e.Status != LedgerStatusActive {
			continue
		}
		item := residueFromLedger(e)
		appendResidueItem(r, item)
	}

	for i, m := range dropped {
		src := fmt.Sprintf("hist:%d", baseIndex+i)
		extractResidueFromMessage(r, m, src)
	}

	if residueEmpty(r) {
		return nil
	}
	return r
}

func residueEmpty(r *protocol.CompactionResidue) bool {
	if r == nil {
		return true
	}
	return len(r.Facts) == 0 && len(r.Decisions) == 0 && len(r.OpenQuestions) == 0 &&
		strings.TrimSpace(r.Summary) == "" && len(r.PinnedKinds) == 0
}

func residueFromLedger(e LedgerEntry) protocol.ResidueItem {
	kind := protocol.ResidueKindDecision
	switch e.Kind {
	case LedgerKindAssumption:
		kind = protocol.ResidueKindAssumption
	case LedgerKindConstraint:
		kind = protocol.ResidueKindConstraint
	case LedgerKindDecision:
		kind = protocol.ResidueKindDecision
	}
	conf := e.Confidence
	if conf == "" {
		conf = LedgerConfidenceMed
	}
	sources := []string{"ledger:" + e.ID}
	for _, ref := range e.EvidenceRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		sources = append(sources, ref)
	}
	files := append([]string(nil), e.ScopePaths...)
	return protocol.ResidueItem{
		ID:         "ledger-" + e.ID,
		Kind:       kind,
		Text:       truncateResidueText(e.Statement),
		Confidence: conf,
		Freshness:  residueFreshness(e),
		SourceIDs:  sources,
		FileRefs:   files,
		LedgerID:   e.ID,
	}
}

func residueFreshness(e LedgerEntry) string {
	if strings.EqualFold(strings.TrimSpace(e.Freshness), "stale") {
		return "stale"
	}
	return "fresh"
}

func extractResidueFromMessage(r *protocol.CompactionResidue, m provider.Message, src string) {
	switch m.Role {
	case provider.RoleUser, provider.RoleAssistant:
		extractResidueFromText(r, m.Text, src, defaultConfidenceForRole(m.Role))
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				// Tool call names alone are not residues; args may carry markers.
				extractResidueFromText(r, string(tc.Args), "tool:"+tc.ID, "medium")
			}
		}
	case provider.RoleTool:
		if m.ToolResult == nil {
			return
		}
		srcID := src
		if m.ToolResult.CallID != "" {
			srcID = "tool:" + m.ToolResult.CallID
		}
		extractResidueFromText(r, m.ToolResult.Output, srcID, "medium")
	}
}

func defaultConfidenceForRole(role provider.Role) string {
	if role == provider.RoleUser {
		return "high"
	}
	return "medium"
}

func extractResidueFromText(r *protocol.CompactionResidue, text, src, defaultConf string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Skip prior compact markers to avoid re-ingesting our own residue prose
	// as new unmarked facts.
	if strings.HasPrefix(text, compactMarkerPrefix) {
		// Still pull structured sections if present (rebuild round-trip).
		extractFromResidueMarkerBody(r, text, src)
		return
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip common list bullets.
		line = strings.TrimLeft(line, "-*• \t")
		line = strings.TrimSpace(line)
		if m := residueMarkerLine.FindStringSubmatch(line); m != nil {
			rawKind := m[1]
			if rawKind == "" {
				rawKind = m[2]
			}
			body := strings.TrimSpace(m[3])
			if body == "" {
				continue
			}
			kind := normalizeResidueKind(rawKind)
			item := protocol.ResidueItem{
				ID:         residueItemID(kind, body, src),
				Kind:       kind,
				Text:       truncateResidueText(body),
				Confidence: confidenceForKind(kind, defaultConf),
				Freshness:  "fresh",
				SourceIDs:  []string{src},
				FileRefs:   extractFileRefs(body),
			}
			appendResidueItem(r, item)
		}
	}
}

// extractFromResidueMarkerBody re-parses a prior compact marker's structured
// sections so repeated compaction keeps provenance items.
func extractFromResidueMarkerBody(r *protocol.CompactionResidue, marker, src string) {
	// Look for "## Decisions" / "## Facts" / "## Open questions" blocks.
	// Only list items ("- …") inside a section are residues; footers like
	// "Continue from the recent context below." are ignored.
	section := ""
	for _, line := range strings.Split(marker, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(trim, "## Decisions"):
			section = protocol.ResidueKindDecision
			continue
		case strings.EqualFold(trim, "## Facts"):
			section = protocol.ResidueKindFact
			continue
		case strings.EqualFold(trim, "## Open questions"):
			section = protocol.ResidueKindOpenQuestion
			continue
		case strings.HasPrefix(trim, "## "):
			section = ""
			continue
		case trim == "" || strings.HasPrefix(strings.ToLower(trim), "continue from"):
			// Blank line or continue footer ends the current list section.
			if strings.HasPrefix(strings.ToLower(trim), "continue from") {
				section = ""
			}
			continue
		}
		if section == "" {
			continue
		}
		// Require a list marker so prose footers are not re-ingested.
		if !strings.HasPrefix(trim, "- ") && !strings.HasPrefix(trim, "* ") && !strings.HasPrefix(trim, "• ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimLeft(trim, "-*• \t"))
		if body == "" || strings.HasPrefix(body, "sources:") {
			continue
		}
		// Drop trailing " (sources: …)" / " [confidence]" annotations.
		if i := strings.LastIndex(body, " (sources:"); i > 0 {
			body = strings.TrimSpace(body[:i])
		}
		if i := strings.LastIndex(body, " ["); i > 0 && strings.HasSuffix(body, "]") {
			// confidence suffix from writeResidueSection
			body = strings.TrimSpace(body[:i])
		}
		if body == "" {
			continue
		}
		item := protocol.ResidueItem{
			ID:         residueItemID(section, body, src),
			Kind:       section,
			Text:       truncateResidueText(body),
			Confidence: confidenceForKind(section, "medium"),
			Freshness:  "stale",
			SourceIDs:  []string{src},
			FileRefs:   extractFileRefs(body),
		}
		appendResidueItem(r, item)
	}
}

func normalizeResidueKind(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "decision":
		return protocol.ResidueKindDecision
	case "fact":
		return protocol.ResidueKindFact
	case "open", "open_question", "question", "todo":
		return protocol.ResidueKindOpenQuestion
	case "assumption":
		return protocol.ResidueKindAssumption
	case "constraint":
		return protocol.ResidueKindConstraint
	default:
		return protocol.ResidueKindFact
	}
}

func confidenceForKind(kind, fallback string) string {
	switch kind {
	case protocol.ResidueKindDecision, protocol.ResidueKindConstraint:
		if fallback == "" {
			return "high"
		}
		return fallback
	case protocol.ResidueKindOpenQuestion:
		return "medium"
	default:
		if fallback == "" {
			return "medium"
		}
		return fallback
	}
}

func residueItemID(kind, text, src string) string {
	// Stable-ish short id from kind + first runes + source (not cryptographic).
	t := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, text)
	for strings.Contains(t, "--") {
		t = strings.ReplaceAll(t, "--", "-")
	}
	t = strings.Trim(t, "-")
	if utf8.RuneCountInString(t) > 24 {
		t = string([]rune(t)[:24])
	}
	if t == "" {
		t = "item"
	}
	src = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, src)
	return kind + "-" + t + "-" + src
}

func truncateResidueText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxResidueItemText {
		return s
	}
	return string([]rune(s)[:maxResidueItemText]) + "…"
}

func extractFileRefs(text string) []string {
	matches := residuePathToken.FindAllStringSubmatch(text, maxFileRefsPerItem*2)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		p := filepath.Clean(strings.TrimSpace(m[1]))
		if p == "" || p == "." {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= maxFileRefsPerItem {
			break
		}
	}
	return out
}

func appendResidueItem(r *protocol.CompactionResidue, item protocol.ResidueItem) {
	if r == nil || strings.TrimSpace(item.Text) == "" {
		return
	}
	// Dedupe by kind+normalized text (keep first / higher-confidence).
	if residueHasText(r, item.Kind, item.Text) {
		return
	}
	switch item.Kind {
	case protocol.ResidueKindDecision, protocol.ResidueKindAssumption, protocol.ResidueKindConstraint:
		if len(r.Decisions) >= maxResidueItemsPerKind {
			return
		}
		// Assumptions/constraints ride in the decisions bucket for the
		// residual document (issue groups them under decisions).
		r.Decisions = append(r.Decisions, item)
	case protocol.ResidueKindOpenQuestion:
		if len(r.OpenQuestions) >= maxResidueItemsPerKind {
			return
		}
		r.OpenQuestions = append(r.OpenQuestions, item)
	default:
		if len(r.Facts) >= maxResidueItemsPerKind {
			return
		}
		item.Kind = protocol.ResidueKindFact
		r.Facts = append(r.Facts, item)
	}
}

func residueHasText(r *protocol.CompactionResidue, kind, text string) bool {
	norm := normalizeResidueText(text)
	check := func(items []protocol.ResidueItem) bool {
		for _, it := range items {
			if normalizeResidueText(it.Text) == norm {
				return true
			}
			// Cross-kind: same text already present elsewhere.
			_ = kind
		}
		return false
	}
	return check(r.Facts) || check(r.Decisions) || check(r.OpenQuestions)
}

func normalizeResidueText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// RebuildPromptSkeleton formats a residue into a model-facing continue
// skeleton (facts / decisions / open questions + optional summary).
// Empty string when residue is nil or empty of content.
func RebuildPromptSkeleton(r *protocol.CompactionResidue) string {
	if r == nil || residueEmpty(r) {
		return ""
	}
	var b strings.Builder
	b.WriteString("Structured residual from prior conversation (not a full transcript).\n")
	if s := strings.TrimSpace(r.Summary); s != "" {
		b.WriteString("\n## Summary\n")
		b.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			b.WriteByte('\n')
		}
	}
	writeResidueSection(&b, "Decisions", r.Decisions)
	writeResidueSection(&b, "Facts", r.Facts)
	writeResidueSection(&b, "Open questions", r.OpenQuestions)
	if len(r.PinnedKinds) > 0 {
		b.WriteString("\n## Pinned context layers\n")
		b.WriteString(strings.Join(r.PinnedKinds, ", "))
		b.WriteByte('\n')
	}
	b.WriteString("\nContinue from the recent context below.\n")
	return strings.TrimSpace(b.String())
}

func writeResidueSection(b *strings.Builder, title string, items []protocol.ResidueItem) {
	if len(items) == 0 {
		return
	}
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteByte('\n')
	for _, it := range items {
		b.WriteString("- ")
		if it.Kind != "" && it.Kind != protocol.ResidueKindFact &&
			it.Kind != protocol.ResidueKindDecision && it.Kind != protocol.ResidueKindOpenQuestion {
			b.WriteString("[")
			b.WriteString(it.Kind)
			b.WriteString("] ")
		}
		b.WriteString(it.Text)
		if len(it.SourceIDs) > 0 {
			b.WriteString(" (sources: ")
			b.WriteString(strings.Join(it.SourceIDs, ", "))
			b.WriteByte(')')
		}
		if it.Confidence != "" {
			b.WriteString(" [")
			b.WriteString(it.Confidence)
			b.WriteByte(']')
		}
		b.WriteByte('\n')
	}
}

// residueForMarker returns the residue used in the model-facing compact marker.
// When the decision_ledger system layer will still compose, ledger-sourced
// items are omitted from the marker to avoid double-injecting the same
// statements (they remain on CompactionCompleted.Residue for rebuild/export).
func residueForMarker(r *protocol.CompactionResidue, ledgerLayerActive bool) *protocol.CompactionResidue {
	if r == nil {
		return nil
	}
	if !ledgerLayerActive {
		return r
	}
	out := *r
	out.Decisions = nil
	for _, d := range r.Decisions {
		if d.LedgerID != "" {
			continue
		}
		out.Decisions = append(out.Decisions, d)
	}
	if residueEmpty(&out) {
		return nil
	}
	return &out
}

// residueCompactMarker builds the model-facing replacement for dropped history
// when a structured residue is available.
func residueCompactMarker(removed int, r *protocol.CompactionResidue) string {
	skel := RebuildPromptSkeleton(r)
	if skel == "" {
		return compactMarker(removed)
	}
	body := fmt.Sprintf("%s residual of %d earlier messages:\n%s]",
		compactMarkerPrefix, removed, skel)
	if len(body) > maxResidueMarkerChars {
		// Prefer keeping decisions; fall back to plain marker if still huge.
		trimmed := *r
		if len(trimmed.Facts) > 8 {
			trimmed.Facts = trimmed.Facts[:8]
		}
		if len(trimmed.OpenQuestions) > 8 {
			trimmed.OpenQuestions = trimmed.OpenQuestions[:8]
		}
		if len(trimmed.Decisions) > 16 {
			trimmed.Decisions = trimmed.Decisions[:16]
		}
		skel = RebuildPromptSkeleton(&trimmed)
		body = fmt.Sprintf("%s residual of %d earlier messages:\n%s]",
			compactMarkerPrefix, removed, skel)
		if len(body) > maxResidueMarkerChars {
			return compactMarker(removed)
		}
	}
	return body
}

// collectActiveLedgerEntries returns active ledger rows when a source is wired.
func (e *Engine) collectActiveLedgerEntries() []LedgerEntry {
	if e == nil || e.opts.Ledger == nil {
		return nil
	}
	entries, err := e.opts.Ledger.ActiveSlice("", "", e.opts.WorkDir)
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make([]LedgerEntry, 0, len(entries))
	for _, ent := range entries {
		out = append(out, cloneLedgerEntry(ent))
	}
	return out
}

func cloneLedgerEntry(e LedgerEntry) LedgerEntry {
	out := e
	out.EvidenceRefs = append([]string(nil), e.EvidenceRefs...)
	out.ScopePaths = append([]string(nil), e.ScopePaths...)
	out.ScopeTaskIDs = append([]string(nil), e.ScopeTaskIDs...)
	return out
}
