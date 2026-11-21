package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike/pkg/redact"
)

// TagLesson marks a memory entry captured as a lesson (#1376). Approved
// proposals carry it, and it auto-loads like the other tags in AutoLoadTags.
const TagLesson = "lesson"

const (
	proposalsVersion = 1
	maxProposals     = 200
)

// ErrProposalNotFound is returned when a proposal id is absent.
var ErrProposalNotFound = errors.New("memory: proposal not found")

// Proposal is a lesson a session suggested. It waits, pending, until the
// operator approves it into a memory entry or dismisses it. A pending
// proposal is never auto-loaded into a prompt.
type Proposal struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Scope     string    `json:"scope"`
	Session   string    `json:"session,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type proposalsDoc struct {
	Version   int        `json:"version"`
	Proposals []Proposal `json:"proposals"`
}

// ProposalsPath is the pending-proposal file beside the store.
func (s *Store) ProposalsPath() string {
	return strings.TrimSuffix(s.path, ".json") + ".proposals.json"
}

// Propose records a pending lesson. Key, value, and session pass through
// pkg/redact first, like Put. Scope is project (default) or global.
func (s *Store) Propose(p Proposal) (Proposal, error) {
	clean, err := scrubProposal(p)
	if err != nil {
		return Proposal{}, err
	}
	id, err := newProposalID()
	if err != nil {
		return Proposal{}, err
	}
	clean.ID = id
	clean.CreatedAt = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Proposal{}, errClosed
	}
	all, err := s.loadProposalsLocked()
	if err != nil {
		return Proposal{}, err
	}
	if len(all) >= maxProposals {
		return Proposal{}, fmt.Errorf("memory: at most %d pending proposals", maxProposals)
	}
	all = append(all, clean)
	if err := s.saveProposalsLocked(all); err != nil {
		return Proposal{}, err
	}
	return clean, nil
}

// Proposals lists pending proposals, oldest first.
func (s *Store) Proposals() ([]Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	return s.loadProposalsLocked()
}

// EditProposal replaces a pending proposal's key, value, and scope. The id,
// session, and creation time are kept.
func (s *Store) EditProposal(id, key, value, scope string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Proposal{}, errClosed
	}
	all, err := s.loadProposalsLocked()
	if err != nil {
		return Proposal{}, err
	}
	for i, p := range all {
		if p.ID != id {
			continue
		}
		next := p
		next.Key, next.Value, next.Scope = key, value, scope
		clean, err := scrubProposal(next)
		if err != nil {
			return Proposal{}, err
		}
		all[i] = clean
		if err := s.saveProposalsLocked(all); err != nil {
			return Proposal{}, err
		}
		return clean, nil
	}
	return Proposal{}, ErrProposalNotFound
}

// TakeProposal removes a pending proposal and returns it. Approve and
// dismiss both start here; a failed approve restores it with RestoreProposal.
func (s *Store) TakeProposal(id string) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Proposal{}, errClosed
	}
	all, err := s.loadProposalsLocked()
	if err != nil {
		return Proposal{}, err
	}
	for i, p := range all {
		if p.ID == id {
			rest := append(append([]Proposal(nil), all[:i]...), all[i+1:]...)
			if err := s.saveProposalsLocked(rest); err != nil {
				return Proposal{}, err
			}
			return p, nil
		}
	}
	return Proposal{}, ErrProposalNotFound
}

// RestoreProposal puts a taken proposal back unchanged.
func (s *Store) RestoreProposal(p Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	all, err := s.loadProposalsLocked()
	if err != nil {
		return err
	}
	all = append(all, p)
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	return s.saveProposalsLocked(all)
}

func (s *Store) loadProposalsLocked() ([]Proposal, error) {
	data, err := os.ReadFile(s.ProposalsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Proposal{}, nil
		}
		return nil, fmt.Errorf("read memory proposals: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return []Proposal{}, nil
	}
	var doc proposalsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode memory proposals: %w", err)
	}
	if doc.Version != proposalsVersion {
		return nil, fmt.Errorf("decode memory proposals: unsupported version %d", doc.Version)
	}
	out := make([]Proposal, 0, len(doc.Proposals))
	out = append(out, doc.Proposals...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) saveProposalsLocked(all []Proposal) error {
	if all == nil {
		all = []Proposal{}
	}
	data, err := json.MarshalIndent(proposalsDoc{Version: proposalsVersion, Proposals: all}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memory proposals: %w", err)
	}
	return writeFileAtomic(s.ProposalsPath(), append(data, '\n'))
}

// NormalizeScope accepts empty as project.
func NormalizeScope(scope string) (string, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		return "project", nil
	case "global":
		return "global", nil
	default:
		return "", errors.New("memory: scope must be project or global")
	}
}

func scrubProposal(p Proposal) (Proposal, error) {
	p.Key = redact.String(strings.TrimSpace(p.Key))
	p.Value = redact.String(strings.TrimSpace(p.Value))
	p.Session = redact.String(strings.TrimSpace(p.Session))
	if err := validateKey(p.Key); err != nil {
		return Proposal{}, err
	}
	if p.Value == "" {
		return Proposal{}, errors.New("memory: proposal text is required")
	}
	if len(p.Value) > maxValueLen {
		return Proposal{}, errValueTooLong
	}
	scope, err := NormalizeScope(p.Scope)
	if err != nil {
		return Proposal{}, err
	}
	p.Scope = scope
	return p, nil
}

func newProposalID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("memory: proposal id: %w", err)
	}
	return "prop-" + hex.EncodeToString(b[:]), nil
}
