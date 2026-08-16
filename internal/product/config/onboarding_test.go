package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOnboardingPathUnderStrikeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".strike", "onboarding.json")
	if got := OnboardingPath(); got != want {
		t.Fatalf("OnboardingPath = %q, want %q", got, want)
	}
}

func TestLoadOnboardingMissingIsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != 0 || st.Acknowledged {
		t.Fatalf("missing file state = %+v, want zero", st)
	}
}

func TestAcknowledgeOnboardingRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AcknowledgeOnboarding(); err != nil {
		t.Fatal(err)
	}
	st, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != OnboardingVersion || !st.Acknowledged {
		t.Fatalf("after ack = %+v", st)
	}
	// Idempotent.
	if err := AcknowledgeOnboarding(); err != nil {
		t.Fatal(err)
	}
	st2, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if st2 != st {
		t.Fatalf("second ack changed state: %+v → %+v", st, st2)
	}
}

func TestSaveOnboardingAtomic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SaveOnboarding(OnboardingState{Acknowledged: true}); err != nil {
		t.Fatal(err)
	}
	path := OnboardingPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st OnboardingState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != OnboardingVersion || !st.Acknowledged {
		t.Fatalf("file = %+v", st)
	}
	// No leftover temp files.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".onboarding-") {
			t.Fatalf("leftover temp %q", e.Name())
		}
	}
}

func TestAcknowledgeOnboardingConcurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			errs <- AcknowledgeOnboarding()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Acknowledged || st.Version != OnboardingVersion {
		t.Fatalf("concurrent ack state = %+v", st)
	}
}

func TestAcknowledgeOnboardingRepairsCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := OnboardingPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeOnboarding(); err != nil {
		t.Fatal(err)
	}
	st, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Acknowledged || st.Version != OnboardingVersion {
		t.Fatalf("repaired = %+v", st)
	}
}

func TestHasDurableSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if HasDurableSessions() {
		t.Fatal("empty home should have no sessions")
	}
	// Precreated sessions dir alone does not count.
	sessDir := filepath.Join(home, ".strike", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasDurableSessions() {
		t.Fatal("empty sessions dir should not count")
	}
	if err := os.WriteFile(filepath.Join(sessDir, "2026.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasDurableSessions() {
		t.Fatal("jsonl session should count")
	}
}

func TestPrecreatedStrikeDirDoesNotImplyOnboarding(t *testing.T) {
	// Only directories under ~/.strike — no onboarding file, no sessions.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".strike", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".strike", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if st.Acknowledged {
		t.Fatal("precreated dirs must not acknowledge onboarding")
	}
	if HasDurableSessions() {
		t.Fatal("empty sessions dir must not count as durable")
	}
}
