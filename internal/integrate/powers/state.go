package powers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// State is the Powers state that survives a restart (#1943): which packs
// have auto-suggestion muted, and when each pack was last activated.
type State struct {
	Muted    []string             `json:"muted,omitempty"`
	LastUsed map[string]time.Time `json:"lastUsed,omitempty"`
}

// LoadState reads path. A missing file is an empty state, not an error.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("powers: state: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("powers: state %s: %w", path, err)
	}
	return st, nil
}

// SaveState writes st to path atomically (temp file in the same directory,
// then rename), creating the directory when needed. The file is 0600.
func SaveState(path string, st State) error {
	st.Muted = append([]string(nil), st.Muted...)
	sort.Strings(st.Muted)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("powers: state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("powers: state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".powers-state-*")
	if err != nil {
		return fmt.Errorf("powers: state: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("powers: state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("powers: state: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("powers: state: %w", err)
	}
	return nil
}
