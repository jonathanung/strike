package plugin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// UpdateReview summarizes old → new changes for user confirmation before update (§6.4).
// Catalog metadata never grants trust; executable changes invalidate prior trust.
type UpdateReview struct {
	ID                string
	OldVersion        string
	NewVersion        string
	OldDigest         string
	NewDigest         string // content digest when known; may be empty pre-download
	OldSource         *SourceIdentity
	NewSource         SourceIdentity
	CapabilityAdded   []string
	CapabilityRemoved []string
	ContribAdded      []string // e.g. "agents", "mcp"
	ContribRemoved    []string
	// ExecutableChanged is true when MCP/harness/hook/process-pane entries differ.
	ExecutableChanged bool
	ExecutableDiffs   []string // human lines (no secret values)
	// TrustInvalidated is true when prior trust must be re-reviewed.
	TrustInvalidated bool
	HadTrust         bool
}

// Format returns a multi-line human review (no secrets).
func (r UpdateReview) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Update review: %s\n", r.ID)
	fmt.Fprintf(&b, "  version:  %s → %s\n", emptyDash(r.OldVersion), emptyDash(r.NewVersion))
	fmt.Fprintf(&b, "  digest:   %s → %s\n", emptyDash(r.OldDigest), emptyDash(r.NewDigest))
	if r.OldSource != nil {
		fmt.Fprintf(&b, "  source:   %s → %s\n", r.OldSource.String(), r.NewSource.String())
	} else {
		fmt.Fprintf(&b, "  source:   %s\n", r.NewSource.String())
	}
	if len(r.CapabilityAdded) > 0 {
		fmt.Fprintf(&b, "  caps + :  %s\n", strings.Join(r.CapabilityAdded, ", "))
	}
	if len(r.CapabilityRemoved) > 0 {
		fmt.Fprintf(&b, "  caps - :  %s\n", strings.Join(r.CapabilityRemoved, ", "))
	}
	if len(r.ContribAdded) > 0 {
		fmt.Fprintf(&b, "  types + : %s\n", strings.Join(r.ContribAdded, ", "))
	}
	if len(r.ContribRemoved) > 0 {
		fmt.Fprintf(&b, "  types - : %s\n", strings.Join(r.ContribRemoved, ", "))
	}
	if r.ExecutableChanged {
		fmt.Fprintf(&b, "  executable content changed: yes\n")
		for _, line := range r.ExecutableDiffs {
			fmt.Fprintf(&b, "    - %s\n", line)
		}
	} else {
		fmt.Fprintf(&b, "  executable content changed: no\n")
	}
	if r.TrustInvalidated {
		if r.HadTrust {
			fmt.Fprintf(&b, "  trust:    INVALIDATED (re-review required before executable activation)\n")
		} else {
			fmt.Fprintf(&b, "  trust:    none recorded; executable activation still requires trust (#728)\n")
		}
	}
	fmt.Fprintf(&b, "  note:     catalog/install metadata cannot enable or execute content\n")
	return b.String()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// BuildUpdateReview compares an installed plugin (old) with a candidate manifest/source.
// newContentDigest may be empty when not yet computed.
func BuildUpdateReview(old InstalledPlugin, newMan Manifest, newSource SourceIdentity, newContentDigest string) UpdateReview {
	r := UpdateReview{
		ID:         old.ID,
		OldVersion: old.Version,
		NewVersion: newMan.Version,
		OldDigest:  old.Digest,
		NewDigest:  newContentDigest,
		OldSource:  old.Source,
		NewSource:  newSource,
		HadTrust:   old.Trust != nil && old.Trust.Digest != "",
	}

	oldCaps := capabilitySet(old)
	newCaps := stringSliceSet(mergeCapabilityTags(newMan))
	r.CapabilityAdded, r.CapabilityRemoved = diffStringSets(oldCaps, newCaps)

	oldTypes := contribTypeSet(old)
	newTypes := contribTypeSetFromManifest(newMan)
	r.ContribAdded, r.ContribRemoved = diffStringSets(oldTypes, newTypes)

	oldExec := executableSnapshot(old)
	newExec := executableSnapshotFromManifest(newMan)
	r.ExecutableDiffs = diffExecutableSnapshots(oldExec, newExec)
	r.ExecutableChanged = len(r.ExecutableDiffs) > 0 || oldExec.fingerprint != newExec.fingerprint

	// Trust invalidation: any content digest change, source change, or executable change.
	digestChanged := r.OldDigest != "" && r.NewDigest != "" && !digestsEqual(r.OldDigest, r.NewDigest)
	sourceChanged := !sourceIdentityEqual(old.Source, &newSource)
	r.TrustInvalidated = r.HadTrust && (digestChanged || sourceChanged || r.ExecutableChanged ||
		r.OldVersion != r.NewVersion)
	// Even without prior trust, surface that executable changes require review.
	if r.ExecutableChanged || digestChanged || sourceChanged {
		if r.HadTrust {
			r.TrustInvalidated = true
		}
	}
	// AC: updates that change executable content invalidate prior trust.
	if r.ExecutableChanged && r.HadTrust {
		r.TrustInvalidated = true
	}
	// Always note invalidation path when digest changes with trust present.
	if !r.TrustInvalidated && r.HadTrust && (digestChanged || r.ExecutableChanged) {
		r.TrustInvalidated = true
	}
	return r
}

func capabilitySet(ip InstalledPlugin) map[string]struct{} {
	if ip.Manifest == nil {
		return map[string]struct{}{}
	}
	return stringSliceSet(mergeCapabilityTags(*ip.Manifest))
}

func stringSliceSet(ss []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

// mergeCapabilityTags returns declared capabilities plus inferred executable kinds.
func mergeCapabilityTags(m Manifest) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, c := range m.Capabilities {
		add(c)
	}
	// Inferred from contributions.
	if len(m.Contributions.Agents) > 0 {
		add("agents")
	}
	if len(m.Contributions.Skills) > 0 {
		add("skills")
	}
	if len(m.Contributions.Workflows) > 0 {
		add("workflows")
	}
	if len(m.Contributions.Themes) > 0 {
		add("themes")
	}
	if len(m.Contributions.Providers) > 0 {
		add("providers")
	}
	if len(m.Contributions.MCP) > 0 {
		add("mcp")
	}
	if len(m.Contributions.Harnesses) > 0 {
		add("harnesses")
	}
	if len(m.Contributions.Hooks) > 0 {
		add("hooks")
	}
	if len(m.Contributions.Panes) > 0 {
		add("panes")
	}
	sort.Strings(out)
	return out
}

func contribTypeSet(ip InstalledPlugin) map[string]struct{} {
	if ip.Manifest == nil {
		return map[string]struct{}{}
	}
	return contribTypeSetFromManifest(*ip.Manifest)
}

func contribTypeSetFromManifest(m Manifest) map[string]struct{} {
	out := map[string]struct{}{}
	c := m.Contributions
	if len(c.Agents) > 0 {
		out["agents"] = struct{}{}
	}
	if len(c.Skills) > 0 {
		out["skills"] = struct{}{}
	}
	if len(c.Workflows) > 0 {
		out["workflows"] = struct{}{}
	}
	if len(c.Themes) > 0 {
		out["themes"] = struct{}{}
	}
	if len(c.Providers) > 0 {
		out["providers"] = struct{}{}
	}
	if len(c.MCP) > 0 {
		out["mcp"] = struct{}{}
	}
	if len(c.Harnesses) > 0 {
		out["harnesses"] = struct{}{}
	}
	if len(c.Hooks) > 0 {
		out["hooks"] = struct{}{}
	}
	if len(c.Panes) > 0 {
		out["panes"] = struct{}{}
	}
	return out
}

func diffStringSets(old, new map[string]struct{}) (added, removed []string) {
	for k := range new {
		if _, ok := old[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

type execSnap struct {
	fingerprint string
	lines       []string // stable sorted summary lines
}

func executableSnapshot(ip InstalledPlugin) execSnap {
	if ip.Manifest == nil {
		return execSnap{}
	}
	return executableSnapshotFromManifest(*ip.Manifest)
}

func executableSnapshotFromManifest(m Manifest) execSnap {
	var lines []string
	for _, raw := range m.Contributions.MCP {
		lines = append(lines, "mcp:"+safeExecJSONLine(raw))
	}
	for _, raw := range m.Contributions.Harnesses {
		lines = append(lines, "harness:"+safeExecJSONLine(raw))
	}
	for _, raw := range m.Contributions.Hooks {
		lines = append(lines, "hook:"+safeExecJSONLine(raw))
	}
	for _, raw := range m.Contributions.Panes {
		// Process panes are executable-class; include in fingerprint.
		lines = append(lines, "pane:"+safeExecJSONLine(raw))
	}
	sort.Strings(lines)
	return execSnap{
		fingerprint: strings.Join(lines, "\n"),
		lines:       lines,
	}
}

// safeExecJSONLine extracts non-secret fields for review (drops env/header values).
func safeExecJSONLine(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "<unparseable>"
	}
	// Pull common keys; never include env/headers object values.
	getStr := func(k string) string {
		v, ok := m[k]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return ""
		}
		return s
	}
	getStrs := func(k string) []string {
		v, ok := m[k]
		if !ok {
			return nil
		}
		var s []string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil
		}
		return s
	}
	getKeys := func(k string) []string {
		v, ok := m[k]
		if !ok {
			return nil
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil
		}
		var keys []string
		for kk := range obj {
			keys = append(keys, kk)
		}
		sort.Strings(keys)
		return keys
	}
	parts := []string{
		"name=" + getStr("name"),
		"id=" + getStr("id"),
		"transport=" + getStr("transport"),
		"command=" + getStr("command"),
		"type=" + getStr("type"),
		"event=" + getStr("event"),
		"matcher=" + getStr("matcher"),
		"mode=" + getStr("mode"),
		"url=" + getStr("url"), // URL path only is still ok; redact userinfo at print time if needed
		"args=" + strings.Join(getStrs("args"), ","),
		"envKeys=" + strings.Join(getKeys("env"), ","),
		"headerKeys=" + strings.Join(getKeys("headers"), ","),
	}
	// Drop empty keys for stability.
	var nz []string
	for _, p := range parts {
		if strings.HasSuffix(p, "=") {
			continue
		}
		nz = append(nz, p)
	}
	return strings.Join(nz, " ")
}

func diffExecutableSnapshots(old, new execSnap) []string {
	oldSet := map[string]struct{}{}
	for _, l := range old.lines {
		oldSet[l] = struct{}{}
	}
	newSet := map[string]struct{}{}
	for _, l := range new.lines {
		newSet[l] = struct{}{}
	}
	var out []string
	for _, l := range new.lines {
		if _, ok := oldSet[l]; !ok {
			out = append(out, "added "+l)
		}
	}
	for _, l := range old.lines {
		if _, ok := newSet[l]; !ok {
			out = append(out, "removed "+l)
		}
	}
	sort.Strings(out)
	return out
}

func sourceIdentityEqual(a *SourceIdentity, b *SourceIdentity) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Type == b.Type &&
		a.Path == b.Path &&
		a.URL == b.URL &&
		a.Ref == b.Ref &&
		a.Commit == b.Commit &&
		a.Subdir == b.Subdir &&
		a.Registry == b.Registry &&
		a.Package == b.Package &&
		a.Version == b.Version &&
		digestsEqual(a.Digest, b.Digest)
}
