package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Injection provenance (#1376): which memory entries were auto-loaded into
// which session. The project and global stores share one directory, so both
// write one record per session there. Display-only: nothing reads it back
// into a prompt.

const injectedVersion = 1

// injectedMu serializes read-merge-write of per-session records across the
// project and global stores of one process.
var injectedMu sync.Mutex

// Injection is one memory entry as it was auto-loaded into a session.
type Injection struct {
	Scope      string    `json:"scope"`
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	Tags       []string  `json:"tags,omitempty"`
	InjectedAt time.Time `json:"injected_at"`
}

type injectedDoc struct {
	Version int         `json:"version"`
	Session string      `json:"session"`
	Entries []Injection `json:"entries"`
}

// AutoLoadSelection lists the store and returns the entries auto-load would
// inject, with the same caps as AutoLoadLayer.
func AutoLoadSelection(list Lister) (selected []Entry, omitted int, err error) {
	if list == nil {
		return nil, 0, nil
	}
	all, err := list.List("")
	if err != nil {
		return nil, 0, err
	}
	selected, omitted = SelectAutoLoad(all)
	return selected, omitted, nil
}

func (s *Store) injectedPath(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(filepath.Dir(s.path), "injected", hex.EncodeToString(digest[:])+".json")
}

// RecordInjected adds entries to sessionID's provenance record. An entry
// already recorded for the session (same scope and key) keeps its first
// injection time and text; the file is rewritten only when something is new.
func (s *Store) RecordInjected(sessionID string, entries []Entry) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(entries) == 0 {
		return nil
	}
	scope := s.Scope()
	path := s.injectedPath(sessionID)

	injectedMu.Lock()
	defer injectedMu.Unlock()
	doc, err := readInjected(path)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(doc.Entries))
	for _, e := range doc.Entries {
		seen[e.Scope+"\x00"+e.Key] = true
	}
	now := time.Now().UTC()
	added := false
	for _, e := range entries {
		id := scope + "\x00" + e.Key
		if seen[id] {
			continue
		}
		seen[id] = true
		added = true
		doc.Entries = append(doc.Entries, Injection{
			Scope:      scope,
			Key:        e.Key,
			Value:      e.Value,
			Tags:       append([]string(nil), e.Tags...),
			InjectedAt: now,
		})
	}
	if !added {
		return nil
	}
	doc.Version = injectedVersion
	doc.Session = sessionID
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memory injection record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create memory injection directory: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// Injected returns the entries recorded as auto-loaded into sessionID, in
// injection order (scope, then key, within one injection). Unknown sessions
// return an empty list.
func (s *Store) Injected(sessionID string) ([]Injection, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []Injection{}, nil
	}
	injectedMu.Lock()
	defer injectedMu.Unlock()
	doc, err := readInjected(s.injectedPath(sessionID))
	if err != nil {
		return nil, err
	}
	out := append([]Injection{}, doc.Entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].InjectedAt.Equal(out[j].InjectedAt) {
			return out[i].InjectedAt.Before(out[j].InjectedAt)
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope > out[j].Scope // project before global
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func readInjected(path string) (injectedDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return injectedDoc{}, nil
		}
		return injectedDoc{}, fmt.Errorf("read memory injection record: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return injectedDoc{}, nil
	}
	var doc injectedDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return injectedDoc{}, fmt.Errorf("decode memory injection record: %w", err)
	}
	if doc.Version != injectedVersion {
		return injectedDoc{}, fmt.Errorf("decode memory injection record: unsupported version %d", doc.Version)
	}
	return doc, nil
}
