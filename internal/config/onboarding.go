package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// OnboardingVersion is the schema version written to onboarding.json.
const OnboardingVersion = 1

// OnboardingState is versioned global first-time setup acknowledgement.
// It is installation-scoped (under ~/.strike), never per-project.
type OnboardingState struct {
	Version      int  `json:"version"`
	Acknowledged bool `json:"acknowledged"`
}

// onboardingMu serializes in-process read-modify-write on onboarding.json
// alongside the cross-process flock in lockGlobalFile.
var onboardingMu sync.Mutex

// OnboardingPath is ~/.strike/onboarding.json.
func OnboardingPath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "onboarding.json")
}

// LoadOnboarding reads global onboarding state. A missing file yields a zero
// value and a nil error (caller decides migration / first-run).
func LoadOnboarding() (OnboardingState, error) {
	path := OnboardingPath()
	if path == "" {
		return OnboardingState{}, fmt.Errorf("cannot locate home directory")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return OnboardingState{}, nil
	case err != nil:
		return OnboardingState{}, err
	case len(strings.TrimSpace(string(data))) == 0:
		return OnboardingState{}, nil
	default:
		var st OnboardingState
		if err := json.Unmarshal(data, &st); err != nil {
			return OnboardingState{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return st, nil
	}
}

// SaveOnboarding writes onboarding state atomically under lock. Version is
// always set to OnboardingVersion.
func SaveOnboarding(st OnboardingState) error {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()
	return saveOnboardingLocked(st)
}

// AcknowledgeOnboarding marks global onboarding complete. Idempotent: a
// second call is a no-op when already acknowledged.
func AcknowledgeOnboarding() error {
	onboardingMu.Lock()
	defer onboardingMu.Unlock()

	st, err := loadOnboardingUnlocked()
	if err != nil {
		// Corrupt or unreadable: overwrite with a clean acknowledged state so
		// users are not stuck reopening the wizard forever.
		if !errors.Is(err, fs.ErrNotExist) && !isOnboardingParseError(err) {
			return err
		}
		st = OnboardingState{}
	}
	if st.Acknowledged && st.Version == OnboardingVersion {
		return nil
	}
	st.Version = OnboardingVersion
	st.Acknowledged = true
	return saveOnboardingLocked(st)
}

func isOnboardingParseError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "parse ")
}

// HasDurableSessions reports whether ~/.strike/sessions contains any session
// log files. Used to migrate established installs without a surprise wizard.
// An empty or missing sessions directory does not count.
func HasDurableSessions() bool {
	root := GlobalRoot()
	if root == "" {
		return false
	}
	dir := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Session logs are *.jsonl; ignore lock/temp sidecars.
		if strings.HasSuffix(name, ".jsonl") {
			return true
		}
	}
	return false
}

func loadOnboardingUnlocked() (OnboardingState, error) {
	path := OnboardingPath()
	if path == "" {
		return OnboardingState{}, fmt.Errorf("cannot locate home directory")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return OnboardingState{}, fs.ErrNotExist
	case err != nil:
		return OnboardingState{}, err
	case len(strings.TrimSpace(string(data))) == 0:
		return OnboardingState{}, nil
	default:
		var st OnboardingState
		if err := json.Unmarshal(data, &st); err != nil {
			return OnboardingState{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return st, nil
	}
}

func saveOnboardingLocked(st OnboardingState) error {
	path := OnboardingPath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}
	unlock, err := lockGlobalFile(path)
	if err != nil {
		return err
	}
	defer unlock()

	path, err = resolveWritePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	st.Version = OnboardingVersion
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	payload := append(out, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".onboarding-")
	if err != nil {
		return fmt.Errorf("create temp onboarding: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp onboarding: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp onboarding: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp onboarding: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp onboarding: %w", err)
	}
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}
	return nil
}
