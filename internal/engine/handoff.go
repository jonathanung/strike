package engine

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// noteMutatedPath records a workspace-relative path for completion handoffs.
// Absolute paths outside WorkDir are stored cleaned as-is.
func (e *Engine) noteMutatedPath(absPath string) {
	if e == nil || absPath == "" {
		return
	}
	absPath = filepath.Clean(absPath)
	rel := absPath
	if wd := strings.TrimSpace(e.opts.WorkDir); wd != "" {
		if r, err := filepath.Rel(wd, absPath); err == nil && r != "." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && r != ".." {
			rel = filepath.ToSlash(r)
		} else if r == "." {
			return
		}
	} else {
		rel = filepath.ToSlash(absPath)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return
	}
	e.mutatedMu.Lock()
	defer e.mutatedMu.Unlock()
	if e.mutatedFiles == nil {
		e.mutatedFiles = make(map[string]struct{})
	}
	e.mutatedFiles[rel] = struct{}{}
}

// mutatedPathsSnapshot returns sorted workspace-relative paths mutated so far.
func (e *Engine) mutatedPathsSnapshot() []string {
	if e == nil {
		return nil
	}
	e.mutatedMu.Lock()
	defer e.mutatedMu.Unlock()
	if len(e.mutatedFiles) == 0 {
		return nil
	}
	out := make([]string, 0, len(e.mutatedFiles))
	for p := range e.mutatedFiles {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// buildCompletionHandoff merges model-supplied structured fields with engine
// file tracking. Always returns a usable handoff (empty slices allowed).
func buildCompletionHandoff(status protocol.ChildStatus, assistantText string, trackedFiles []string) protocol.CompletionHandoff {
	assistantText = strings.TrimSpace(assistantText)
	parsed, ok := parseCompletionHandoff(assistantText)

	var h protocol.CompletionHandoff
	if ok {
		h = parsed
		h.Incomplete = false
	} else {
		h.Incomplete = true
	}

	if strings.TrimSpace(h.Summary) == "" {
		if assistantText != "" {
			h.Summary = assistantText
		} else {
			h.Summary = defaultHandoffSummary(status)
		}
	}

	h.FilesChanged = mergeUniquePaths(trackedFiles, h.FilesChanged)
	if h.FilesChanged == nil {
		h.FilesChanged = []string{}
	}
	if h.Findings == nil {
		h.Findings = []string{}
	}
	if h.Blockers == nil {
		h.Blockers = []string{}
	}

	// Failure/cancel: ensure a blocker hint when the model left blockers empty.
	if (status == protocol.ChildStatusFailed || status == protocol.ChildStatusCanceled) &&
		len(h.Blockers) == 0 && h.Incomplete {
		switch status {
		case protocol.ChildStatusCanceled:
			h.Blockers = []string{"task canceled"}
		default:
			h.Blockers = []string{"task failed"}
		}
	}

	return h
}

func defaultHandoffSummary(status protocol.ChildStatus) string {
	switch status {
	case protocol.ChildStatusCanceled:
		return "task canceled"
	case protocol.ChildStatusFailed:
		return "task failed"
	case protocol.ChildStatusBlocked:
		return "task blocked (verification failed)"
	default:
		return "task completed"
	}
}

// marshalVerificationModelJSON is the snake_case verification report for notices.
func marshalVerificationModelJSON(v protocol.VerificationReport) string {
	type checkView struct {
		Name       string `json:"name,omitempty"`
		Kind       string `json:"kind"`
		Value      string `json:"value,omitempty"`
		Passed     bool   `json:"passed"`
		ExitCode   int    `json:"exit_code,omitempty"`
		Output     string `json:"output,omitempty"`
		Error      string `json:"error,omitempty"`
		DurationMs int64  `json:"duration_ms,omitempty"`
	}
	type envView struct {
		WorkDir    string `json:"work_dir,omitempty"`
		SessionID  string `json:"session_id,omitempty"`
		WorktreeID string `json:"worktree_id,omitempty"`
		ModelID    string `json:"model_id,omitempty"`
		StartedAt  string `json:"started_at,omitempty"`
		FinishedAt string `json:"finished_at,omitempty"`
	}
	type view struct {
		Passed     bool        `json:"passed"`
		Claimed    bool        `json:"claimed"`
		Verified   bool        `json:"verified"`
		Checks     []checkView `json:"checks"`
		Env        envView     `json:"env"`
		Summary    string      `json:"summary,omitempty"`
		DurationMs int64       `json:"duration_ms,omitempty"`
	}
	checks := make([]checkView, 0, len(v.Checks))
	for _, c := range v.Checks {
		checks = append(checks, checkView{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
		})
	}
	if checks == nil {
		checks = []checkView{}
	}
	b, err := json.Marshal(view{
		Passed:   v.Passed,
		Claimed:  v.Claimed,
		Verified: v.Verified,
		Checks:   checks,
		Env: envView{
			WorkDir:    v.Env.WorkDir,
			SessionID:  v.Env.SessionID,
			WorktreeID: v.Env.WorktreeID,
			ModelID:    v.Env.ModelID,
			StartedAt:  v.Env.StartedAt,
			FinishedAt: v.Env.FinishedAt,
		},
		Summary:    v.Summary,
		DurationMs: v.DurationMs,
	})
	if err != nil {
		return `{"passed":false,"claimed":true,"verified":false,"checks":[]}`
	}
	return string(b)
}

// parseCompletionHandoff extracts a structured handoff from assistant text.
// Accepts a whole-message JSON object, a ```json fenced block, or a trailing
// JSON object. Keys may be snake_case or camelCase.
func parseCompletionHandoff(text string) (protocol.CompletionHandoff, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return protocol.CompletionHandoff{}, false
	}

	candidates := handoffJSONCandidates(text)
	for _, raw := range candidates {
		if h, ok := decodeHandoffObject(raw); ok {
			return h, true
		}
	}
	return protocol.CompletionHandoff{}, false
}

func handoffJSONCandidates(text string) []string {
	var out []string
	// Whole message.
	out = append(out, text)
	// Fenced ```json ... ``` or ``` ... ```.
	for _, block := range extractFencedJSONBlocks(text) {
		out = append(out, block)
	}
	// Trailing object: last '{' … matching '}'.
	if obj := trailingJSONObject(text); obj != "" && obj != text {
		out = append(out, obj)
	}
	return out
}

func extractFencedJSONBlocks(text string) []string {
	var blocks []string
	rest := text
	for {
		start := strings.Index(rest, "```")
		if start < 0 {
			break
		}
		rest = rest[start+3:]
		// Optional language tag on first line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			tag := strings.TrimSpace(rest[:nl])
			if tag == "" || strings.EqualFold(tag, "json") || strings.EqualFold(tag, "handoff") {
				rest = rest[nl+1:]
			}
		}
		end := strings.Index(rest, "```")
		if end < 0 {
			break
		}
		blocks = append(blocks, strings.TrimSpace(rest[:end]))
		rest = rest[end+3:]
	}
	return blocks
}

func trailingJSONObject(text string) string {
	start := strings.LastIndexByte(text, '{')
	if start < 0 {
		return ""
	}
	raw := strings.TrimSpace(text[start:])
	if !json.Valid([]byte(raw)) {
		// Try to balance braces when trailing prose is absent but nested.
		if balanced := balanceJSONObject(text[start:]); balanced != "" && json.Valid([]byte(balanced)) {
			return balanced
		}
		return ""
	}
	return raw
}

func balanceJSONObject(s string) string {
	depth := 0
	inStr := false
	esc := false
	for i, r := range s {
		if inStr {
			if esc {
				esc = false
				continue
			}
			if r == '\\' {
				esc = true
				continue
			}
			if r == '"' {
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[:i+1])
			}
		}
	}
	return ""
}

func decodeHandoffObject(raw string) (protocol.CompletionHandoff, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return protocol.CompletionHandoff{}, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return protocol.CompletionHandoff{}, false
	}
	// Require at least one known handoff key so arbitrary JSON is not treated
	// as a completion handoff.
	if !hasHandoffKey(m) {
		return protocol.CompletionHandoff{}, false
	}
	h := protocol.CompletionHandoff{
		Summary:               firstString(m, "summary"),
		Verification:          firstString(m, "verification"),
		RecommendedNextAction: firstString(m, "recommended_next_action", "recommendedNextAction"),
		FilesChanged:          firstStringSlice(m, "files_changed", "filesChanged"),
		Findings:              firstStringSlice(m, "findings"),
		Blockers:              firstStringSlice(m, "blockers"),
		ArtifactRefs:          firstArtifactRefs(m, "artifact_refs", "artifactRefs"),
		MissingContext:        firstMissingContext(m, "missing_context", "missingContext"),
		Provenance:            firstStringSlice(m, "provenance"),
	}
	if b, ok := firstBool(m, "incomplete"); ok {
		h.Incomplete = b
	}
	// Plan section refinement fields (plan_delegate / #724).
	if title, body, found := sectionPtrs(m); found {
		if title != nil {
			h.SectionTitle = *title
		}
		if body != nil {
			h.SectionBody = *body
			h.SectionBodySet = true
		}
	}
	return h, true
}

func hasHandoffKey(m map[string]json.RawMessage) bool {
	keys := []string{
		"summary", "files_changed", "filesChanged",
		"verification", "findings", "blockers",
		"recommended_next_action", "recommendedNextAction",
		"artifact_refs", "artifactRefs",
		"missing_context", "missingContext",
		"provenance",
		// Plan section refinement (#724).
		"section_body", "sectionBody", "section_title", "sectionTitle",
		"plan_section", "planSection",
	}
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func firstMissingContext(m map[string]json.RawMessage, keys ...string) []protocol.MissingContextEntry {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var objs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &objs); err != nil {
			continue
		}
		out := make([]protocol.MissingContextEntry, 0, len(objs))
		for _, o := range objs {
			e := protocol.MissingContextEntry{
				Kind:       strings.ToLower(firstString(o, "kind")),
				Path:       firstString(o, "path"),
				Question:   firstString(o, "question"),
				ArtifactID: firstString(o, "artifact_id", "artifactId"),
				ItemID:     firstString(o, "item_id", "itemId"),
				Detail:     firstString(o, "detail"),
			}
			if e.Kind == "" && e.Path == "" && e.Question == "" && e.ArtifactID == "" && e.ItemID == "" && e.Detail == "" {
				continue
			}
			if e.Kind == "" {
				switch {
				case e.Path != "":
					e.Kind = "path"
				case e.Question != "":
					e.Kind = "question"
				case e.ArtifactID != "":
					e.Kind = "artifact"
				case e.ItemID != "":
					e.Kind = "item"
				default:
					e.Kind = "other"
				}
			}
			out = append(out, e)
		}
		return out
	}
	return nil
}

func firstArtifactRefs(m map[string]json.RawMessage, keys ...string) []protocol.ArtifactRef {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		// Array of objects or strings.
		var objs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &objs); err == nil {
			out := make([]protocol.ArtifactRef, 0, len(objs))
			for _, o := range objs {
				ref := protocol.ArtifactRef{
					ID:   firstString(o, "id"),
					Type: firstString(o, "type"),
				}
				if v, ok := firstInt(o, "version"); ok {
					ref.Version = v
				}
				if ref.ID == "" {
					continue
				}
				out = append(out, ref)
			}
			return out
		}
		// Bare string ids.
		var ss []string
		if err := json.Unmarshal(raw, &ss); err == nil {
			out := make([]protocol.ArtifactRef, 0, len(ss))
			for _, id := range trimNonEmpty(ss) {
				out = append(out, protocol.ArtifactRef{ID: id})
			}
			return out
		}
		// Single string id.
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s == "" {
				return []protocol.ArtifactRef{}
			}
			return []protocol.ArtifactRef{{ID: s}}
		}
	}
	return nil
}

func firstInt(m map[string]json.RawMessage, keys ...string) (int, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var n int
		if err := json.Unmarshal(raw, &n); err == nil {
			return n, true
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstStringSlice(m map[string]json.RawMessage, keys ...string) []string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var ss []string
		if err := json.Unmarshal(raw, &ss); err == nil {
			return trimNonEmpty(ss)
		}
		// Single string → one-element slice.
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s == "" {
				return []string{}
			}
			return []string{s}
		}
	}
	return nil
}

func firstBool(m map[string]json.RawMessage, keys ...string) (bool, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return b, true
		}
	}
	return false, false
}

func trimNonEmpty(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mergeUniquePaths(a, b []string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range a {
		add(p)
	}
	for _, p := range b {
		add(p)
	}
	sort.Strings(out)
	return out
}

// handoffModelView is the snake_case JSON shape for model-facing notices and
// task_status (matches the issue schema / tool conventions).
type handoffModelView struct {
	Summary               string                      `json:"summary"`
	FilesChanged          []string                    `json:"files_changed"`
	Verification          string                      `json:"verification,omitempty"`
	Findings              []string                    `json:"findings"`
	Blockers              []string                    `json:"blockers"`
	RecommendedNextAction string                      `json:"recommended_next_action,omitempty"`
	ArtifactRefs          []handoffArtifactRefView    `json:"artifact_refs,omitempty"`
	MissingContext        []handoffMissingContextView `json:"missing_context,omitempty"`
	Provenance            []string                    `json:"provenance,omitempty"`
	Incomplete            bool                        `json:"incomplete,omitempty"`
}

type handoffArtifactRefView struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
	Type    string `json:"type,omitempty"`
}

type handoffMissingContextView struct {
	Kind       string `json:"kind"`
	Path       string `json:"path,omitempty"`
	Question   string `json:"question,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func handoffToModelView(h protocol.CompletionHandoff) handoffModelView {
	files := h.FilesChanged
	if files == nil {
		files = []string{}
	}
	findings := h.Findings
	if findings == nil {
		findings = []string{}
	}
	blockers := h.Blockers
	if blockers == nil {
		blockers = []string{}
	}
	var refs []handoffArtifactRefView
	if len(h.ArtifactRefs) > 0 {
		refs = make([]handoffArtifactRefView, 0, len(h.ArtifactRefs))
		for _, r := range h.ArtifactRefs {
			id := strings.TrimSpace(r.ID)
			if id == "" {
				continue
			}
			refs = append(refs, handoffArtifactRefView{
				ID:      id,
				Version: r.Version,
				Type:    strings.TrimSpace(r.Type),
			})
		}
	}
	var missing []handoffMissingContextView
	if len(h.MissingContext) > 0 {
		missing = make([]handoffMissingContextView, 0, len(h.MissingContext))
		for _, e := range h.MissingContext {
			missing = append(missing, handoffMissingContextView{
				Kind:       e.Kind,
				Path:       e.Path,
				Question:   e.Question,
				ArtifactID: e.ArtifactID,
				ItemID:     e.ItemID,
				Detail:     e.Detail,
			})
		}
	}
	return handoffModelView{
		Summary:               h.Summary,
		FilesChanged:          files,
		Verification:          h.Verification,
		Findings:              findings,
		Blockers:              blockers,
		RecommendedNextAction: h.RecommendedNextAction,
		ArtifactRefs:          refs,
		MissingContext:        missing,
		Provenance:            append([]string(nil), h.Provenance...),
		Incomplete:            h.Incomplete,
	}
}

func marshalHandoffModelJSON(h protocol.CompletionHandoff) string {
	b, err := json.Marshal(handoffToModelView(h))
	if err != nil {
		return `{"summary":"","files_changed":[],"findings":[],"blockers":[]}`
	}
	return string(b)
}

// childHandoffSystemPrompt teaches delegated children to emit a structured
// completion handoff as their final message.
const childHandoffSystemPrompt = `## Completion handoff (required)

When you finish this delegated task, end with a machine-parseable JSON handoff
object (as the whole final message, a trailing object, or a ` + "```json" + ` fence).
The engine merges engine-tracked file mutations into files_changed.

Success schema (empty arrays/strings allowed when honest):
{
  "summary": "short outcome",
  "files_changed": ["path/relative.go"],
  "verification": "what you ran and results",
  "findings": ["notable discovery or risk"],
  "blockers": [],
  "recommended_next_action": "concrete next step for the lead",
  "artifact_refs": [{"id": "artifactId", "version": 1, "type": "findings"}],
  "provenance": ["goal", "artifact-1"],
  "missing_context": []
}

Prefer artifact_refs (from artifact_write) over inlining large findings/patches/
test reports. Cite context_bundle item ids in provenance when conclusions rest
on sealed context. If you lack required context, set missing_context (non-empty
→ blocked) instead of guessing. For parallel writers, prefer patch_collab submit
(apply_patch envelope) plus artifact_refs type=patch so the lead can
preview/reject/apply instead of editing the shared tree in place. On failure or
cancel, still return summary, blockers, partial files_changed, and
recommended_next_action when known. Prefer this JSON over free-form prose alone
— the lead reads [child.completed] handoff JSON, not only chat text.
`
