package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type lifecycleFakeRuntime struct {
	mu sync.Mutex

	imageID   string
	current   *InspectState
	nextID    int
	infoCalls chan struct{}

	createCalls   int
	createEntered chan struct{}
	createRelease chan struct{}
	stopCalls     int
	stopEntered   chan struct{}
	stopRelease   chan struct{}

	conflictOnCreate bool
}

func newLifecycleFakeRuntime() *lifecycleFakeRuntime {
	return &lifecycleFakeRuntime{
		imageID:   "sha256:test-image",
		infoCalls: make(chan struct{}, 8),
	}
}

func (f *lifecycleFakeRuntime) exec(_ context.Context, _ string, args ...string) (string, string, int, error) {
	if len(args) == 0 {
		return "", "missing command", 1, nil
	}
	switch args[0] {
	case "info":
		f.infoCalls <- struct{}{}
		return "test", "", 0, nil
	case "image":
		return f.imageID, "", 0, nil
	case "network":
		return "", "", 0, nil
	case "inspect":
		return f.inspect(args)
	case "create":
		return f.create(args)
	case "start":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.matchesCurrent(args[len(args)-1]) {
			f.current.Running = true
			f.current.Status = "running"
			return "", "", 0, nil
		}
		return "", "missing", 1, nil
	case "stop":
		f.mu.Lock()
		f.stopCalls++
		entered, release := f.stopEntered, f.stopRelease
		f.mu.Unlock()
		if entered != nil {
			entered <- struct{}{}
		}
		if release != nil {
			<-release
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.matchesCurrent(args[len(args)-1]) {
			f.current.Running = false
			f.current.Status = "exited"
		}
		return "", "", 0, nil
	case "rm":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.matchesCurrent(args[len(args)-1]) {
			f.current = nil
		}
		return "", "", 0, nil
	}
	return "", "unhandled " + strings.Join(args, " "), 1, nil
}

func (f *lifecycleFakeRuntime) inspect(args []string) (string, string, int, error) {
	ref := args[len(args)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.matchesCurrent(ref) {
		return "", "Error: No such object: " + ref, 1, nil
	}
	if strings.Contains(args[2], "json .Config.Labels") {
		data, _ := json.Marshal(f.current.Labels)
		return string(data), "", 0, nil
	}
	return fmt.Sprintf("%s|%s|%t|%s|%s|%d|%t|%s",
		f.current.ID, f.current.Name, f.current.Running, f.current.Status,
		f.current.Image, f.current.ExitCode, f.current.OOMKilled, f.current.Error), "", 0, nil
}

func (f *lifecycleFakeRuntime) create(args []string) (string, string, int, error) {
	name := argAfter(args, "--name")
	labels := map[string]string{}
	for i, arg := range args {
		if arg == "--label" && i+1 < len(args) {
			key, value, ok := strings.Cut(args[i+1], "=")
			if ok {
				labels[key] = value
			}
		}
	}

	f.mu.Lock()
	f.createCalls++
	call := f.createCalls
	entered, release := f.createEntered, f.createRelease
	conflict := f.conflictOnCreate
	if conflict {
		f.conflictOnCreate = false
		f.nextID++
		f.current = &InspectState{
			ID: "cid-winner", Name: name, Running: true, Status: "running",
			Image: f.imageID, Labels: labels,
		}
	}
	f.mu.Unlock()
	if conflict {
		return "", "Conflict: name already in use", 1, nil
	}
	if call == 1 && entered != nil {
		entered <- struct{}{}
		<-release
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current != nil {
		return "", "Conflict: name already in use", 1, nil
	}
	f.nextID++
	id := fmt.Sprintf("cid-%d", f.nextID)
	f.current = &InspectState{ID: id, Name: name, Status: "created", Image: f.imageID, Labels: labels}
	return id, "", 0, nil
}

func (f *lifecycleFakeRuntime) matchesCurrent(ref string) bool {
	return f.current != nil && (ref == f.current.ID || ref == f.current.Name)
}

func argAfter(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func newLifecycleTestManagers(t *testing.T, repo string, fake *lifecycleFakeRuntime) (*Manager, *Manager, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.TemplateVersion = "concurrency-test"
	cfg.Network.Mode = "none"
	cfg.Auth.ForwardEnv = nil
	newManager := func() *Manager {
		cli := NewCLI("docker")
		cli.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
		cli.ExecFn = fake.exec
		manager, err := NewManager(repo, cfg, cli)
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	m1, m2 := newManager(), newManager()
	_, hash, err := m1.dockerfileAndHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Cache.SetImageID(fake.imageID); err != nil {
		t.Fatal(err)
	}
	if err := m1.Cache.SetConfigHash(hash); err != nil {
		t.Fatal(err)
	}
	return m1, m2, hash
}

type launchOutcome struct {
	result LaunchResult
	err    error
}

func launchAsync(manager *Manager, opts LaunchOpts) <-chan launchOutcome {
	done := make(chan launchOutcome, 1)
	go func() {
		result, err := manager.LaunchWithResult(context.Background(), opts)
		done <- launchOutcome{result: result, err: err}
	}()
	return done
}

func requireSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func requireBlocked(t *testing.T, done <-chan launchOutcome, label string) {
	t.Helper()
	select {
	case outcome := <-done:
		t.Fatalf("%s completed before lifecycle mutation released: result=%+v err=%v", label, outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireOutcome(t *testing.T, done <-chan launchOutcome) launchOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for launch")
		return launchOutcome{}
	}
}

func TestConcurrentInitialLaunchCreatesOnce(t *testing.T) {
	repo := t.TempDir()
	fake := newLifecycleFakeRuntime()
	fake.createEntered = make(chan struct{}, 1)
	fake.createRelease = make(chan struct{})
	m1, m2, _ := newLifecycleTestManagers(t, repo, fake)

	first := launchAsync(m1, LaunchOpts{Headless: true})
	requireSignal(t, fake.createEntered, "first create")
	requireSignal(t, fake.infoCalls, "first availability check")
	released := false
	defer func() {
		if !released {
			close(fake.createRelease)
		}
	}()

	second := launchAsync(m2, LaunchOpts{Headless: true})
	requireSignal(t, fake.infoCalls, "second availability check")
	requireBlocked(t, second, "second launch")
	close(fake.createRelease)
	released = true

	one, two := requireOutcome(t, first), requireOutcome(t, second)
	if one.err != nil || two.err != nil {
		t.Fatalf("launch errors: first=%v second=%v", one.err, two.err)
	}
	if one.result.ID != two.result.ID || one.result.Mode != LaunchModeStarted || two.result.Mode != LaunchModeAttached {
		t.Fatalf("outcomes: first=%+v second=%+v", one.result, two.result)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", fake.createCalls)
	}
	if fake.stopCalls != 0 {
		t.Fatalf("stop calls = %d, compatible winner must not be stopped", fake.stopCalls)
	}
}

func TestConcurrentStaleRebuildReinspectsBeforeAttach(t *testing.T) {
	repo := t.TempDir()
	fake := newLifecycleFakeRuntime()
	fake.stopEntered = make(chan struct{}, 1)
	fake.stopRelease = make(chan struct{})
	m1, m2, _ := newLifecycleTestManagers(t, repo, fake)
	name := ContainerName(repo)
	fake.current = &InspectState{
		ID: "cid-stale", Name: name, Running: true, Status: "running", Image: fake.imageID,
		Labels: map[string]string{LabelManaged: "true", LabelConfigHash: "stale", LabelImageID: fake.imageID},
	}
	if err := m1.Cache.SetContainerID(fake.current.ID); err != nil {
		t.Fatal(err)
	}

	rebuild := launchAsync(m1, LaunchOpts{Headless: true, Replace: true})
	requireSignal(t, fake.stopEntered, "stale container stop")
	requireSignal(t, fake.infoCalls, "rebuild availability check")
	released := false
	defer func() {
		if !released {
			close(fake.stopRelease)
		}
	}()

	attach := launchAsync(m2, LaunchOpts{Headless: true, AttachStale: true})
	requireSignal(t, fake.infoCalls, "attach availability check")
	requireBlocked(t, attach, "concurrent attach")
	close(fake.stopRelease)
	released = true

	rebuilt, attached := requireOutcome(t, rebuild), requireOutcome(t, attach)
	if rebuilt.err != nil || attached.err != nil {
		t.Fatalf("launch errors: rebuild=%v attach=%v", rebuilt.err, attached.err)
	}
	if rebuilt.result.Mode != LaunchModeRebuilt || attached.result.Mode != LaunchModeAttached {
		t.Fatalf("outcomes: rebuild=%+v attach=%+v", rebuilt.result, attached.result)
	}
	if rebuilt.result.ID == "cid-stale" || rebuilt.result.ID != attached.result.ID {
		t.Fatalf("attach did not re-inspect rebuilt container: rebuild=%+v attach=%+v", rebuilt.result, attached.result)
	}
}

func TestRebuildWaitsForActiveAttach(t *testing.T) {
	repo := t.TempDir()
	fake := newLifecycleFakeRuntime()
	fake.stopEntered = make(chan struct{}, 1)
	m1, m2, _ := newLifecycleTestManagers(t, repo, fake)
	fake.current = &InspectState{
		ID: "cid-stale", Name: ContainerName(repo), Running: true, Status: "running", Image: fake.imageID,
		Labels: map[string]string{LabelManaged: "true", LabelConfigHash: "stale", LabelImageID: fake.imageID},
	}
	if err := m1.Cache.SetContainerID(fake.current.ID); err != nil {
		t.Fatal(err)
	}
	attachEntered := make(chan struct{}, 1)
	attachRelease := make(chan struct{})
	m1.AttachFn = func(context.Context, string, string, []string, bool) error {
		attachEntered <- struct{}{}
		<-attachRelease
		return nil
	}

	attached := launchAsync(m1, LaunchOpts{Attach: true, AttachStale: true})
	requireSignal(t, attachEntered, "active attach")
	requireSignal(t, fake.infoCalls, "attach availability check")
	released := false
	defer func() {
		if !released {
			close(attachRelease)
		}
	}()

	rebuilt := launchAsync(m2, LaunchOpts{Headless: true, Replace: true})
	requireSignal(t, fake.infoCalls, "rebuild availability check")
	requireBlocked(t, rebuilt, "rebuild with active attach")
	select {
	case <-fake.stopEntered:
		t.Fatal("rebuild stopped the container while attach lease was active")
	default:
	}

	close(attachRelease)
	released = true
	requireSignal(t, fake.stopEntered, "stop after attach release")
	attachOutcome, rebuildOutcome := requireOutcome(t, attached), requireOutcome(t, rebuilt)
	if attachOutcome.err != nil || rebuildOutcome.err != nil {
		t.Fatalf("launch errors: attach=%v rebuild=%v", attachOutcome.err, rebuildOutcome.err)
	}
	if rebuildOutcome.result.Mode != LaunchModeRebuilt || rebuildOutcome.result.ID == attachOutcome.result.ID {
		t.Fatalf("outcomes: attach=%+v rebuild=%+v", attachOutcome.result, rebuildOutcome.result)
	}
}

func TestCreateConflictAdoptsCompatibleWinner(t *testing.T) {
	repo := t.TempDir()
	fake := newLifecycleFakeRuntime()
	fake.conflictOnCreate = true
	m1, _, _ := newLifecycleTestManagers(t, repo, fake)

	result, err := m1.LaunchWithResult(context.Background(), LaunchOpts{Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "cid-winner" || result.Mode != LaunchModeAttached {
		t.Fatalf("result = %+v", result)
	}
}

func TestForceBuildWithoutReplaceDoesNotStopLiveStaleContainer(t *testing.T) {
	repo := t.TempDir()
	fake := newLifecycleFakeRuntime()
	m1, _, _ := newLifecycleTestManagers(t, repo, fake)
	fake.current = &InspectState{
		ID: "cid-stale", Name: ContainerName(repo), Running: true, Status: "running", Image: fake.imageID,
		Labels: map[string]string{LabelManaged: "true", LabelConfigHash: "stale", LabelImageID: fake.imageID},
	}
	if err := m1.Cache.SetContainerID(fake.current.ID); err != nil {
		t.Fatal(err)
	}

	_, err := m1.LaunchWithResult(context.Background(), LaunchOpts{Headless: true, ForceRebuild: true})
	if !errors.Is(err, ErrConfigDrift) {
		t.Fatalf("expected config drift, got %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stopCalls != 0 || fake.current == nil || !fake.current.Running {
		t.Fatalf("live stale container was mutated: stops=%d state=%+v", fake.stopCalls, fake.current)
	}
}

func TestExecTerminationErrorContext(t *testing.T) {
	base := errors.New("exit status")
	tests := []struct {
		name string
		err  *ExecTerminationError
		want string
	}{
		{name: "oom", err: &ExecTerminationError{Err: base, ExecExitCode: 137, ContainerExitCode: 137, OOMKilled: true}, want: "OOM-killed"},
		{name: "container exited", err: &ExecTerminationError{Err: base, ExecExitCode: 125, ContainerStatus: "exited", ContainerExitCode: 2}, want: "container exited"},
		{name: "killed exec", err: &ExecTerminationError{Err: base, ExecExitCode: 137, ContainerRunning: true, ContainerStatus: "running"}, want: "exec was killed"},
		{name: "inspect failed", err: &ExecTerminationError{Err: base, ExecExitCode: 137, InspectErr: errors.New("missing")}, want: "inspection failed"},
		{name: "ordinary exit", err: &ExecTerminationError{Err: base, ExecExitCode: 7, ContainerRunning: true, ContainerStatus: "running"}, want: "status 7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); !strings.Contains(got, tt.want) {
				t.Fatalf("error %q does not contain %q", got, tt.want)
			}
			if !errors.Is(tt.err, base) {
				t.Fatal("underlying exit error not preserved")
			}
		})
	}
}
