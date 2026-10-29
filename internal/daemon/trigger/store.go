package trigger

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

const (
	fileVersion   = 1
	maxName       = 80
	maxPrompt     = 8000
	maxSecret     = 256
	minSecret     = 16
	maxLog        = 200
	defaultRate   = 6
	maxRate       = 120
	defaultDedupe = 600
	maxDedupe     = 86400
	// MaxBody is the inbound hook body cap.
	MaxBody = 256 << 10
)

const (
	KindWebhook = "webhook"
	KindGitHub  = "github"
	KindChannel = "channel"
)

const (
	OutcomeAccepted    = "accepted"
	OutcomeDuplicate   = "duplicate"
	OutcomeRejected    = "rejected"
	OutcomeRateLimited = "rate_limited"
	OutcomeDisabled    = "disabled"
	OutcomeIgnored     = "ignored"
	OutcomeError       = "error"
	OutcomeReplayed    = "replayed"
)

// View is the registry record returned to an authenticated client.
// It never includes the HMAC secret.
type View struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Kind                   string   `json:"kind"`
	Enabled                bool     `json:"enabled"`
	SecretSet              bool     `json:"secretSet"`
	Prompt                 string   `json:"prompt"`
	PermissionMode         string   `json:"permissionMode"`
	ResolvedPermissionMode string   `json:"resolvedPermissionMode"`
	ContinueSessionID      string   `json:"continueSessionId"`
	RatePerMinute          int      `json:"ratePerMinute"`
	DedupeSeconds          int      `json:"dedupeSeconds"`
	Events                 []string `json:"events"`
	Adapter                string   `json:"adapter"`
	Channel                string   `json:"channel"`
	HookPath               string   `json:"hookPath"`
}

// Input is a create or full-form update. An empty Secret keeps the stored
// secret. RotateSecret mints a new one and returns it once.
type Input struct {
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Secret            string   `json:"secret,omitempty"`
	RotateSecret      bool     `json:"rotateSecret,omitempty"`
	Prompt            string   `json:"prompt"`
	PermissionMode    string   `json:"permissionMode"`
	ContinueSessionID string   `json:"continueSessionId"`
	RatePerMinute     *int     `json:"ratePerMinute"`
	DedupeSeconds     *int     `json:"dedupeSeconds"`
	Events            []string `json:"events"`
	Adapter           string   `json:"adapter"`
	Channel           string   `json:"channel"`
	Enabled           *bool    `json:"enabled"`
}

// Delivery is one logged attempt. Prompt and Fingerprint stay on disk for
// replay and dedupe; list responses use DeliveryView.
type Delivery struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Outcome     string    `json:"outcome"`
	SessionID   string    `json:"sessionId,omitempty"`
	Event       string    `json:"event,omitempty"`
	Action      string    `json:"action,omitempty"`
	Repo        string    `json:"repo,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
}

// DeliveryView is a delivery log row without the stored prompt.
type DeliveryView struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Outcome   string    `json:"outcome"`
	SessionID string    `json:"sessionId,omitempty"`
	Event     string    `json:"event,omitempty"`
	Action    string    `json:"action,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type record struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Enabled           bool      `json:"enabled"`
	Secret            string    `json:"secret,omitempty"`
	Prompt            string    `json:"prompt,omitempty"`
	PermissionMode    string    `json:"permissionMode,omitempty"`
	ContinueSessionID string    `json:"continueSessionId,omitempty"`
	RatePerMinute     int       `json:"ratePerMinute"`
	DedupeSeconds     int       `json:"dedupeSeconds"`
	Events            []string  `json:"events,omitempty"`
	Adapter           string    `json:"adapter,omitempty"`
	Channel           string    `json:"channel,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type fileState struct {
	Version  int      `json:"version"`
	Triggers []record `json:"triggers"`
}

// Store is the 0600 trigger registry under dir.
type Store struct {
	mu   sync.Mutex
	dir  string
	path string
	st   fileState
	now  func() time.Time
}

// DefaultDir is ~/.strike.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".strike"
	}
	return filepath.Join(home, ".strike")
}

// Open loads dir/triggers.json. The file is created on the first write.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultDir()
	}
	s := &Store{
		dir:  dir,
		path: filepath.Join(dir, "triggers.json"),
		now:  time.Now,
		st:   fileState{Version: fileVersion, Triggers: []record{}},
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("trigger: parse %s: %w", s.path, err)
	}
	if s.st.Triggers == nil {
		s.st.Triggers = []record{}
	}
	return s, nil
}

func (s *Store) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(s.st.Triggers))
	for _, rec := range s.st.Triggers {
		out = append(out, rec.view())
	}
	return out
}

func (s *Store) Get(id string) (record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.find(id)
	if !ok {
		return record{}, errNotFound
	}
	return rec, nil
}

var errNotFound = errors.New("trigger not found")

// Create validates input, mints a secret when the kind needs one and none
// was supplied, and returns that secret once. Later reads omit it.
func (s *Store) Create(in Input) (View, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, secretOnce, err := s.normalize(record{}, in, true)
	if err != nil {
		return View{}, "", err
	}
	rec.ID = newID()
	rec.CreatedAt = s.now().UTC()
	rec.UpdatedAt = rec.CreatedAt
	s.st.Triggers = append(s.st.Triggers, rec)
	if err := s.saveLocked(); err != nil {
		s.st.Triggers = s.st.Triggers[:len(s.st.Triggers)-1]
		return View{}, "", err
	}
	return rec.view(), secretOnce, nil
}

// Update replaces the editable fields of id. An empty secret keeps the
// current one. The returned secret is non-empty only when it was just minted.
func (s *Store) Update(id string, in Input) (View, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, rec := range s.st.Triggers {
		if rec.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return View{}, "", errNotFound
	}
	prev := s.st.Triggers[idx]
	rec, secretOnce, err := s.normalize(prev, in, false)
	if err != nil {
		return View{}, "", err
	}
	rec.ID = prev.ID
	rec.CreatedAt = prev.CreatedAt
	rec.UpdatedAt = s.now().UTC()
	s.st.Triggers[idx] = rec
	if err := s.saveLocked(); err != nil {
		s.st.Triggers[idx] = prev
		return View{}, "", err
	}
	return rec.view(), secretOnce, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, rec := range s.st.Triggers {
		if rec.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errNotFound
	}
	s.st.Triggers = append(s.st.Triggers[:idx], s.st.Triggers[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		return err
	}
	_ = os.Remove(s.logPath(id))
	return nil
}

func (s *Store) AppendDelivery(id string, d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.find(id); !ok {
		return errNotFound
	}
	if err := validateID(id); err != nil {
		return err
	}
	path := s.logPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	existing, _ := s.readLog(path)
	existing = append(existing, d)
	return writeLog(path, trimLog(existing))
}

// trimLog keeps the newest maxLog rows, dropping non-sealing rows first so
// a run of signature failures cannot evict a verified delivery from the
// replay set.
func trimLog(rows []Delivery) []Delivery {
	if len(rows) <= maxLog {
		return rows
	}
	drop := len(rows) - maxLog
	kept := make([]Delivery, 0, maxLog)
	for _, row := range rows {
		if drop > 0 && !seals(row.Outcome) {
			drop--
			continue
		}
		kept = append(kept, row)
	}
	if len(kept) > maxLog {
		kept = kept[len(kept)-maxLog:]
	}
	return kept
}

func (s *Store) Deliveries(id string) []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.readLog(s.logPath(id))
	if err != nil {
		return nil
	}
	return rows
}

func (s *Store) find(id string) (record, bool) {
	for _, rec := range s.st.Triggers {
		if rec.ID == id {
			return rec, true
		}
	}
	return record{}, false
}

func (s *Store) normalize(prev record, in Input, create bool) (record, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxName {
		return record{}, "", fmt.Errorf("name is required (max %d)", maxName)
	}
	kind := strings.TrimSpace(in.Kind)
	switch kind {
	case KindWebhook, KindGitHub, KindChannel:
	default:
		return record{}, "", fmt.Errorf("kind must be webhook, github, or channel")
	}
	if _, err := rootstart.ResolveMode(in.PermissionMode); err != nil {
		return record{}, "", err
	}
	prompt := strings.TrimSpace(in.Prompt)
	if len(prompt) > maxPrompt {
		return record{}, "", fmt.Errorf("prompt is too long")
	}
	rate := defaultRate
	if in.RatePerMinute != nil {
		rate = *in.RatePerMinute
	} else if !create && prev.RatePerMinute > 0 {
		rate = prev.RatePerMinute
	}
	if rate < 1 || rate > maxRate {
		return record{}, "", fmt.Errorf("ratePerMinute must be 1..%d", maxRate)
	}
	dedupe := defaultDedupe
	if in.DedupeSeconds != nil {
		dedupe = *in.DedupeSeconds
	} else if !create {
		dedupe = prev.DedupeSeconds
	}
	if dedupe < 0 || dedupe > maxDedupe {
		return record{}, "", fmt.Errorf("dedupeSeconds must be 0..%d", maxDedupe)
	}
	cont := strings.TrimSpace(in.ContinueSessionID)
	if err := validateSessionRef(cont); err != nil {
		return record{}, "", err
	}
	adapter := strings.TrimSpace(in.Adapter)
	channel := strings.TrimSpace(in.Channel)
	if kind == KindChannel {
		if adapter == "" || len(adapter) > 32 || strings.ContainsAny(adapter, " /\\") {
			return record{}, "", fmt.Errorf("channel triggers need an adapter name")
		}
	}
	events := compactEvents(in.Events)
	if kind == KindGitHub {
		for _, ev := range events {
			if !knownGitHubEvent(ev) {
				return record{}, "", fmt.Errorf("unsupported github event %q", ev)
			}
		}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	} else if !create {
		enabled = prev.Enabled
	}
	secret := prev.Secret
	var once string
	if kind == KindChannel {
		secret = ""
	} else if strings.TrimSpace(in.Secret) != "" {
		secret = strings.TrimSpace(in.Secret)
		if len(secret) < minSecret || len(secret) > maxSecret {
			return record{}, "", fmt.Errorf("secret must be %d..%d characters", minSecret, maxSecret)
		}
	} else if in.RotateSecret || secret == "" {
		minted, err := mintSecret()
		if err != nil {
			return record{}, "", err
		}
		secret = minted
		once = minted
	}
	if kind != KindChannel && secret == "" {
		return record{}, "", fmt.Errorf("webhook and github triggers need a secret")
	}
	rec := record{
		Name:              name,
		Kind:              kind,
		Enabled:           enabled,
		Secret:            secret,
		Prompt:            prompt,
		PermissionMode:    strings.TrimSpace(in.PermissionMode),
		ContinueSessionID: cont,
		RatePerMinute:     rate,
		DedupeSeconds:     dedupe,
		Events:            events,
		Adapter:           adapter,
		Channel:           channel,
	}
	return rec, once, nil
}

func (rec record) view() View {
	mode, _ := rootstart.ResolveMode(rec.PermissionMode)
	events := rec.Events
	if events == nil {
		events = []string{}
	}
	return View{
		ID:                     rec.ID,
		Name:                   rec.Name,
		Kind:                   rec.Kind,
		Enabled:                rec.Enabled,
		SecretSet:              rec.Secret != "",
		Prompt:                 rec.Prompt,
		PermissionMode:         rec.PermissionMode,
		ResolvedPermissionMode: string(mode),
		ContinueSessionID:      rec.ContinueSessionID,
		RatePerMinute:          rec.RatePerMinute,
		DedupeSeconds:          rec.DedupeSeconds,
		Events:                 events,
		Adapter:                rec.Adapter,
		Channel:                rec.Channel,
		HookPath:               "/hooks/" + rec.ID,
	}
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	s.st.Version = fileVersion
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.dir, ".triggers-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (s *Store) logPath(id string) string {
	return filepath.Join(s.dir, "triggers", id+".jsonl")
}

func (s *Store) readLog(path string) ([]Delivery, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Delivery
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var d Delivery
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func writeLog(path string, rows []Delivery) error {
	var b strings.Builder
	for _, row := range rows {
		raw, err := json.Marshal(row)
		if err != nil {
			return err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deliveries-*.jsonl")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func (d Delivery) view() DeliveryView {
	return DeliveryView{
		ID:        d.ID,
		At:        d.At,
		Outcome:   d.Outcome,
		SessionID: d.SessionID,
		Event:     d.Event,
		Action:    d.Action,
		Repo:      d.Repo,
		Reason:    d.Reason,
	}
}

func newID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000")))
	}
	return hex.EncodeToString(buf[:])
}

func mintSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func validateID(id string) error {
	if len(id) < 8 || len(id) > 64 {
		return fmt.Errorf("invalid trigger id")
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("invalid trigger id")
		}
	}
	return nil
}

func validateSessionRef(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 128 || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid continue session id")
	}
	return nil
}

func compactEvents(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, ev := range in {
		ev = strings.TrimSpace(ev)
		if ev == "" || seen[ev] {
			continue
		}
		seen[ev] = true
		out = append(out, ev)
	}
	return out
}

func knownGitHubEvent(ev string) bool {
	switch ev {
	case "issues", "pull_request", "issue_comment", "pull_request_review_comment", "ping":
		return true
	default:
		return false
	}
}
