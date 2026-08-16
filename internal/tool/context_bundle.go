package tool

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Context bundle limits (spawn attach + child read).
const (
	MaxContextBundleItems          = 64
	MaxContextBundlePaths          = 64
	MaxContextBundleConstraints    = 32
	MaxContextBundleAcceptance     = 32
	MaxContextBundleArtifacts      = 32
	MaxContextBundleFilePins       = 32
	MaxContextBundleTextRunes      = 32 * 1024
	MaxContextBundleItemTextRunes  = 16 * 1024
	MaxContextBundleGoalRunes      = 4 * 1024
	MaxContextBundlePathRunes      = 1024
	MaxContextBundleItemIDRunes    = 128
	MaxContextBundleConstraintRune = 2 * 1024
)

// ContextBundle is the sealed context package attached at task/delegate spawn.
// Snake_case JSON matches tool args; protocol.ContextBundle is the camelCase wire form.
type ContextBundle struct {
	Goal          string              `json:"goal,omitempty"`
	Acceptance    []string            `json:"acceptance,omitempty"`
	AllowedPaths  []string            `json:"allowed_paths,omitempty"`
	RequiredPaths []string            `json:"required_paths,omitempty"`
	Artifacts     []BundleArtifactRef `json:"artifacts,omitempty"`
	Constraints   []string            `json:"constraints,omitempty"`
	Items         []ContextBundleItem `json:"items,omitempty"`
	FilePins      []ContextFilePin    `json:"file_pins,omitempty"`
}

// BundleArtifactRef is an artifact id(+version/type) inside a context bundle.
type BundleArtifactRef struct {
	ID      string `json:"id"`
	Version int    `json:"version,omitempty"`
	Type    string `json:"type,omitempty"`
}

// ContextBundleItem is one addressable entry for provenance citations.
type ContextBundleItem struct {
	ID       string             `json:"id"`
	Kind     string             `json:"kind,omitempty"`
	Title    string             `json:"title,omitempty"`
	Text     string             `json:"text,omitempty"`
	Path     string             `json:"path,omitempty"`
	Artifact *BundleArtifactRef `json:"artifact,omitempty"`
	Hash     string             `json:"hash,omitempty"`
}

// ContextFilePin seals a path with optional hash and/or snapshot text.
type ContextFilePin struct {
	Path string `json:"path"`
	Hash string `json:"hash,omitempty"`
	Text string `json:"text,omitempty"`
}

// MissingContextEntry is one gap reported on a blocked completion handoff.
type MissingContextEntry struct {
	Kind       string `json:"kind"`
	Path       string `json:"path,omitempty"`
	Question   string `json:"question,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	ItemID     string `json:"item_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// Empty reports whether b has no attachable content.
func (b ContextBundle) Empty() bool {
	return strings.TrimSpace(b.Goal) == "" &&
		len(b.Acceptance) == 0 &&
		len(b.AllowedPaths) == 0 &&
		len(b.RequiredPaths) == 0 &&
		len(b.Artifacts) == 0 &&
		len(b.Constraints) == 0 &&
		len(b.Items) == 0 &&
		len(b.FilePins) == 0
}

// Clone returns a deep copy safe for callers to mutate.
func (b ContextBundle) Clone() ContextBundle {
	out := ContextBundle{
		Goal:          b.Goal,
		Acceptance:    append([]string(nil), b.Acceptance...),
		AllowedPaths:  append([]string(nil), b.AllowedPaths...),
		RequiredPaths: append([]string(nil), b.RequiredPaths...),
		Constraints:   append([]string(nil), b.Constraints...),
	}
	if len(b.Artifacts) > 0 {
		out.Artifacts = append([]BundleArtifactRef(nil), b.Artifacts...)
	}
	if len(b.Items) > 0 {
		out.Items = make([]ContextBundleItem, len(b.Items))
		for i, it := range b.Items {
			out.Items[i] = it
			if it.Artifact != nil {
				cp := *it.Artifact
				out.Items[i].Artifact = &cp
			}
		}
	}
	if len(b.FilePins) > 0 {
		out.FilePins = append([]ContextFilePin(nil), b.FilePins...)
	}
	return out
}

// NormalizeContextBundle trims, validates, and bounds a spawn-time bundle.
// Empty input returns a zero bundle and nil error.
func NormalizeContextBundle(in ContextBundle) (ContextBundle, error) {
	if in.Empty() {
		return ContextBundle{}, nil
	}
	out := ContextBundle{}

	goal := strings.TrimSpace(in.Goal)
	if n := utf8.RuneCountInString(goal); n > MaxContextBundleGoalRunes {
		return ContextBundle{}, fmt.Errorf("context_bundle: goal exceeds %d runes (%d)", MaxContextBundleGoalRunes, n)
	}
	out.Goal = goal

	acc, err := normalizeBoundedStrings("acceptance", in.Acceptance, MaxContextBundleAcceptance, MaxContextBundleConstraintRune)
	if err != nil {
		return ContextBundle{}, err
	}
	out.Acceptance = acc

	allowed, err := normalizeBundlePaths("allowed_paths", in.AllowedPaths)
	if err != nil {
		return ContextBundle{}, err
	}
	out.AllowedPaths = allowed

	required, err := normalizeBundlePaths("required_paths", in.RequiredPaths)
	if err != nil {
		return ContextBundle{}, err
	}
	out.RequiredPaths = required

	constraints, err := normalizeBoundedStrings("constraints", in.Constraints, MaxContextBundleConstraints, MaxContextBundleConstraintRune)
	if err != nil {
		return ContextBundle{}, err
	}
	out.Constraints = constraints

	if len(in.Artifacts) > MaxContextBundleArtifacts {
		return ContextBundle{}, fmt.Errorf("context_bundle: artifacts exceeds %d items", MaxContextBundleArtifacts)
	}
	for i, a := range in.Artifacts {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			return ContextBundle{}, fmt.Errorf("context_bundle: artifacts[%d]: id is required", i)
		}
		out.Artifacts = append(out.Artifacts, BundleArtifactRef{
			ID:      id,
			Version: a.Version,
			Type:    strings.TrimSpace(a.Type),
		})
	}

	if len(in.FilePins) > MaxContextBundleFilePins {
		return ContextBundle{}, fmt.Errorf("context_bundle: file_pins exceeds %d items", MaxContextBundleFilePins)
	}
	for i, p := range in.FilePins {
		path, err := normalizeBundlePath(p.Path)
		if err != nil {
			return ContextBundle{}, fmt.Errorf("context_bundle: file_pins[%d]: %w", i, err)
		}
		if path == "" {
			return ContextBundle{}, fmt.Errorf("context_bundle: file_pins[%d]: path is required", i)
		}
		text := p.Text
		if n := utf8.RuneCountInString(text); n > MaxContextBundleTextRunes {
			return ContextBundle{}, fmt.Errorf("context_bundle: file_pins[%d]: text exceeds %d runes", i, MaxContextBundleTextRunes)
		}
		out.FilePins = append(out.FilePins, ContextFilePin{
			Path: path,
			Hash: strings.TrimSpace(p.Hash),
			Text: text,
		})
	}

	if len(in.Items) > MaxContextBundleItems {
		return ContextBundle{}, fmt.Errorf("context_bundle: items exceeds %d", MaxContextBundleItems)
	}
	seenIDs := make(map[string]struct{}, len(in.Items))
	for i, it := range in.Items {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: id is required", i)
		}
		if n := utf8.RuneCountInString(id); n > MaxContextBundleItemIDRunes {
			return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: id exceeds %d runes", i, MaxContextBundleItemIDRunes)
		}
		if _, dup := seenIDs[id]; dup {
			return ContextBundle{}, fmt.Errorf("context_bundle: duplicate item id %q", id)
		}
		seenIDs[id] = struct{}{}
		kind := strings.ToLower(strings.TrimSpace(it.Kind))
		if kind != "" {
			switch kind {
			case "goal", "acceptance", "path", "artifact", "constraint", "note", "file_pin", "other":
			default:
				return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: unknown kind %q", i, it.Kind)
			}
		}
		if n := utf8.RuneCountInString(it.Text); n > MaxContextBundleItemTextRunes {
			return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: text exceeds %d runes", i, MaxContextBundleItemTextRunes)
		}
		path := ""
		if strings.TrimSpace(it.Path) != "" {
			path, err = normalizeBundlePath(it.Path)
			if err != nil {
				return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: %w", i, err)
			}
		}
		item := ContextBundleItem{
			ID:    id,
			Kind:  kind,
			Title: strings.TrimSpace(it.Title),
			Text:  it.Text,
			Path:  path,
			Hash:  strings.TrimSpace(it.Hash),
		}
		if it.Artifact != nil {
			aid := strings.TrimSpace(it.Artifact.ID)
			if aid == "" {
				return ContextBundle{}, fmt.Errorf("context_bundle: items[%d]: artifact.id is required", i)
			}
			item.Artifact = &BundleArtifactRef{
				ID:      aid,
				Version: it.Artifact.Version,
				Type:    strings.TrimSpace(it.Artifact.Type),
			}
		}
		out.Items = append(out.Items, item)
	}

	// Synthesize addressable items for top-level fields when the lead omitted
	// explicit items — enables provenance citations without boilerplate.
	out.Items = ensureSyntheticBundleItems(out)
	return out, nil
}

func ensureSyntheticBundleItems(b ContextBundle) []ContextBundleItem {
	items := append([]ContextBundleItem(nil), b.Items...)
	have := make(map[string]struct{}, len(items))
	for _, it := range items {
		have[it.ID] = struct{}{}
	}
	add := func(id, kind, text, path string, art *BundleArtifactRef) {
		if _, ok := have[id]; ok {
			return
		}
		have[id] = struct{}{}
		items = append(items, ContextBundleItem{
			ID:       id,
			Kind:     kind,
			Text:     text,
			Path:     path,
			Artifact: art,
		})
	}
	if g := strings.TrimSpace(b.Goal); g != "" {
		add("goal", "goal", g, "", nil)
	}
	for i, a := range b.Acceptance {
		add(fmt.Sprintf("acceptance-%d", i+1), "acceptance", a, "", nil)
	}
	for i, p := range b.RequiredPaths {
		add(fmt.Sprintf("required-path-%d", i+1), "path", "", p, nil)
	}
	for i, p := range b.AllowedPaths {
		add(fmt.Sprintf("allowed-path-%d", i+1), "path", "", p, nil)
	}
	for i, a := range b.Artifacts {
		ref := a
		add(fmt.Sprintf("artifact-%d", i+1), "artifact", "", "", &ref)
	}
	for i, c := range b.Constraints {
		add(fmt.Sprintf("constraint-%d", i+1), "constraint", c, "", nil)
	}
	for i, p := range b.FilePins {
		add(fmt.Sprintf("file-pin-%d", i+1), "file_pin", p.Text, p.Path, nil)
	}
	return items
}

func normalizeBoundedStrings(field string, in []string, maxN, maxRunes int) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxN {
		return nil, fmt.Errorf("context_bundle: %s exceeds %d items", field, maxN)
	}
	out := make([]string, 0, len(in))
	for i, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if n := utf8.RuneCountInString(s); n > maxRunes {
			return nil, fmt.Errorf("context_bundle: %s[%d] exceeds %d runes", field, i, maxRunes)
		}
		out = append(out, s)
	}
	return out, nil
}

func normalizeBundlePaths(field string, in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxContextBundlePaths {
		return nil, fmt.Errorf("context_bundle: %s exceeds %d items", field, MaxContextBundlePaths)
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for i, p := range in {
		np, err := normalizeBundlePath(p)
		if err != nil {
			return nil, fmt.Errorf("context_bundle: %s[%d]: %w", field, i, err)
		}
		if np == "" {
			continue
		}
		if _, ok := seen[np]; ok {
			continue
		}
		seen[np] = struct{}{}
		out = append(out, np)
	}
	return out, nil
}

func normalizeBundlePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil
	}
	if n := utf8.RuneCountInString(p); n > MaxContextBundlePathRunes {
		return "", fmt.Errorf("path exceeds %d runes", MaxContextBundlePathRunes)
	}
	// Reject absolute and parent escapes; keep workspace-relative globs.
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("path must be workspace-relative (got %q)", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must not escape workspace (got %q)", p)
	}
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path must be workspace-relative (got %q)", p)
	}
	return clean, nil
}

// NormalizeMissingContext validates handoff missing_context entries.
func NormalizeMissingContext(in []MissingContextEntry) ([]MissingContextEntry, error) {
	if len(in) == 0 {
		return nil, nil
	}
	const maxN = 32
	if len(in) > maxN {
		return nil, fmt.Errorf("missing_context: at most %d entries (got %d)", maxN, len(in))
	}
	out := make([]MissingContextEntry, 0, len(in))
	for i, e := range in {
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		if kind == "" {
			return nil, fmt.Errorf("missing_context[%d]: kind is required", i)
		}
		switch kind {
		case "path", "question", "artifact", "item", "other":
		default:
			return nil, fmt.Errorf("missing_context[%d]: unknown kind %q", i, e.Kind)
		}
		entry := MissingContextEntry{
			Kind:       kind,
			Path:       strings.TrimSpace(e.Path),
			Question:   strings.TrimSpace(e.Question),
			ArtifactID: strings.TrimSpace(e.ArtifactID),
			ItemID:     strings.TrimSpace(e.ItemID),
			Detail:     strings.TrimSpace(e.Detail),
		}
		if entry.Path != "" {
			np, err := normalizeBundlePath(entry.Path)
			if err != nil {
				// Keep raw path for lead visibility when model supplies abs paths.
				entry.Path = strings.TrimSpace(e.Path)
			} else {
				entry.Path = np
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
