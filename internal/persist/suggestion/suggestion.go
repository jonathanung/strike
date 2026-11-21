// Package suggestion is the daemon's Ideas inbox store (#1323): proactive
// suggestions for new work that background producers deposit and the user
// accepts, snoozes or dismisses.
//
// Rules the store enforces (not the UI):
//   - Producers name themselves (Source) and a Kind; a muted source or kind
//     is refused, so the inbox cannot become a nag feed.
//   - Suggestions dedupe by (Source, EvidenceKey) for their whole lifetime,
//     dismissed and accepted included: a recurring heartbeat does not refile
//     the same idea daily. Expired suggestions are pruned, freeing the key.
//   - Every suggestion expires (default 7 days, at most 30).
//   - Dismissals are counted per source/kind as a preference signal; a
//     "never" dismissal mutes the kind.
//   - Text fields are scrubbed of credential-shaped strings on write.
//
// Accepting only changes state. Starting the session is the cockpit's
// normal session-start path; there is no execution path here.
package suggestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/pkg/redact"
)

// State is a suggestion's lifecycle state.
type State string

const (
	StateOpen      State = "open"
	StateSnoozed   State = "snoozed"
	StateAccepted  State = "accepted"
	StateDismissed State = "dismissed"
)

const (
	DefaultTTL   = 7 * 24 * time.Hour
	MaxTTL       = 30 * 24 * time.Hour
	MaxSnooze    = 30 * 24 * time.Hour
	MaxStored    = 500
	MaxMuted     = 100
	maxTitle     = 120
	maxWhy       = 2000
	maxPrompt    = 8000
	maxToken     = 64
	maxEffort    = 40
	maxKey       = 200
	maxEvidence  = 10
	maxEvLabel   = 200
	maxEvPath    = 512
	maxEvURL     = 2048
	maxSessionID = 128
	fileName     = "suggestions.json"
)

var (
	ErrInvalid  = errors.New("invalid suggestion")
	ErrMuted    = errors.New("source or kind is muted")
	ErrNotFound = errors.New("suggestion not found")
	ErrState    = errors.New("suggestion is not open")
)

// Evidence links a suggestion to what prompted it.
type Evidence struct {
	Label     string `json:"label"`
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Suggestion is one Ideas inbox row.
type Suggestion struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Why         string     `json:"why"`
	Evidence    []Evidence `json:"evidence"`
	EvidenceKey string     `json:"evidenceKey"`
	Source      string     `json:"source"`
	Kind        string     `json:"kind"`
	Effort      string     `json:"effort,omitempty"`
	Prompt      string     `json:"prompt"`
	State       State      `json:"state"`
	SnoozeUntil *time.Time `json:"snoozeUntil,omitempty"`
	// Repeats counts producer refilings deduped onto this row.
	Repeats   int       `json:"repeats,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Prefs are the inbox controls: muted producers and kinds, plus the
// dismissal counts per "source/kind".
type Prefs struct {
	MutedSources []string       `json:"mutedSources"`
	MutedKinds   []string       `json:"mutedKinds"`
	Dismissals   map[string]int `json:"dismissals"`
}

// Draft is a producer's create request.
type Draft struct {
	Title       string
	Why         string
	Prompt      string
	Source      string
	Kind        string
	Effort      string
	EvidenceKey string
	Evidence    []Evidence
	// TTL 0 means DefaultTTL; values above MaxTTL are clamped.
	TTL time.Duration
}

type document struct {
	Suggestions []Suggestion `json:"suggestions"`
	Prefs       Prefs        `json:"prefs"`
	NextID      int64        `json:"nextId"`
}

// Store is a file-backed, concurrency-safe suggestion inbox.
type Store struct {
	mu   sync.Mutex
	path string
	doc  document
	now  func() time.Time
}

// Open loads (or starts) the store in dir. A corrupt file starts empty
// rather than blocking the daemon; it is overwritten on the next write.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, fileName), now: time.Now}
	raw, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var doc document
		if json.Unmarshal(raw, &doc) == nil {
			s.doc = doc
		}
	}
	s.normalizeLocked()
	return s, nil
}

// SetClock overrides the clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *Store) normalizeLocked() {
	if s.doc.Prefs.Dismissals == nil {
		s.doc.Prefs.Dismissals = map[string]int{}
	}
	if s.doc.Prefs.MutedSources == nil {
		s.doc.Prefs.MutedSources = []string{}
	}
	if s.doc.Prefs.MutedKinds == nil {
		s.doc.Prefs.MutedKinds = []string{}
	}
	if s.doc.Suggestions == nil {
		s.doc.Suggestions = []Suggestion{}
	}
}

// sweepLocked drops expired rows and wakes snoozes that are due. It
// reports whether anything changed.
func (s *Store) sweepLocked(now time.Time) bool {
	changed := false
	kept := s.doc.Suggestions[:0]
	for _, sg := range s.doc.Suggestions {
		if !sg.ExpiresAt.After(now) {
			changed = true
			continue
		}
		if sg.State == StateSnoozed && sg.SnoozeUntil != nil && !sg.SnoozeUntil.After(now) {
			sg.State = StateOpen
			sg.SnoozeUntil = nil
			sg.UpdatedAt = now
			changed = true
		}
		kept = append(kept, sg)
	}
	s.doc.Suggestions = kept
	return changed
}

func (s *Store) persistLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	raw, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, fileName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ValidToken reports whether s is a source/kind token: 1-64 of [a-z0-9._-].
func ValidToken(s string) bool {
	if s == "" || len(s) > maxToken {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func clean(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	return redact.String(s), true
}

func cleanEvidence(in []Evidence) ([]Evidence, error) {
	if len(in) > maxEvidence {
		return nil, fmt.Errorf("%w: at most %d evidence links", ErrInvalid, maxEvidence)
	}
	out := make([]Evidence, 0, len(in))
	for _, ev := range in {
		label, ok := clean(ev.Label, maxEvLabel)
		if !ok || label == "" {
			return nil, fmt.Errorf("%w: evidence label", ErrInvalid)
		}
		sid := strings.TrimSpace(ev.SessionID)
		if len(sid) > maxSessionID || strings.ContainsAny(sid, "/\\ \t\r\n") {
			return nil, fmt.Errorf("%w: evidence session id", ErrInvalid)
		}
		path, ok := clean(ev.Path, maxEvPath)
		if !ok {
			return nil, fmt.Errorf("%w: evidence path", ErrInvalid)
		}
		link := strings.TrimSpace(ev.URL)
		if link != "" {
			u, err := url.Parse(link)
			if err != nil || len(link) > maxEvURL || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("%w: evidence url must be http(s)", ErrInvalid)
			}
			link = u.String()
		}
		out = append(out, Evidence{Label: label, SessionID: sid, Path: path, URL: link})
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Create files a suggestion. It returns the stored row and whether it is
// new; a deduped refiling returns the existing row and false.
func (s *Store) Create(d Draft) (Suggestion, bool, error) {
	title, ok := clean(d.Title, maxTitle)
	if !ok || title == "" {
		return Suggestion{}, false, fmt.Errorf("%w: title (1-%d characters)", ErrInvalid, maxTitle)
	}
	why, ok := clean(d.Why, maxWhy)
	if !ok {
		return Suggestion{}, false, fmt.Errorf("%w: why (at most %d characters)", ErrInvalid, maxWhy)
	}
	prompt, ok := clean(d.Prompt, maxPrompt)
	if !ok || prompt == "" {
		return Suggestion{}, false, fmt.Errorf("%w: prompt (1-%d characters)", ErrInvalid, maxPrompt)
	}
	source := strings.TrimSpace(d.Source)
	if !ValidToken(source) {
		return Suggestion{}, false, fmt.Errorf("%w: source must be 1-64 of [a-z0-9._-]", ErrInvalid)
	}
	kind := strings.TrimSpace(d.Kind)
	if kind == "" {
		kind = "general"
	}
	if !ValidToken(kind) {
		return Suggestion{}, false, fmt.Errorf("%w: kind must be 1-64 of [a-z0-9._-]", ErrInvalid)
	}
	effort, ok := clean(d.Effort, maxEffort)
	if !ok {
		return Suggestion{}, false, fmt.Errorf("%w: effort", ErrInvalid)
	}
	evidence, err := cleanEvidence(d.Evidence)
	if err != nil {
		return Suggestion{}, false, err
	}
	key := strings.TrimSpace(d.EvidenceKey)
	if key == "" {
		key = kind + ":" + strings.ToLower(title)
	}
	if utf8.RuneCountInString(key) > maxKey {
		return Suggestion{}, false, fmt.Errorf("%w: evidence key", ErrInvalid)
	}
	ttl := d.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	swept := s.sweepLocked(now)
	if contains(s.doc.Prefs.MutedSources, source) || contains(s.doc.Prefs.MutedKinds, kind) {
		if swept {
			_ = s.persistLocked()
		}
		return Suggestion{}, false, ErrMuted
	}
	for i := range s.doc.Suggestions {
		sg := &s.doc.Suggestions[i]
		if sg.Source != source || sg.EvidenceKey != key {
			continue
		}
		if sg.State == StateOpen || sg.State == StateSnoozed {
			sg.Repeats++
			sg.UpdatedAt = now
		}
		if err := s.persistLocked(); err != nil {
			return Suggestion{}, false, err
		}
		return *sg, false, nil
	}
	s.doc.NextID++
	sg := Suggestion{
		ID:          fmt.Sprintf("sg-%d-%d", now.UnixMilli(), s.doc.NextID),
		Title:       title,
		Why:         why,
		Evidence:    evidence,
		EvidenceKey: key,
		Source:      source,
		Kind:        kind,
		Effort:      effort,
		Prompt:      prompt,
		State:       StateOpen,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}
	s.doc.Suggestions = append(s.doc.Suggestions, sg)
	s.capLocked()
	if err := s.persistLocked(); err != nil {
		return Suggestion{}, false, err
	}
	return sg, true, nil
}

// capLocked keeps at most MaxStored rows, dropping the oldest settled
// (accepted/dismissed) rows first, then the oldest of any state.
func (s *Store) capLocked() {
	over := len(s.doc.Suggestions) - MaxStored
	if over <= 0 {
		return
	}
	idx := make([]int, len(s.doc.Suggestions))
	for i := range idx {
		idx[i] = i
	}
	settled := func(st State) bool { return st == StateAccepted || st == StateDismissed }
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := s.doc.Suggestions[idx[a]], s.doc.Suggestions[idx[b]]
		if settled(x.State) != settled(y.State) {
			return settled(x.State)
		}
		return x.CreatedAt.Before(y.CreatedAt)
	})
	drop := map[int]bool{}
	for _, i := range idx[:over] {
		drop[i] = true
	}
	kept := make([]Suggestion, 0, MaxStored)
	for i, sg := range s.doc.Suggestions {
		if !drop[i] {
			kept = append(kept, sg)
		}
	}
	s.doc.Suggestions = kept
}

// List returns every unexpired suggestion, newest first, and the prefs.
func (s *Store) List() ([]Suggestion, Prefs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sweepLocked(s.now().UTC()) {
		if err := s.persistLocked(); err != nil {
			return nil, Prefs{}, err
		}
	}
	out := append([]Suggestion(nil), s.doc.Suggestions...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, s.prefsCopyLocked(), nil
}

func (s *Store) prefsCopyLocked() Prefs {
	p := Prefs{
		MutedSources: append([]string{}, s.doc.Prefs.MutedSources...),
		MutedKinds:   append([]string{}, s.doc.Prefs.MutedKinds...),
		Dismissals:   map[string]int{},
	}
	for k, v := range s.doc.Prefs.Dismissals {
		p.Dismissals[k] = v
	}
	return p
}

func (s *Store) findLocked(id string) (*Suggestion, error) {
	for i := range s.doc.Suggestions {
		if s.doc.Suggestions[i].ID == id {
			return &s.doc.Suggestions[i], nil
		}
	}
	return nil, ErrNotFound
}

// transition applies fn to an open or snoozed row and persists.
func (s *Store) transition(id string, fn func(sg *Suggestion, now time.Time) error) (Suggestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.sweepLocked(now)
	sg, err := s.findLocked(id)
	if err != nil {
		return Suggestion{}, err
	}
	if sg.State != StateOpen && sg.State != StateSnoozed {
		return Suggestion{}, ErrState
	}
	if err := fn(sg, now); err != nil {
		return Suggestion{}, err
	}
	sg.UpdatedAt = now
	if err := s.persistLocked(); err != nil {
		return Suggestion{}, err
	}
	return *sg, nil
}

// Accept marks a suggestion accepted. The caller starts the session.
func (s *Store) Accept(id string) (Suggestion, error) {
	return s.transition(id, func(sg *Suggestion, _ time.Time) error {
		sg.State = StateAccepted
		sg.SnoozeUntil = nil
		return nil
	})
}

// Dismiss records a dismissal (a preference signal for its source/kind).
// never also mutes the suggestion's kind.
func (s *Store) Dismiss(id string, never bool) (Suggestion, error) {
	return s.transition(id, func(sg *Suggestion, _ time.Time) error {
		sg.State = StateDismissed
		sg.SnoozeUntil = nil
		s.doc.Prefs.Dismissals[sg.Source+"/"+sg.Kind]++
		if never && !contains(s.doc.Prefs.MutedKinds, sg.Kind) && len(s.doc.Prefs.MutedKinds) < MaxMuted {
			s.doc.Prefs.MutedKinds = append(s.doc.Prefs.MutedKinds, sg.Kind)
			sort.Strings(s.doc.Prefs.MutedKinds)
		}
		return nil
	})
}

// Snooze hides a suggestion for d (1 minute to MaxSnooze).
func (s *Store) Snooze(id string, d time.Duration) (Suggestion, error) {
	if d < time.Minute || d > MaxSnooze {
		return Suggestion{}, fmt.Errorf("%w: snooze between 1 minute and 30 days", ErrInvalid)
	}
	return s.transition(id, func(sg *Suggestion, now time.Time) error {
		until := now.Add(d)
		sg.State = StateSnoozed
		sg.SnoozeUntil = &until
		return nil
	})
}

// SetMuted replaces the muted source and kind lists.
func (s *Store) SetMuted(sources, kinds []string) (Prefs, error) {
	norm := func(in []string) ([]string, error) {
		if len(in) > MaxMuted {
			return nil, fmt.Errorf("%w: at most %d muted entries", ErrInvalid, MaxMuted)
		}
		out := []string{}
		for _, v := range in {
			v = strings.TrimSpace(v)
			if !ValidToken(v) {
				return nil, fmt.Errorf("%w: muted entry %q", ErrInvalid, v)
			}
			if !contains(out, v) {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	src, err := norm(sources)
	if err != nil {
		return Prefs{}, err
	}
	kd, err := norm(kinds)
	if err != nil {
		return Prefs{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Prefs.MutedSources = src
	s.doc.Prefs.MutedKinds = kd
	if err := s.persistLocked(); err != nil {
		return Prefs{}, err
	}
	return s.prefsCopyLocked(), nil
}
