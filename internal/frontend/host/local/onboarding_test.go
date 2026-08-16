package local

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
)

func TestOnboardingCleanInstallAutoOpens(t *testing.T) {
	svc, _ := newTestServices(t)
	if svc.Onboarding == nil {
		t.Fatal("Onboarding not wired")
	}
	if !svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("clean install should auto-open")
	}
	// Still true until acknowledged (no file written yet).
	if !svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("second ShouldAutoOpen should still be true")
	}
	st, err := config.LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if st.Acknowledged {
		t.Fatal("ShouldAutoOpen must not acknowledge a clean install")
	}
}

func TestOnboardingPrecreatedDirsStillAutoOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	// Precreated home layout without sessions or credentials.
	for _, d := range []string{"sessions", "cache", "history"} {
		if err := os.MkdirAll(filepath.Join(home, ".strike", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, nil, nil, nil, nil, nil, "")
	if !svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("precreated dirs must not suppress first launch")
	}
}

func TestOnboardingMigrateCredentials(t *testing.T) {
	svc, store := newTestServices(t)
	if err := store.Set("anthropic", auth.Credential{Type: auth.TypeAPIKey, APIKey: "sk-test-key"}); err != nil {
		t.Fatal(err)
	}
	if svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("credentials should migrate to acknowledged")
	}
	st, err := config.LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Acknowledged || st.Version != config.OnboardingVersion {
		t.Fatalf("migrated state = %+v", st)
	}
}

func TestOnboardingMigrateSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	sessDir := filepath.Join(home, ".strike", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "old.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, nil, nil, nil, nil, nil, nil, "")
	if svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("existing sessions should migrate to acknowledged")
	}
	st, err := config.LoadOnboarding()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Acknowledged {
		t.Fatalf("expected acknowledged after session migrate: %+v", st)
	}
}

func TestOnboardingAcknowledgeStopsAutoOpen(t *testing.T) {
	svc, store := newTestServices(t)
	if !svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("want auto-open before ack")
	}
	if err := svc.Onboarding.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	if svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("auto-open after acknowledge")
	}
	// Fresh adapter on the same HOME still sees disk state.
	svc2 := New(store, nil, nil, nil, nil, nil, nil, "")
	if svc2.Onboarding.ShouldAutoOpen() {
		t.Fatal("persisted ack should suppress auto-open on new adapter")
	}
}

func TestOnboardingUnacknowledgedReopens(t *testing.T) {
	// Interrupted flow: wizard opened but never finished/dismissed — no ack file.
	svc, _ := newTestServices(t)
	if !svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("want auto-open")
	}
	// Simulate process exit without Acknowledge: new adapter, same home.
	home := os.Getenv("HOME")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	again := New(store, nil, nil, nil, nil, nil, nil, "")
	if !again.Onboarding.ShouldAutoOpen() {
		t.Fatal("unacknowledged should reopen on next launch")
	}
}

func TestOnboardingAcknowledgeConcurrent(t *testing.T) {
	svc, _ := newTestServices(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			errs <- svc.Onboarding.Acknowledge()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if svc.Onboarding.ShouldAutoOpen() {
		t.Fatal("want closed after concurrent ack")
	}
}

func TestOnboardingExplicitUnackedFileReopens(t *testing.T) {
	svc, _ := newTestServices(t)
	if err := config.SaveOnboarding(config.OnboardingState{Acknowledged: false}); err != nil {
		t.Fatal(err)
	}
	// New adapter reads disk.
	home := os.Getenv("HOME")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	fresh := New(store, nil, nil, nil, nil, nil, nil, "")
	if !fresh.Onboarding.ShouldAutoOpen() {
		t.Fatal("explicit unacked file should auto-open")
	}
	_ = svc
}
