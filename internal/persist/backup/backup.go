// Package backup is the portable whole-gateway snapshot format (#1339): one
// versioned JSON archive with a manifest, per-section sha256 integrity
// hashes, and an auditable list of what was deliberately left out.
//
// This package owns only the archive document and its validation. Reading
// stores for export and writing them back on restore stays with the
// caller, which must go through each store's own write path.
//
// The sha256 hashes detect truncation and accidental corruption. They are
// not a signature: anyone who edits a section can recompute its hash. The
// restore side therefore treats an archive as untrusted input and never
// restores trust dials (permission mode, sandbox) from it.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// Format identifies an instance backup archive.
	Format = "strike.instance-backup"
	// Version is the archive document version.
	Version = 1
	// Filename is the download name the daemon suggests.
	Filename = "strike-backup.json"
)

// Section names. A section holds one store's records as a JSON array.
const (
	SectionSessions  = "sessions"
	SectionMemory    = "memory"
	SectionSchedules = "schedules"
	SectionSettings  = "settings"
)

// KnownSections lists every section this version can restore, in restore
// order (settings first so a restored session opens with its defaults).
var KnownSections = []string{SectionSettings, SectionMemory, SectionSchedules, SectionSessions}

// Exclusion kinds.
const (
	// ExcludeSecret is never exported, by design.
	ExcludeSecret = "secret"
	// ExcludePolicy is left out because restoring it from a file would
	// change a trust boundary on the target gateway.
	ExcludePolicy = "policy"
	// ExcludeLater is not covered by this archive version yet.
	ExcludeLater = "not-yet-included"
	// ExcludeUnavailable is a store this gateway does not run.
	ExcludeUnavailable = "unavailable"
)

// ErrInvalid wraps every archive validation failure.
var ErrInvalid = errors.New("invalid backup archive")

// Archive is the whole backup document.
type Archive struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// StrikeVersion is the exporting build, for support only.
	StrikeVersion string `json:"strikeVersion,omitempty"`
	// Encrypted is always false in this version; it is spelled out so the
	// file says plainly that it is readable by anyone who holds it.
	Encrypted bool                       `json:"encrypted"`
	Manifest  Manifest                   `json:"manifest"`
	Sections  map[string]json.RawMessage `json:"sections"`
}

// Manifest lists every section with its integrity hash and every category
// that was left out, so an omission is auditable rather than silent.
type Manifest struct {
	Sections []SectionEntry `json:"sections"`
	Excluded []Exclusion    `json:"excluded"`
}

// SectionEntry describes one section.
type SectionEntry struct {
	Name   string `json:"name"`
	Items  int    `json:"items"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Exclusion is one category the archive does not carry.
type Exclusion struct {
	Category string `json:"category"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
}

// New starts an empty archive stamped at now.
func New(now time.Time, strikeVersion string) *Archive {
	return &Archive{
		Format:        Format,
		Version:       Version,
		CreatedAt:     now.UTC(),
		StrikeVersion: strings.TrimSpace(strikeVersion),
		Sections:      map[string]json.RawMessage{},
	}
}

// Add stores records (a slice) as section name with items entries.
// Adding the same section twice is an error.
func (a *Archive) Add(name string, items int, records any) error {
	if !known(name) {
		return fmt.Errorf("backup: unknown section %q", name)
	}
	if _, dup := a.Sections[name]; dup {
		return fmt.Errorf("backup: section %q added twice", name)
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("backup: encode %s: %w", name, err)
	}
	if a.Sections == nil {
		a.Sections = map[string]json.RawMessage{}
	}
	a.Sections[name] = raw
	a.Manifest.Sections = append(a.Manifest.Sections, SectionEntry{
		Name:   name,
		Items:  items,
		Bytes:  len(raw),
		SHA256: Hash(raw),
	})
	return nil
}

// Exclude records one omitted category.
func (a *Archive) Exclude(category, kind, reason string) {
	a.Manifest.Excluded = append(a.Manifest.Excluded, Exclusion{Category: category, Kind: kind, Reason: reason})
}

// Encode renders the archive. Section bytes are written exactly as hashed:
// the document is compact so no re-indentation can disturb them.
func (a *Archive) Encode() ([]byte, error) {
	return json.Marshal(a)
}

// Hash is the lowercase hex sha256 of a section's compact JSON bytes.
func Hash(raw []byte) string {
	sum := sha256.Sum256(compact(raw))
	return hex.EncodeToString(sum[:])
}

// compact normalises insignificant whitespace so a pretty-printed copy of
// an intact archive still verifies. Invalid JSON is hashed as-is (and then
// rejected when the section is decoded).
func compact(raw []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// Decode parses and verifies an archive: format and version match, every
// manifest section is present exactly once with a matching hash, and no
// section exists that the manifest does not list. Nothing is restored from
// an archive Decode rejects.
func Decode(data []byte) (*Archive, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty file", ErrInvalid)
	}
	var a Archive
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%w: bad JSON: %v", ErrInvalid, err)
	}
	if a.Format != Format {
		return nil, fmt.Errorf("%w: format %q, want %q", ErrInvalid, a.Format, Format)
	}
	if a.Version != Version {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrInvalid, a.Version, Version)
	}
	if a.Encrypted {
		return nil, fmt.Errorf("%w: encrypted archives are not supported", ErrInvalid)
	}
	listed := map[string]bool{}
	for _, e := range a.Manifest.Sections {
		if !known(e.Name) {
			return nil, fmt.Errorf("%w: unknown section %q", ErrInvalid, e.Name)
		}
		if listed[e.Name] {
			return nil, fmt.Errorf("%w: section %q listed twice", ErrInvalid, e.Name)
		}
		listed[e.Name] = true
		raw, ok := a.Sections[e.Name]
		if !ok {
			return nil, fmt.Errorf("%w: section %q is missing", ErrInvalid, e.Name)
		}
		if got := Hash(raw); got != strings.ToLower(strings.TrimSpace(e.SHA256)) {
			return nil, fmt.Errorf("%w: section %q failed its integrity check (sha256 %s, manifest %s)", ErrInvalid, e.Name, got, e.SHA256)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("%w: section %q is not a JSON array", ErrInvalid, e.Name)
		}
		if len(items) != e.Items {
			return nil, fmt.Errorf("%w: section %q has %d items, manifest says %d", ErrInvalid, e.Name, len(items), e.Items)
		}
	}
	names := make([]string, 0, len(a.Sections))
	for name := range a.Sections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !listed[name] {
			return nil, fmt.Errorf("%w: section %q is not in the manifest", ErrInvalid, name)
		}
	}
	return &a, nil
}

// Section decodes section name into dst. A section the archive does not
// carry leaves dst untouched and reports false.
func (a *Archive) Section(name string, dst any) (bool, error) {
	raw, ok := a.Sections[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return true, fmt.Errorf("%w: section %q: %v", ErrInvalid, name, err)
	}
	return true, nil
}

func known(name string) bool {
	for _, k := range KnownSections {
		if k == name {
			return true
		}
	}
	return false
}

// Restore actions reported per item.
const (
	// ActionCreate writes a record the target does not have.
	ActionCreate = "create"
	// ActionSkip leaves the target as it is (identical, or a kept value).
	ActionSkip = "skip"
	// ActionCopy restores a differing record under a new name or id; the
	// target's own record is never overwritten.
	ActionCopy = "copy"
	// ActionFail is a record the target refused; other records continue.
	ActionFail = "fail"
)

// Item is one restore decision.
type Item struct {
	Section string `json:"section"`
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Action  string `json:"action"`
	// Target is the id or key written when it differs from ID (copies).
	Target string `json:"target,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Report is the restore plan (DryRun) or its outcome.
type Report struct {
	DryRun    bool      `json:"dryRun"`
	CreatedAt time.Time `json:"archiveCreatedAt"`
	// Counts is section -> action -> n.
	Counts   map[string]map[string]int `json:"counts"`
	Items    []Item                    `json:"items"`
	Excluded []Exclusion               `json:"excluded"`
}

// NewReport starts a report for archive a.
func NewReport(a *Archive, dryRun bool) *Report {
	return &Report{
		DryRun:    dryRun,
		CreatedAt: a.CreatedAt,
		Counts:    map[string]map[string]int{},
		Items:     []Item{},
		Excluded:  append([]Exclusion{}, a.Manifest.Excluded...),
	}
}

// Add records one item and bumps its count.
func (r *Report) Add(it Item) {
	if r.Counts[it.Section] == nil {
		r.Counts[it.Section] = map[string]int{}
	}
	r.Counts[it.Section][it.Action]++
	r.Items = append(r.Items, it)
}

// CopyName is the name a differing record restores under: base plus a
// " (restored)" suffix, numbered when that is taken too. taken reports
// whether a candidate already exists on the target.
func CopyName(base string, taken func(string) bool) string {
	cand := base + " (restored)"
	for n := 2; taken(cand) && n < 1000; n++ {
		cand = fmt.Sprintf("%s (restored %d)", base, n)
	}
	return cand
}
