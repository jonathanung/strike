package tool

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// OverlapPolicy controls multi-agent path conflict handling.
//
//	off   — track touches but never warn or block
//	warn  — allow the write; surface a warning (tool output + optional event)
//	block — refuse the write when another active session holds the path
const (
	OverlapOff   = "off"
	OverlapWarn  = "warn"
	OverlapBlock = "block"
)

// NormalizeOverlapPolicy maps config strings to off|warn|block (default warn).
func NormalizeOverlapPolicy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case OverlapOff:
		return OverlapOff
	case OverlapBlock:
		return OverlapBlock
	default:
		return OverlapWarn
	}
}

// LeaseMode is exclusive (one holder) or shared (many readers/writers OK together).
const (
	LeaseExclusive = "exclusive"
	LeaseShared    = "shared"
)

// PathHolder is one session that has touched or leased a path.
type PathHolder struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name,omitempty"`
	Active    bool   `json:"active"`
	// Source is "touch" (observed write) or "lease".
	Source string `json:"source,omitempty"`
	// Mode is exclusive|shared for leases; empty for plain touches.
	Mode string `json:"mode,omitempty"`
}

// PathClaim is one path row in an ownership snapshot.
type PathClaim struct {
	Path    string       `json:"path"`
	Holders []PathHolder `json:"holders"`
}

// OwnershipSnapshot is the team-wide ownership/overlap map.
type OwnershipSnapshot struct {
	Policy   string      `json:"policy"`
	Claims   []PathClaim `json:"claims"`
	Overlaps []string    `json:"overlaps,omitempty"` // paths with ≥2 active holders
}

// TouchResult is the outcome of recording a write touch.
type TouchResult struct {
	Path    string
	Display string
	Overlap bool
	Blocked bool
	Warning string
	Holders []PathHolder // other active holders (excludes self)
	Policy  string
}

// OverlapNotify is invoked when a touch or lease hits an active conflict.
// Engine wires this to emit protocol.path.overlap.
type OverlapNotify func(result TouchResult)

// PathOwnership tracks file/path claims across concurrent agents on one team.
// A nil receiver is a no-op on every method (single-agent / no team).
//
// Paths are stored as cleaned absolute paths. Display strings (relative when
// possible) are kept for messages. Active holders are sessions that have not
// been DeactivateSession'd (child still running or lead). Terminal children
// stay in history but no longer cause overlap.
type PathOwnership struct {
	mu     sync.Mutex
	policy string // off|warn|block
	// touches: abs path → session id → meta
	touches map[string]map[string]holderMeta
	// leases: abs prefix → session id → lease
	leases map[string]map[string]leaseMeta
	// Notify is optional; called outside the lock when overlap is detected.
	Notify OverlapNotify
}

type holderMeta struct {
	name   string
	active bool
	at     time.Time
}

type leaseMeta struct {
	name    string
	mode    string // exclusive|shared
	active  bool
	at      time.Time
	display string
}

// NewPathOwnership constructs a tracker with the given policy (normalized).
func NewPathOwnership(policy string) *PathOwnership {
	return &PathOwnership{
		policy:  NormalizeOverlapPolicy(policy),
		touches: make(map[string]map[string]holderMeta),
		leases:  make(map[string]map[string]leaseMeta),
	}
}

// SetPolicy updates the overlap policy (normalized).
func (o *PathOwnership) SetPolicy(policy string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.policy = NormalizeOverlapPolicy(policy)
}

// Policy returns the current normalized policy (warn when o is nil).
func (o *PathOwnership) Policy() string {
	if o == nil {
		return OverlapWarn
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.policy
}

// SetNotify installs the overlap callback (may be nil).
func (o *PathOwnership) SetNotify(fn OverlapNotify) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Notify = fn
}

// Touch records that sessionID wrote (or is about to write) absPath.
// display is used in warning/error text (typically workspace-relative).
// Self re-touches are fine. Disjoint paths never conflict.
func (o *PathOwnership) Touch(sessionID, name, absPath, display string) TouchResult {
	if o == nil {
		return TouchResult{}
	}
	sessionID = strings.TrimSpace(sessionID)
	absPath = cleanAbs(absPath)
	if sessionID == "" || absPath == "" {
		return TouchResult{}
	}
	if display == "" {
		display = absPath
	}
	name = strings.TrimSpace(name)

	o.mu.Lock()
	policy := o.policy
	others := o.activeConflictsLocked(sessionID, absPath)
	// Block policy: refuse without recording so a failed write does not claim.
	if len(others) > 0 && policy == OverlapBlock {
		notify := o.Notify
		o.mu.Unlock()
		res := TouchResult{
			Path:    absPath,
			Display: display,
			Overlap: true,
			Blocked: true,
			Warning: formatOverlapWarning(display, others, policy),
			Holders: others,
			Policy:  policy,
		}
		if notify != nil {
			notify(res)
		}
		return res
	}
	if o.touches[absPath] == nil {
		o.touches[absPath] = make(map[string]holderMeta)
	}
	prev := o.touches[absPath][sessionID]
	if name == "" {
		name = prev.name
	}
	o.touches[absPath][sessionID] = holderMeta{name: name, active: true, at: time.Now()}
	notify := o.Notify
	o.mu.Unlock()

	res := TouchResult{
		Path:    absPath,
		Display: display,
		Policy:  policy,
		Holders: others,
	}
	if len(others) == 0 || policy == OverlapOff {
		return res
	}
	res.Overlap = true
	res.Warning = formatOverlapWarning(display, others, policy)
	if notify != nil {
		notify(res)
	}
	return res
}

// RecordFilesChanged merges handoff/observed paths as touches for sessionID.
// Relative paths resolve under workDir; absolute paths are cleaned.
// Designed for structured completion handoffs (files_changed) when available.
func (o *PathOwnership) RecordFilesChanged(sessionID, name, workDir string, paths []string) []TouchResult {
	if o == nil || len(paths) == 0 {
		return nil
	}
	var out []TouchResult
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, display := resolveOwnershipPath(workDir, p)
		if abs == "" {
			continue
		}
		out = append(out, o.Touch(sessionID, name, abs, display))
	}
	return out
}

// AcquireLease claims a path prefix for sessionID.
// exclusive conflicts with any other active lease/touch under the prefix;
// shared conflicts only with exclusive leases (and still records the claim).
func (o *PathOwnership) AcquireLease(sessionID, name, absPrefix, display string, exclusive bool) TouchResult {
	if o == nil {
		return TouchResult{}
	}
	sessionID = strings.TrimSpace(sessionID)
	absPrefix = cleanAbs(absPrefix)
	if sessionID == "" || absPrefix == "" {
		return TouchResult{}
	}
	if display == "" {
		display = absPrefix
	}
	name = strings.TrimSpace(name)
	mode := LeaseShared
	if exclusive {
		mode = LeaseExclusive
	}

	o.mu.Lock()
	policy := o.policy
	others := o.activeLeaseConflictsLocked(sessionID, absPrefix, exclusive)
	if len(others) > 0 && policy == OverlapBlock {
		notify := o.Notify
		o.mu.Unlock()
		res := TouchResult{
			Path:    absPrefix,
			Display: display,
			Overlap: true,
			Blocked: true,
			Warning: formatOverlapWarning(display, others, policy),
			Holders: others,
			Policy:  policy,
		}
		if notify != nil {
			notify(res)
		}
		return res
	}
	if o.leases[absPrefix] == nil {
		o.leases[absPrefix] = make(map[string]leaseMeta)
	}
	o.leases[absPrefix][sessionID] = leaseMeta{
		name: name, mode: mode, active: true, at: time.Now(), display: display,
	}
	// Recompute after install for warn path (includes concurrent exclusive).
	others = o.activeLeaseConflictsLocked(sessionID, absPrefix, exclusive)
	notify := o.Notify
	o.mu.Unlock()

	res := TouchResult{Path: absPrefix, Display: display, Policy: policy, Holders: others}
	if len(others) == 0 || policy == OverlapOff {
		return res
	}
	res.Overlap = true
	res.Warning = formatOverlapWarning(display, others, policy)
	if notify != nil {
		notify(res)
	}
	return res
}

// ReleaseLease drops sessionID's lease on absPrefix (idempotent).
func (o *PathOwnership) ReleaseLease(sessionID, absPrefix string) {
	if o == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	absPrefix = cleanAbs(absPrefix)
	if sessionID == "" || absPrefix == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if m := o.leases[absPrefix]; m != nil {
		delete(m, sessionID)
		if len(m) == 0 {
			delete(o.leases, absPrefix)
		}
	}
}

// ReleaseAllLeases drops every lease held by sessionID.
func (o *PathOwnership) ReleaseAllLeases(sessionID string) {
	if o == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for prefix, m := range o.leases {
		delete(m, sessionID)
		if len(m) == 0 {
			delete(o.leases, prefix)
		}
	}
}

// DeactivateSession marks the session inactive so it no longer causes overlap
// (child completed/failed/canceled). Historical touches remain in Snapshot.
func (o *PathOwnership) DeactivateSession(sessionID string) {
	if o == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, m := range o.touches {
		if h, ok := m[sessionID]; ok {
			h.active = false
			m[sessionID] = h
		}
	}
	for _, m := range o.leases {
		if h, ok := m[sessionID]; ok {
			h.active = false
			m[sessionID] = h
		}
	}
}

// Clear removes all claims (team dissolve).
func (o *PathOwnership) Clear() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.touches = make(map[string]map[string]holderMeta)
	o.leases = make(map[string]map[string]leaseMeta)
}

// PathsForSession returns absolute paths touched or leased by sessionID
// (active or inactive). Sorted and de-duplicated. Callers may relativize
// under the workspace root for model-facing output (#774 files_touched).
func (o *PathOwnership) PathsForSession(sessionID string) []string {
	if o == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	seen := make(map[string]struct{})
	var out []string
	add := func(abs string) {
		if abs == "" {
			return
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	for abs, m := range o.touches {
		if _, ok := m[sessionID]; ok {
			add(abs)
		}
	}
	for abs, m := range o.leases {
		if _, ok := m[sessionID]; ok {
			add(abs)
		}
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a stable ownership map (paths sorted).
func (o *PathOwnership) Snapshot() OwnershipSnapshot {
	if o == nil {
		return OwnershipSnapshot{Policy: OverlapWarn, Claims: []PathClaim{}}
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	// Union of touch paths and lease prefixes.
	pathSet := make(map[string]struct{})
	for p := range o.touches {
		pathSet[p] = struct{}{}
	}
	for p := range o.leases {
		pathSet[p] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	claims := make([]PathClaim, 0, len(paths))
	var overlaps []string
	for _, p := range paths {
		holders := o.holdersLocked(p)
		if len(holders) == 0 {
			continue
		}
		claims = append(claims, PathClaim{Path: p, Holders: holders})
		active := 0
		for _, h := range holders {
			if h.Active {
				active++
			}
		}
		if active >= 2 {
			overlaps = append(overlaps, p)
		}
	}
	return OwnershipSnapshot{
		Policy:   o.policy,
		Claims:   claims,
		Overlaps: overlaps,
	}
}

// holdersLocked collects touch + lease holders for path (exact + covering leases).
func (o *PathOwnership) holdersLocked(path string) []PathHolder {
	byID := make(map[string]PathHolder)
	if m := o.touches[path]; m != nil {
		for id, h := range m {
			byID[id] = PathHolder{
				SessionID: id,
				Name:      h.name,
				Active:    h.active,
				Source:    "touch",
			}
		}
	}
	for prefix, m := range o.leases {
		// Lease row itself, or a touch path covered by this lease prefix.
		if path != prefix && !pathCoveredByPrefix(path, prefix) {
			continue
		}
		for id, h := range m {
			// Prefer showing lease when both exist.
			byID[id] = PathHolder{
				SessionID: id,
				Name:      h.name,
				Active:    h.active,
				Source:    "lease",
				Mode:      h.mode,
			}
		}
	}
	if len(byID) == 0 {
		return nil
	}
	out := make([]PathHolder, 0, len(byID))
	for _, h := range byID {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// activeConflictsLocked returns other active holders that conflict with a touch.
func (o *PathOwnership) activeConflictsLocked(self, absPath string) []PathHolder {
	seen := make(map[string]PathHolder)
	// Other touches on the same path.
	if m := o.touches[absPath]; m != nil {
		for id, h := range m {
			if id == self || !h.active {
				continue
			}
			seen[id] = PathHolder{SessionID: id, Name: h.name, Active: true, Source: "touch"}
		}
	}
	// Active leases covering this path.
	for prefix, m := range o.leases {
		if !pathCoveredByPrefix(absPath, prefix) {
			continue
		}
		for id, h := range m {
			if id == self || !h.active {
				continue
			}
			// Shared leases do not block/warn plain touches from co-holders of
			// the same shared lease group — but exclusive always conflicts,
			// and any lease from a *different* session conflicts with a touch
			// (lease signals intent to own the tree).
			seen[id] = PathHolder{
				SessionID: id, Name: h.name, Active: true,
				Source: "lease", Mode: h.mode,
			}
		}
	}
	return holdersMapToSlice(seen)
}

// activeLeaseConflictsLocked returns conflicts for acquiring a lease.
func (o *PathOwnership) activeLeaseConflictsLocked(self, absPrefix string, exclusive bool) []PathHolder {
	seen := make(map[string]PathHolder)
	// Touches under the prefix.
	for path, m := range o.touches {
		if !pathCoveredByPrefix(path, absPrefix) {
			continue
		}
		for id, h := range m {
			if id == self || !h.active {
				continue
			}
			seen[id] = PathHolder{SessionID: id, Name: h.name, Active: true, Source: "touch"}
		}
	}
	// Other leases that overlap this prefix.
	for prefix, m := range o.leases {
		if !prefixesOverlap(absPrefix, prefix) {
			continue
		}
		for id, h := range m {
			if id == self || !h.active {
				continue
			}
			// Shared+shared is OK; exclusive conflicts with anything.
			if !exclusive && h.mode == LeaseShared {
				continue
			}
			seen[id] = PathHolder{
				SessionID: id, Name: h.name, Active: true,
				Source: "lease", Mode: h.mode,
			}
		}
	}
	return holdersMapToSlice(seen)
}

func holdersMapToSlice(m map[string]PathHolder) []PathHolder {
	if len(m) == 0 {
		return nil
	}
	out := make([]PathHolder, 0, len(m))
	for _, h := range m {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func formatOverlapWarning(display string, others []PathHolder, policy string) string {
	parts := make([]string, 0, len(others))
	for _, h := range others {
		label := h.SessionID
		if h.Name != "" {
			label = h.Name + " (" + shortSession(h.SessionID) + ")"
		} else {
			label = shortSession(h.SessionID)
		}
		if h.Source == "lease" && h.Mode != "" {
			label += " [" + h.Mode + " lease]"
		}
		parts = append(parts, label)
	}
	msg := fmt.Sprintf("path overlap on %s: also claimed by %s", display, strings.Join(parts, ", "))
	if policy == OverlapBlock {
		return msg + " (blocked by session.overlapPolicy=block)"
	}
	return msg + " (session.overlapPolicy=warn)"
}

func shortSession(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return id
	}
	// Match tool shortID: first 8 runes.
	r := []rune(id)
	if len(r) <= 8 {
		return id
	}
	return string(r[:8])
}

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		// Keep cleaned relative form when caller did not abs it.
		return filepath.Clean(p)
	}
	// Normalize identity (symlink parents, . segments) for grant/overlap match.
	if id, err := pathIdentity(p); err == nil && id != "" {
		return id
	}
	return filepath.Clean(p)
}

func resolveOwnershipPath(workDir, p string) (abs, display string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", ""
	}
	if filepath.IsAbs(p) {
		abs = cleanAbs(p)
		display = abs
		if workDir != "" {
			if rel, err := filepath.Rel(workDir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				display = rel
			}
		}
		return abs, display
	}
	display = filepath.Clean(p)
	if workDir == "" {
		return display, display
	}
	return filepath.Clean(filepath.Join(workDir, p)), display
}

// pathCoveredByPrefix reports whether path is prefix or a descendant.
func pathCoveredByPrefix(path, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if path == prefix {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, prefix+sep)
}

func prefixesOverlap(a, b string) bool {
	return pathCoveredByPrefix(a, b) || pathCoveredByPrefix(b, a)
}

// ClaimWrite is the write-tool entry point: records a touch and returns a
// warning (warn policy) or error (block policy). Nil Ownership / empty
// SessionID is a no-op so single-agent and read-only explore stay unaffected.
// OnOverlap (when set) is invoked on conflict so the engine can emit events.
func (tc *Context) ClaimWrite(absPath, display string) (warning string, err error) {
	if tc == nil || tc.Ownership == nil || strings.TrimSpace(tc.SessionID) == "" {
		return "", nil
	}
	res := tc.Ownership.Touch(tc.SessionID, tc.MemberName, absPath, display)
	if !res.Overlap {
		return "", nil
	}
	if tc.OnOverlap != nil {
		tc.OnOverlap(res)
	}
	if res.Blocked {
		return "", fmt.Errorf("%s", res.Warning)
	}
	return res.Warning, nil
}

// AppendOverlapWarning appends an overlap warning to a tool output string.
func AppendOverlapWarning(output, warning string) string {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return output
	}
	if strings.TrimSpace(output) == "" {
		return "warning: " + warning
	}
	return output + "\nwarning: " + warning
}
