package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestEngineSessionTempAllocatedAndCleaned(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	work := t.TempDir()
	const sid = "eng-temp-cleanup"
	eng := engine.New(engine.Options{
		SessionID:       sid,
		WorkDir:         work,
		InitialProvider: "echo",
		InitialModel:    "echo",
		Select: func(name string) (provider.Provider, string, error) {
			return echo.New(), "echo", nil
		},
		Registry: tool.NewRegistry(),
		Agents:   []engine.Agent{{Name: "build"}},
	})
	// Lazy: nothing on disk until first SessionTempDir/prompt/tool use.
	if _, err := os.Stat(filepath.Join(os.TempDir(), "strike", sid)); !os.IsNotExist(err) {
		t.Fatalf("temp dir created before first use: %v", err)
	}
	temp := eng.SessionTempDir()
	if temp == "" {
		t.Fatal("SessionTempDir empty after ensure")
	}
	if !strings.Contains(filepath.ToSlash(temp), "/strike/") {
		t.Fatalf("SessionTempDir = %q, want under …/strike/…", temp)
	}
	if st, err := os.Stat(temp); err != nil || !st.IsDir() {
		t.Fatalf("temp dir missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(temp, "mark"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(eng.Ops())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	for range eng.Events() {
	}

	deadline := time.After(2 * time.Second)
	for {
		_, err := os.Stat(temp)
		if os.IsNotExist(err) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("session temp still present after Run: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestEngineSessionTempInEnvironmentPrompt(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	work := t.TempDir()
	// captureSystemPrompt forces SessionID to "s-prompt".
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: work,
		Agents:  []engine.Agent{{Name: "build"}},
	}, "echo", "echo")
	t.Cleanup(func() { _ = tool.CleanupSessionTemp("s-prompt") })

	if !strings.Contains(sys, "Session temporary directory:") {
		t.Fatalf("environment missing session temp line:\n%s", sys)
	}
	if !strings.Contains(sys, filepath.Join("strike", "s-prompt")) && !strings.Contains(sys, "s-prompt") {
		t.Fatalf("environment missing session id path:\n%s", sys)
	}
	if !strings.Contains(sys, "session temporary directory") {
		t.Fatalf("environment missing temp usage note:\n%s", sys)
	}
}
