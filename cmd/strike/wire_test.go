package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/provider/echo"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/internal/project"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// eventEqual compares protocol events; TurnCompleted gained a slice field and
// is no longer comparable with ==.
func eventEqual(a, b protocol.Event) bool {
	return reflect.DeepEqual(a, b)
}

func TestRunSessionClosesStoreAfterEngineCleanupAndFinalAppend(t *testing.T) {
	engineEvents := make(chan protocol.Event)
	canceled := make(chan struct{})
	finishCleanup := make(chan struct{})
	engineReturned := make(chan struct{})
	appendStarted := make(chan struct{})
	finishAppend := make(chan struct{})
	appendCompleted := make(chan struct{})
	closeCalled := make(chan struct{})
	terminal := protocol.TurnCompleted{StopReason: "canceled"}

	var mu sync.Mutex
	var appended []protocol.Event
	var closeBeforeEngineReturn, closeBeforeAppendCompletion bool
	store := &fakeSessionStore{
		append: func(event protocol.Event) error {
			close(appendStarted)
			<-finishAppend
			mu.Lock()
			appended = append(appended, event)
			mu.Unlock()
			close(appendCompleted)
			return nil
		},
		close: func() error {
			select {
			case <-engineReturned:
			default:
				closeBeforeEngineReturn = true
			}
			select {
			case <-appendCompleted:
			default:
				closeBeforeAppendCompletion = true
			}
			close(closeCalled)
			return nil
		},
	}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		<-finishCleanup
		engineEvents <- terminal
		close(engineEvents)
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return nil
		})
	}()

	waitSignal(t, canceled, "engine cancellation")
	close(finishCleanup)
	waitSignal(t, appendStarted, "terminal event append start")
	waitSignal(t, engineReturned, "engine return")
	close(finishAppend)

	if err := waitResult(t, done, "runSession return"); err != nil {
		t.Fatalf("runSession() error = %v, want nil", err)
	}
	waitSignal(t, closeCalled, "store close")
	if closeBeforeEngineReturn {
		t.Error("store.Close occurred before the engine completed cleanup and returned")
	}
	if closeBeforeAppendCompletion {
		t.Error("store.Close occurred before the terminal event Append completed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(appended) != 1 || !eventEqual(appended[0], terminal) {
		t.Errorf("appended events = %#v, want terminal event %#v", appended, terminal)
	}
}

func TestRunSessionDrainsEventsAfterFrontendAbandonsForwardedStream(t *testing.T) {
	const eventCount = 4096
	engineEvents := make(chan protocol.Event)
	canceled := make(chan struct{})
	store := &fakeSessionStore{}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		for i := 0; i < eventCount; i++ {
			engineEvents <- protocol.TextDelta{Text: "event"}
		}
		close(engineEvents)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return nil
		})
	}()

	waitSignal(t, canceled, "engine cancellation after frontend return")
	if err := waitResult(t, done, "runSession draining abandoned frontend events"); err != nil {
		t.Fatalf("runSession() error = %v, want nil", err)
	}
	if got := store.appendCount(); got != eventCount {
		t.Errorf("Append call count = %d, want all %d events drained", got, eventCount)
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

func TestRunSessionAppendErrorCancelsEngineAndStopsAppend(t *testing.T) {
	firstAppendErr := errors.New("first append failure")
	engineEvents := make(chan protocol.Event)
	engineReturned := make(chan struct{})
	engineSawCancel := make(chan struct{})

	var closeBeforeJoin bool
	store := &fakeSessionStore{}
	store.append = func(protocol.Event) error {
		return firstAppendErr
	}
	store.close = func() error {
		select {
		case <-engineReturned:
		default:
			closeBeforeJoin = true
		}
		return nil
	}
	engineRun := func(ctx context.Context) {
		// Emit events while running; first Append fails and cancels ctx.
		engineEvents <- protocol.TextDelta{Text: "one"}
		engineEvents <- protocol.TextDelta{Text: "two"}
		engineEvents <- protocol.TurnCompleted{StopReason: "end_turn"}
		close(engineEvents)
		select {
		case <-ctx.Done():
			close(engineSawCancel)
		case <-time.After(2 * time.Second):
		}
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(ch <-chan protocol.Event) error {
			// Block until channel closes so we don't cancel via frontend first.
			for range ch {
			}
			return nil
		})
	}()
	err := waitResult(t, done, "runSession after append errors")
	if !errors.Is(err, firstAppendErr) {
		t.Errorf("runSession() error = %v, want first append error", err)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "append") {
		t.Errorf("runSession() error = %v, want append context", err)
	}
	// Fail-safe: stop appending after the first failure (remaining events drain).
	if got := store.appendCount(); got != 1 {
		t.Errorf("Append call count = %d, want 1 (stop after fatal persistence)", got)
	}
	select {
	case <-engineSawCancel:
	case <-time.After(2 * time.Second):
		t.Error("engine was not canceled after append failure")
	}
	if closeBeforeJoin {
		t.Error("store.Close occurred before engine return and tee drain completion")
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

func TestRunSessionDoesNotForwardUnpersistedTerminal(t *testing.T) {
	appendErr := errors.New("disk full")
	engineEvents := make(chan protocol.Event, 2)
	store := &fakeSessionStore{}
	store.append = func(protocol.Event) error { return appendErr }

	engineRun := func(ctx context.Context) {
		engineEvents <- protocol.TurnCompleted{StopReason: "end_turn"}
		close(engineEvents)
	}

	var forwarded []protocol.Event
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(ch <-chan protocol.Event) error {
			for ev := range ch {
				forwarded = append(forwarded, ev)
			}
			return nil
		})
	}()
	if err := waitResult(t, done, "runSession unpersisted terminal"); !errors.Is(err, appendErr) {
		t.Fatalf("err = %v, want appendErr", err)
	}
	if len(forwarded) != 0 {
		t.Fatalf("forwarded = %#v, want none (terminal must not appear completed)", forwarded)
	}
}

func TestRunSessionSurfacesFrontendAndCloseErrorsAfterShutdown(t *testing.T) {
	frontendErr := errors.New("frontend failure")
	closeErr := errors.New("close failure")
	engineEvents := make(chan protocol.Event)
	engineReturned := make(chan struct{})
	store := &fakeSessionStore{close: func() error { return closeErr }}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(engineEvents)
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return frontendErr
		})
	}()
	err := waitResult(t, done, "runSession after frontend error")
	if !errors.Is(err, frontendErr) {
		t.Errorf("runSession() error = %v, want joined frontend error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Errorf("runSession() error = %v, want joined close error", err)
	}
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		if !strings.Contains(lowerErr, "frontend") {
			t.Errorf("runSession() error = %v, want frontend context", err)
		}
		if !strings.Contains(lowerErr, "clos") {
			t.Errorf("runSession() error = %v, want close context", err)
		}
	}
	select {
	case <-engineReturned:
	default:
		t.Error("runSession returned before the engine")
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

type fakeSessionStore struct {
	mu       sync.Mutex
	appended []protocol.Event
	closes   int
	append   func(protocol.Event) error
	close    func() error
}

func (s *fakeSessionStore) Append(event protocol.Event) error {
	s.mu.Lock()
	s.appended = append(s.appended, event)
	s.mu.Unlock()
	if s.append != nil {
		return s.append(event)
	}
	return nil
}

func (s *fakeSessionStore) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	if s.close != nil {
		return s.close()
	}
	return nil
}

func (s *fakeSessionStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appended)
}

func (s *fakeSessionStore) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitResult(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func TestRunSessionQuietStartupDoesNotReAppendSelections(t *testing.T) {
	// Mirrors --continue: real engine QuietStartup + runSession tee must not
	// re-append ModelSelected/AgentSelected/AutonomySelected already in the log.
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	info, err := mgr.Create(session.CreateOptions{ID: "resume-quiet"})
	if err != nil {
		t.Fatal(err)
	}
	corr := protocol.Correlation{SessionID: info.ID}
	prior := []protocol.Event{
		protocol.ModelSelected{Correlation: corr, Provider: "echo", Model: "echo"},
		protocol.AutonomySelected{Correlation: corr, Mode: protocol.AutonomySupervised},
		protocol.AgentSelected{Correlation: corr, Name: "build"},
		protocol.UserMessage{Correlation: corr, Text: "prior"},
	}
	for _, ev := range prior {
		if err := mgr.Append(info.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	_ = mgr.Close(info.ID)

	opened, err := openResumeSession(mgr, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// runSession owns bound.Close.

	restored := engine.Restore(opened.replay)
	eng := engine.New(engine.Options{
		SessionID:             info.ID,
		Select:                func(string) (provider.Provider, string, error) { return echo.New(), "echo", nil },
		Registry:              tool.NewRegistry(),
		WorkDir:               t.TempDir(),
		InitialProvider:       "echo",
		InitialModel:          "echo",
		InitialAgent:          restored.Agent,
		InitialAutonomy:       restored.Autonomy,
		InitialPermissionMode: restored.PermissionMode,
		InitialMessages:       restored.Messages,
		QuietStartup:          true,
		Agents:                []engine.Agent{{Name: "build"}},
	})

	err = runSession(context.Background(), eng.Run, eng.Events(), opened.bound, func(live <-chan protocol.Event) error {
		// Settle long enough for quiet startup to finish, then exit (like TUI quit).
		deadline := time.After(100 * time.Millisecond)
		for {
			select {
			case ev, ok := <-live:
				if !ok {
					return nil
				}
				switch ev.(type) {
				case protocol.ModelSelected, protocol.AgentSelected, protocol.AutonomySelected,
					protocol.PermissionModeSelected, protocol.EffortSelected, protocol.PhaseChanged:
					t.Errorf("live stream saw startup selection %T", ev)
				}
			case <-deadline:
				return nil
			}
		}
	})
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}

	replay, err := mgr.Replay(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != len(prior) {
		t.Fatalf("replay len = %d, want %d (selections re-appended)", len(replay), len(prior))
	}
	var modelN, agentN, autonomyN int
	for _, ev := range replay {
		switch ev.(type) {
		case protocol.ModelSelected:
			modelN++
		case protocol.AgentSelected:
			agentN++
		case protocol.AutonomySelected:
			autonomyN++
		}
	}
	if modelN != 1 || agentN != 1 || autonomyN != 1 {
		t.Fatalf("selection counts model=%d agent=%d autonomy=%d, want 1 each", modelN, agentN, autonomyN)
	}
}

func TestOpenResumeSession(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-1", Title: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(root.ID, protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: root.ID},
		Text:        "hi there",
	}); err != nil {
		t.Fatal(err)
	}
	_ = mgr.Close(root.ID)

	opened, err := openResumeSession(mgr, root.ID)
	if err != nil {
		t.Fatalf("openResumeSession: %v", err)
	}
	if opened.id != root.ID {
		t.Errorf("id = %q", opened.id)
	}
	if len(opened.replay) != 1 {
		t.Fatalf("replay len = %d", len(opened.replay))
	}
	_ = opened.bound.Close()

	child, err := mgr.Create(session.CreateOptions{
		ID: "child-1", ParentSessionID: root.ID, Title: "sub",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = mgr.Close(child.ID)
	if _, err := openResumeSession(mgr, child.ID); err == nil {
		t.Fatal("expected error resuming child session")
	}
	if _, err := openResumeSession(mgr, "missing"); err == nil {
		t.Fatal("expected error for missing session")
	}
}

// TestOpenResumeSessionRestoresPhaseAutonomyGrants is the --continue /
// --session contract: openResumeSession loads the JSONL log and engine.Restore
// recovers workflow phase, autonomy mode, and DecisionAlways grants that wire
// feeds into engine.Options.
func TestOpenResumeSessionRestoresPhaseAutonomyGrants(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-resume"})
	if err != nil {
		t.Fatal(err)
	}
	corr := protocol.Correlation{SessionID: root.ID}
	for _, ev := range []protocol.Event{
		protocol.ModelSelected{Correlation: corr, Provider: "echo", Model: "echo"},
		protocol.AgentSelected{Correlation: corr, Name: "build"},
		protocol.AutonomySelected{Correlation: corr, Mode: protocol.AutonomyAgent},
		protocol.PhaseChanged{
			Correlation: corr,
			Workflow:    "plan-implement",
			Phase:       "implement",
			Index:       1,
			Gate:        "agent",
		},
		protocol.UserMessage{Correlation: corr, Text: "keep going"},
		protocol.PermissionAsked{
			Correlation: corr,
			RequestID:   root.ID + ":perm_1",
			Permission:  "bash",
			Patterns:    []string{"git status"},
			Always:      []string{"git *"},
		},
		protocol.PermissionResolved{
			Correlation: corr,
			RequestID:   root.ID + ":perm_1",
			Decision:    protocol.DecisionAlways,
		},
		// Incomplete turn at end of log must not block restore of selections.
		protocol.TurnStarted{Correlation: corr},
		protocol.PermissionAsked{
			Correlation: corr,
			RequestID:   root.ID + ":perm_open",
			Permission:  "edit",
			Patterns:    []string{"foo.go"},
		},
	} {
		if err := mgr.Append(root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	_ = mgr.Close(root.ID)

	opened, err := openResumeSession(mgr, root.ID)
	if err != nil {
		t.Fatalf("openResumeSession: %v", err)
	}
	defer opened.bound.Close()

	// Mirror wire.go resume path: Restore(replay) → engine.Options seeds.
	restored := engine.Restore(opened.replay)
	if restored.Autonomy != protocol.AutonomyAgent {
		t.Fatalf("Autonomy = %q, want agent", restored.Autonomy)
	}
	if restored.PhaseWorkflow != "plan-implement" || restored.PhaseName != "implement" || restored.PhaseIndex != 1 {
		t.Fatalf("phase = %q/%q/%d", restored.PhaseWorkflow, restored.PhaseName, restored.PhaseIndex)
	}
	if restored.Provider != "echo" || restored.Model != "echo" || restored.Agent != "build" {
		t.Fatalf("selections = %+v", restored)
	}
	if len(restored.Messages) != 1 || restored.Messages[0].Text != "keep going" {
		t.Fatalf("Messages = %#v", restored.Messages)
	}
	if len(restored.AlwaysGrants) != 1 {
		t.Fatalf("AlwaysGrants = %#v, want one bash grant", restored.AlwaysGrants)
	}
	g := restored.AlwaysGrants[0]
	if g.Permission != "bash" || g.Pattern != "git *" || g.Action != permission.Allow {
		t.Fatalf("grant = %#v", g)
	}
	// Unresolved PermissionAsked must not become a grant.
	for _, g := range restored.AlwaysGrants {
		if g.Permission == "edit" {
			t.Fatalf("open PermissionAsked leaked into AlwaysGrants: %#v", restored.AlwaysGrants)
		}
	}
}

func TestBuildCustomProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("kimi", auth.Credential{Type: auth.TypeAPIKey, APIKey: "sk-kimi"}); err != nil {
		t.Fatal(err)
	}

	t.Run("openai", func(t *testing.T) {
		p, model, err := buildCustomProvider(config.CustomProvider{
			Name:    "kimi",
			BaseURL: "https://api.moonshot.cn/v1",
			API:     config.WireOpenAI,
			Models:  []string{"moonshot-v1", "moonshot-v2"},
			Headers: map[string]string{"X-Custom": "1"},
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("provider is nil")
		}
		if p.Name() != "kimi" {
			t.Fatalf("Name = %q", p.Name())
		}
		if model != "moonshot-v1" {
			t.Errorf("model = %q, want moonshot-v1", model)
		}
	})

	t.Run("openai empty models", func(t *testing.T) {
		p, model, err := buildCustomProvider(config.CustomProvider{
			Name:    "ollama",
			BaseURL: "http://localhost:11434/v1",
			API:     config.WireOpenAI,
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.Name() != "ollama" {
			t.Fatalf("provider = %v", p)
		}
		if model != "" {
			t.Errorf("model = %q, want empty", model)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		p, model, err := buildCustomProvider(config.CustomProvider{
			Name:    "proxy",
			BaseURL: "https://proxy.example",
			API:     config.WireAnthropic,
			Models:  []string{"claude-custom"},
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.Name() != "proxy" {
			t.Fatalf("provider = %v", p)
		}
		if model != "claude-custom" {
			t.Errorf("model = %q", model)
		}
	})

	t.Run("unknown api", func(t *testing.T) {
		_, _, err := buildCustomProvider(config.CustomProvider{
			Name:    "bad",
			BaseURL: "https://x.example",
			API:     "gemini",
		}, store)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "unknown api") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("anthropic missing name", func(t *testing.T) {
		_, _, err := buildCustomProvider(config.CustomProvider{
			BaseURL: "https://proxy.example",
			API:     config.WireAnthropic,
		}, store)
		if err == nil {
			t.Fatal("expected error for empty name")
		}
	})

	t.Run("env baseURL and apiKeyEnv", func(t *testing.T) {
		t.Setenv("CUSTOM_BASE", "https://env.example/v1")
		t.Setenv("CUSTOM_KEY", "from-env")
		p, _, err := buildCustomProvider(config.CustomProvider{
			Name:      "envproxy",
			BaseURL:   "{env:CUSTOM_BASE}",
			API:       config.WireOpenAI,
			APIKeyEnv: "{env:CUSTOM_KEY}",
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.Name() != "envproxy" {
			t.Fatalf("provider = %v", p)
		}
	})
}

func TestBuildBuiltinWithEndpointAnthropic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROXY_ANTHROPIC_KEY", "proxy-secret")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}

	var sawPath, sawKey, sawModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawKey = r.Header.Get("x-api-key")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			sawModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)

	// OpenCode-style baseURL includes /v1 — must not double to /v1/v1/messages.
	p, model, err, handled := buildBuiltinWithEndpoint("anthropic", config.ProviderEndpoint{
		BaseURL:   srv.URL + "/v1",
		APIKeyEnv: "PROXY_ANTHROPIC_KEY",
	}, store)
	if err != nil || !handled || p == nil {
		t.Fatalf("err=%v handled=%v p=%v", err, handled, p)
	}
	if model == "" {
		t.Error("expected default anthropic model")
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:     "claude-sonnet-test",
		MaxTokens: 16,
		Messages:  []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if sawPath != "/v1/messages" {
		t.Errorf("path = %q (OpenCode baseURL …/v1 must join /messages only)", sawPath)
	}
	if sawKey != "proxy-secret" {
		t.Errorf("key = %q", sawKey)
	}
	if sawModel != "claude-sonnet-test" {
		t.Errorf("wire model = %q", sawModel)
	}
}

func TestBuildBuiltinWithEndpointAnthropicOriginOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PROXY_ANTHROPIC_KEY", "proxy-secret")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(srv.Close)

	p, _, err, handled := buildBuiltinWithEndpoint("anthropic", config.ProviderEndpoint{
		BaseURL:   srv.URL, // origin only (legacy strike)
		APIKeyEnv: "PROXY_ANTHROPIC_KEY",
	}, store)
	if err != nil || !handled || p == nil {
		t.Fatalf("err=%v handled=%v p=%v", err, handled, p)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model: "m", MaxTokens: 8,
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if sawPath != "/v1/messages" {
		t.Errorf("origin-only path = %q", sawPath)
	}
}

func TestBuildBuiltinWithEndpointMissingEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MISSING_PROXY_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err, handled := buildBuiltinWithEndpoint("anthropic", config.ProviderEndpoint{
		BaseURL:   "https://proxy.example",
		APIKeyEnv: "MISSING_PROXY_KEY",
	}, store)
	if !handled || err == nil {
		t.Fatalf("want clear missing-env error, got err=%v handled=%v", err, handled)
	}
	if !strings.Contains(err.Error(), "MISSING_PROXY_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestBuildCustomProviderNestedModelWireID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QGENIE_KEY", "sk-test")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}

	var gotModel, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			gotModel = m
		}
		// @ai-sdk/openai → Responses API (not chat/completions).
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "hi"},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	raw := []byte(`{
  "QGenie_oai": {
    "npm": "@ai-sdk/openai",
    "name": "QGenie OAI",
    "options": {
      "baseURL": "` + srv.URL + `/v1",
      "apiKey": "{env:QGENIE_KEY}"
    },
    "models": {
      "gpt-5.5": {
        "name": "GPT-5.5",
        "limit": { "context": 272000, "output": 128000 },
        "variants": {
          "high": { "reasoningEffort": "high", "textVerbosity": "low" }
        }
      }
    }
  }
}`)
	pf, err := config.ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	cp := pf.Customs[0]
	if cp.API != config.WireResponses {
		t.Fatalf("API = %q, want responses (@ai-sdk/openai)", cp.API)
	}
	p, defaultModel, err := buildCustomProvider(cp, store)
	if err != nil {
		t.Fatal(err)
	}
	if defaultModel != "gpt-5.5" {
		t.Fatalf("defaultModel = %q (must be map key, not display name)", defaultModel)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:    defaultModel,
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var gotText string
	for ev := range ch {
		if ev.Type == provider.EventTextDelta {
			gotText += ev.Text
		}
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if gotPath != "/v1/responses" {
		t.Errorf("path = %q, want /v1/responses (not chat/completions)", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotModel != "gpt-5.5" {
		t.Errorf("wire model = %q, want map key gpt-5.5 (not display name)", gotModel)
	}
	if gotText != "hi" {
		t.Errorf("text = %q", gotText)
	}
}

func TestBuildCustomProviderOpenAICompatibleStillChat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COMPAT_KEY", "sk-compat")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			gotModel = m
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	raw := []byte(`{
  "my_oai": {
    "npm": "@ai-sdk/openai-compatible",
    "options": {
      "baseURL": "` + srv.URL + `/v1",
      "apiKey": "{env:COMPAT_KEY}"
    },
    "models": { "gpt-local": { "name": "Local GPT" } }
  }
}`)
	pf, err := config.ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Customs[0].API != config.WireOpenAI {
		t.Fatalf("API = %q", pf.Customs[0].API)
	}
	p, model, err := buildCustomProvider(pf.Customs[0], store)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model: model, Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotModel != "gpt-local" {
		t.Errorf("model = %q", gotModel)
	}
}

func TestBuildCustomProviderMissingAPIKeyEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ABSENT_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = buildCustomProvider(config.CustomProvider{
		Name:      "proxy",
		BaseURL:   "https://proxy.example/v1",
		API:       config.WireOpenAI,
		APIKeyEnv: "ABSENT_KEY",
	}, store)
	if err == nil {
		t.Fatal("expected missing env error")
	}
	if !strings.Contains(err.Error(), "ABSENT_KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestOptionalBearer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CUSTOM_KEY", "")
	store, err := auth.OpenStore(filepath.Join(home, ".strike", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}

	src := optionalBearer("local", store, "CUSTOM_KEY")
	tok, err := src(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Errorf("empty credentials = %q, want empty", tok)
	}

	t.Setenv("CUSTOM_KEY", "from-env")
	tok, err = src(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-env" {
		t.Errorf("env token = %q", tok)
	}

	t.Setenv("CUSTOM_KEY", "")
	if err := store.Set("local", auth.Credential{Type: auth.TypeAPIKey, APIKey: "from-store"}); err != nil {
		t.Fatal(err)
	}
	tok, err = src(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-store" {
		t.Errorf("store token = %q", tok)
	}

	// Env wins over store.
	t.Setenv("CUSTOM_KEY", "env-wins")
	tok, err = src(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "env-wins" {
		t.Errorf("env precedence = %q", tok)
	}
}

func TestBindSessionWorktreeCreatesAndCleans(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "--quiet", "--allow-empty", "-m", "init")

	mgr := session.NewManager(filepath.Join(home, ".strike", "sessions"))
	info, err := mgr.Create(session.CreateOptions{ID: "wt-bind-1", ProjectKey: repo})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Session: config.SessionConfig{Worktree: "always", WorktreeCleanup: "delete"}}
	toolDir, cleanup, notice, err := bindSessionWorktree(mgr, info.ID, repo, cfg, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("unexpected notice: %q", notice)
	}
	if toolDir == repo {
		t.Fatal("expected worktree path, got launch dir")
	}
	if _, err := os.Stat(toolDir); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}
	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreePath != toolDir {
		t.Fatalf("meta path = %q, tool = %q", got.WorktreePath, toolDir)
	}
	// Write inside worktree only.
	marker := filepath.Join(toolDir, "only-here.txt")
	if err := os.WriteFile(marker, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "only-here.txt")); !os.IsNotExist(err) {
		t.Fatal("file leaked to primary")
	}
	if cleanup == nil {
		t.Fatal("want cleanup when delete")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(toolDir); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("primary checkout removed: %v", err)
	}
}

func TestBindSessionWorktreeOffStaysOnLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	mgr := session.NewManager(filepath.Join(home, ".strike", "sessions"))
	info, err := mgr.Create(session.CreateOptions{ID: "no-wt"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Session: config.SessionConfig{Worktree: "off"}}
	toolDir, cleanup, notice, err := bindSessionWorktree(mgr, info.ID, dir, cfg, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("unexpected notice: %q", notice)
	}
	if toolDir != dir {
		t.Fatalf("toolDir = %q, want %q", toolDir, dir)
	}
	if cleanup != nil {
		t.Fatal("unexpected cleanup")
	}
}

func TestBindSessionWorktreeNonGitSoftFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	mgr := session.NewManager(filepath.Join(home, ".strike", "sessions"))
	info, err := mgr.Create(session.CreateOptions{ID: "non-git-wt"})
	if err != nil {
		t.Fatal(err)
	}
	// Explicit always wants a worktree; non-git cwd must not hard-fail launch.
	cfgAlways := config.Config{Session: config.SessionConfig{Worktree: "always"}}
	toolDir, cleanup, notice, err := bindSessionWorktree(mgr, info.ID, dir, cfgAlways, false, false, 0)
	if err != nil {
		t.Fatalf("soft-fail path returned error: %v", err)
	}
	if toolDir != dir {
		t.Fatalf("toolDir = %q, want launch dir %q", toolDir, dir)
	}
	if cleanup != nil {
		t.Fatal("unexpected cleanup on soft-fail")
	}
	if notice == "" || !strings.Contains(notice, "no git repository") {
		t.Fatalf("notice = %q, want no-git explanation", notice)
	}
	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreePath != "" {
		t.Fatalf("meta worktree path = %q, want empty", got.WorktreePath)
	}

	// Explicit --worktree force also soft-fails outside a repo.
	info2, err := mgr.Create(session.CreateOptions{ID: "non-git-force"})
	if err != nil {
		t.Fatal(err)
	}
	toolDir, cleanup, notice, err = bindSessionWorktree(mgr, info2.ID, dir, config.Config{Session: config.SessionConfig{Worktree: "off"}}, true, false, 0)
	if err != nil {
		t.Fatalf("force soft-fail: %v", err)
	}
	if toolDir != dir || cleanup != nil {
		t.Fatalf("force soft-fail toolDir=%q cleanup=%v", toolDir, cleanup != nil)
	}
	if !strings.Contains(notice, "no git repository") {
		t.Fatalf("force notice = %q", notice)
	}
}

func TestBindSessionWorktreeDefaultOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	mgr := session.NewManager(filepath.Join(home, ".strike", "sessions"))
	info, err := mgr.Create(session.CreateOptions{ID: "wt-default", ProjectKey: repo})
	if err != nil {
		t.Fatal(err)
	}
	// Empty Session.Worktree uses default off (launch cwd).
	toolDir, cleanup, notice, err := bindSessionWorktree(mgr, info.ID, repo, config.Config{}, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("unexpected notice: %q", notice)
	}
	if toolDir != repo {
		t.Fatalf("default off toolDir = %q, want launch dir %q", toolDir, repo)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup when no worktree")
	}
	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreePath != "" || got.WorktreeBranch != "" {
		t.Fatalf("meta worktree = %q / %q, want empty", got.WorktreePath, got.WorktreeBranch)
	}
}

func TestBindSessionWorktreeAutoSecondRoot(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	cmd := exec.Command(git, "init", "--quiet", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"-C", repo, "config", "user.email", "t@t"},
		{"-C", repo, "config", "user.name", "t"},
		{"-C", repo, "commit", "--quiet", "--allow-empty", "-m", "i"},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mgr := session.NewManager(filepath.Join(home, ".strike", "sessions"))
	first, err := mgr.Create(session.CreateOptions{ID: "root-1"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Session: config.SessionConfig{Worktree: "auto"}}
	// First root (openRootsBefore=0): no worktree.
	tool1, _, notice, err := bindSessionWorktree(mgr, first.ID, repo, cfg, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("unexpected notice: %q", notice)
	}
	if tool1 != repo {
		t.Fatalf("first root toolDir = %q", tool1)
	}
	second, err := mgr.Create(session.CreateOptions{ID: "root-2"})
	if err != nil {
		t.Fatal(err)
	}
	tool2, cleanup, notice, err := bindSessionWorktree(mgr, second.ID, repo, cfg, false, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Fatalf("unexpected notice: %q", notice)
	}
	if tool2 == repo {
		t.Fatal("second root should get worktree")
	}
	if cleanup != nil {
		t.Fatal("default cleanup is keep")
	}
	t.Cleanup(func() {
		info, _ := mgr.Get(second.ID)
		_ = project.Remove(context.Background(), repo, info.WorktreePath, info.WorktreeBranch)
	})
}
