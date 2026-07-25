package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// This file holds the shared test doubles for the frontend: scriptable fakes
// for every host.Services capability, plus the model-construction helpers the
// test suite builds on. The TUI is exercised entirely against these fakes —
// no auth store, config, models catalog, or history on disk — which is the
// whole point of the internal/host boundary.

// --- fakeAuth: a scriptable host.Auth ------------------------------------

type recordedAPIKey struct {
	provider string
	key      string
}

// fakeAuth is a host.Auth whose provider statuses, describe strings, and login
// outcomes are all scriptable, and which records credential mutations for
// assertions.
type fakeAuth struct {
	statuses    []host.ProviderStatus
	setCalls    []recordedAPIKey
	logoutCalls []string
	setErr      error
	logoutErr   error
	// oauth/device override the default login per provider; oauthErr/deviceErr
	// force Begin* to fail.
	oauth     map[string]*host.OAuthLogin
	device    map[string]*host.DeviceLogin
	oauthErr  map[string]error
	deviceErr map[string]error
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{statuses: defaultProviderStatuses()}
}

// defaultProviderStatuses mirrors the real local.New order and capability flags
// (anthropic: API key only; openai: OAuth+key; xai: OAuth+device+key; echo:
// builtin) so pickers and /auth behave as in production.
func defaultProviderStatuses() []host.ProviderStatus {
	return []host.ProviderStatus{
		{Name: "anthropic", Detail: "none", APIKey: true},
		{Name: "openai", Detail: "none", OAuth: true, APIKey: true},
		{Name: "xai", Detail: "none", OAuth: true, Device: true, APIKey: true},
		{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
	}
}

func (f *fakeAuth) Statuses() []host.ProviderStatus {
	return append([]host.ProviderStatus(nil), f.statuses...)
}

func (f *fakeAuth) Describe(provider string) string {
	for _, s := range f.statuses {
		if s.Name == provider {
			return s.Detail
		}
	}
	return "none"
}

func (f *fakeAuth) SetAPIKey(provider, key string) error {
	f.setCalls = append(f.setCalls, recordedAPIKey{provider: provider, key: key})
	return f.setErr
}

func (f *fakeAuth) Logout(provider string) error {
	f.logoutCalls = append(f.logoutCalls, provider)
	return f.logoutErr
}

func (f *fakeAuth) BeginOAuth(ctx context.Context, provider string) (*host.OAuthLogin, error) {
	if err := f.oauthErr[provider]; err != nil {
		return nil, err
	}
	if l := f.oauth[provider]; l != nil {
		return l, nil
	}
	// Default logins complete Wait immediately and accept any paste (no-op),
	// matching a host that wires WithPaste even when the test does not use it.
	return host.NewOAuthLogin("https://login.test/"+provider, func(context.Context) (string, error) {
		return "Signed in to " + provider, nil
	}).WithPaste(func(string) error { return nil }), nil
}

// oauthLoginBlockingPaste builds an OAuth login whose Wait blocks until a
// successful CompleteWithPaste (or ctx cancel). pasteCheck may reject pastes
// without unblocking Wait — the TUI keeps the wait modal open on those errors.
func oauthLoginBlockingPaste(url, outcome string, pasteCheck func(string) error) *host.OAuthLogin {
	ready := make(chan struct{})
	var once sync.Once
	return host.NewOAuthLogin(url, func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ready:
			return outcome, nil
		}
	}).WithPaste(func(raw string) error {
		if pasteCheck != nil {
			if err := pasteCheck(raw); err != nil {
				return err
			}
		}
		var opened bool
		once.Do(func() {
			close(ready)
			opened = true
		})
		if !opened {
			return errors.New("login already completed")
		}
		return nil
	})
}

func (f *fakeAuth) BeginDevice(ctx context.Context, provider string) (*host.DeviceLogin, error) {
	if err := f.deviceErr[provider]; err != nil {
		return nil, err
	}
	if l := f.device[provider]; l != nil {
		return l, nil
	}
	return host.NewDeviceLogin("DEVICE-CODE", "https://login.test/device/"+provider, func(context.Context) (string, error) {
		return "Signed in to " + provider, nil
	}), nil
}

// --- fakeCatalog: a scriptable host.Catalog ------------------------------

type fakeCatalog struct {
	ids map[string][]string
	err error
}

func (c *fakeCatalog) ModelIDs(ctx context.Context, provider string) ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	ids := c.ids[provider]
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models listed for %s", provider)
	}
	return append([]string(nil), ids...), nil
}

func (c *fakeCatalog) ContextWindow(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}

func (c *fakeCatalog) OutputLimit(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}

// --- fakeSettings: a recording host.Settings -----------------------------

type savedDefaults struct {
	provider string
	model    string
	agent    string
	effort   string
}

type fakeSettings struct {
	saved       []savedDefaults
	savedThemes []string
	err         error
	themeErr    error
}

func (s *fakeSettings) SaveDefaults(provider, model, agent, effort string) error {
	s.saved = append(s.saved, savedDefaults{provider: provider, model: model, agent: agent, effort: effort})
	return s.err
}

func (s *fakeSettings) SaveTheme(id string) error {
	if s.themeErr != nil {
		return s.themeErr
	}
	s.savedThemes = append(s.savedThemes, id)
	return s.err
}

// --- fakeHistory: an in-memory host.History ------------------------------

var errFakeHistoryClosed = errors.New("history store is closed")

// fakeHistory is an in-memory host.History. Enqueue appends synchronously (in
// submission order, like the real store's serial worker) and returns a
// pre-resolved channel, so tests can drain persistence results in any order
// without changing entry order. Setting fail makes Enqueue reject without
// appending, standing in for a closed store.
type fakeHistory struct {
	entries []string
	fail    bool
}

func newFakeHistory(entries ...string) *fakeHistory {
	return &fakeHistory{entries: append([]string(nil), entries...)}
}

func (f *fakeHistory) Entries() []string {
	return append([]string(nil), f.entries...)
}

func (f *fakeHistory) Enqueue(prompt string) <-chan error {
	done := make(chan error, 1)
	if f.fail {
		done <- errFakeHistoryClosed
		close(done)
		return done
	}
	f.entries = append(f.entries, prompt)
	done <- nil
	close(done)
	return done
}

// --- fakeFiles: a scriptable host.Files ----------------------------------

// fakeFiles is a host.Files that matches paths exactly as passed to ReadFile
// / ListDir (the TUI forwards the path argument unchanged).
type fakeFiles struct {
	files map[string][]byte
	dirs  map[string][]host.DirEntry
	err   error
}

func (f *fakeFiles) ReadFile(path string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeFiles) ListDir(path string) ([]host.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.dirs == nil {
		return nil, fmt.Errorf("directory not found: %s", path)
	}
	entries, ok := f.dirs[path]
	if !ok {
		return nil, fmt.Errorf("directory not found: %s", path)
	}
	out := make([]host.DirEntry, len(entries))
	copy(out, entries)
	return out, nil
}

// --- construction helpers ------------------------------------------------

// fakeSkill builds a host.Skill from a template, mirroring config.Skill.Render
// (which the frontend no longer sees): $ARGUMENTS substitution when present,
// otherwise appended arguments.
func fakeSkill(name, description, template string) host.Skill {
	hasArgs := strings.Contains(template, "$ARGUMENTS")
	return host.NewSkill(name, description, hasArgs, func(args string) string {
		if hasArgs {
			return strings.ReplaceAll(template, "$ARGUMENTS", args)
		}
		if args != "" {
			return template + "\n\nArguments: " + args
		}
		return template
	})
}

// testServices bundles the default fakes with the given agents and skills.
func testServices(agents []string, skills []host.Skill) host.Services {
	return host.Services{
		Auth:     newFakeAuth(),
		Catalog:  &fakeCatalog{ids: map[string][]string{"echo": {"echo-1"}, "openai": {"gpt-test"}}},
		Settings: &fakeSettings{},
		Agents:   agents,
		Skills:   skills,
	}
}

func newAppTestModel(agents []string, skills []host.Skill) (Model, chan protocol.Op) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	return New(ops, events, testServices(agents, skills)), ops
}

func newAppTestModelWithOptions(options Options) (Model, chan protocol.Op) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	return New(ops, events, testServices(nil, nil), options), ops
}

func newAppTestModelWithHistory(agents []string, skills []host.Skill, hist *fakeHistory) (Model, chan protocol.Op) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	services := testServices(agents, skills)
	services.History = hist
	return New(ops, events, services), ops
}
