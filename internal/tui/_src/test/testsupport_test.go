package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
	if f.logoutErr != nil {
		return f.logoutErr
	}
	for i, s := range f.statuses {
		if s.Name == provider && !s.Builtin {
			s.Authed = false
			s.Detail = "none"
			f.statuses[i] = s
			break
		}
	}
	return nil
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
	ids  map[string][]string
	meta map[string]host.ModelInfo // key: provider/model
	err  error
}

func (c *fakeCatalog) ModelIDs(ctx context.Context, provider string) ([]string, error) {
	infos, err := c.Models(ctx, provider)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	return ids, nil
}

func (c *fakeCatalog) Models(_ context.Context, provider string) ([]host.ModelInfo, error) {
	if c.err != nil {
		return nil, c.err
	}
	ids := c.ids[provider]
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models listed for %s", provider)
	}
	out := make([]host.ModelInfo, len(ids))
	for i, id := range ids {
		if c.meta != nil {
			if info, ok := c.meta[provider+"/"+id]; ok {
				info.ID = id
				out[i] = info
				continue
			}
		}
		out[i] = host.ModelInfo{ID: id}
	}
	return out, nil
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
	mode     string
}

type fakeSettings struct {
	saved       []savedDefaults
	savedThemes []string
	err         error
	themeErr    error
}

func (s *fakeSettings) SaveDefaults(provider, model, agent, effort, mode string) error {
	s.saved = append(s.saved, savedDefaults{provider: provider, model: model, agent: agent, effort: effort, mode: mode})
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

// --- fakeProviders: in-memory custom provider CRUD -----------------------

type fakeProviders struct {
	items []host.CustomProvider
	err   error
}

func (f *fakeProviders) List() []host.CustomProvider {
	out := make([]host.CustomProvider, len(f.items))
	copy(out, f.items)
	return out
}

func (f *fakeProviders) Get(name string) (host.CustomProvider, bool) {
	for _, p := range f.items {
		if p.Name == name {
			return p, true
		}
	}
	return host.CustomProvider{}, false
}

func (f *fakeProviders) Upsert(p host.CustomProvider) error {
	if f.err != nil {
		return f.err
	}
	for i, existing := range f.items {
		if existing.Name == p.Name {
			f.items[i] = p
			return nil
		}
	}
	f.items = append(f.items, p)
	// Keep Auth.Statuses in sync when tests share a fakeAuth — callers that
	// need status rows should update statuses themselves.
	return nil
}

func (f *fakeProviders) Remove(name string) error {
	if f.err != nil {
		return f.err
	}
	out := f.items[:0]
	for _, p := range f.items {
		if p.Name != name {
			out = append(out, p)
		}
	}
	f.items = append([]host.CustomProvider(nil), out...)
	return nil
}

// --- fakeFiles: a scriptable host.Files ----------------------------------

// fakeFiles is a host.Files that matches paths exactly as passed to ReadFile
// / ListDir (the TUI forwards the path argument unchanged).
type fakeFiles struct {
	files map[string][]byte
	dirs  map[string][]host.DirEntry
	// search optionally overrides SearchFiles results (nil → keys of files).
	search []string
	err    error
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

func (f *fakeFiles) SearchFiles(query string, limit int) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit <= 0 {
		limit = 30
	}
	var all []string
	if f.search != nil {
		all = append([]string(nil), f.search...)
	} else {
		for p := range f.files {
			all = append(all, p)
		}
		sort.Strings(all)
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		if len(all) > limit {
			return all[:limit], nil
		}
		return all, nil
	}
	var out []string
	for _, p := range all {
		lower := strings.ToLower(p)
		if strings.Contains(lower, query) || orderedSubsequence(lower, query) {
			out = append(out, p)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeFiles) ReadScoped(path string) (host.FileContent, error) {
	if f.err != nil {
		return host.FileContent{}, f.err
	}
	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")
	if path == "" || path == ".." || strings.HasPrefix(path, "../") || strings.HasPrefix(path, "/") {
		return host.FileContent{Path: path, Skip: true, Notice: "path escapes project root"}, nil
	}
	data, ok := f.files[path]
	if !ok {
		return host.FileContent{Path: path, Skip: true, Notice: "file not found"}, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return host.FileContent{Path: path, Skip: true, Notice: "binary file skipped"}, nil
	}
	return host.FileContent{Path: path, Content: string(data)}, nil
}

// --- fakeMemory: an in-memory host.Memory --------------------------------

type fakeMemory struct {
	mu            sync.Mutex
	entries       map[string]host.MemoryEntry
	err           error
	exportPath    string
	importEntries []host.MemoryEntry
	importFn      func(path string, replace bool) (int, error)
}

func newFakeMemory(entries ...host.MemoryEntry) *fakeMemory {
	f := &fakeMemory{entries: make(map[string]host.MemoryEntry)}
	for _, e := range entries {
		f.entries[e.Key] = host.MemoryEntry{
			Key:   e.Key,
			Value: e.Value,
			Tags:  append([]string(nil), e.Tags...),
		}
	}
	return f
}

func (f *fakeMemory) List(tag string) ([]host.MemoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]host.MemoryEntry, 0, len(f.entries))
	for _, e := range f.entries {
		if tag != "" {
			found := false
			for _, t := range e.Tags {
				if t == tag {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, host.MemoryEntry{
			Key:   e.Key,
			Value: e.Value,
			Tags:  append([]string(nil), e.Tags...),
		})
	}
	// Stable order for notice text.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Key < out[i].Key {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func (f *fakeMemory) Get(key string) (host.MemoryEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return host.MemoryEntry{}, false, f.err
	}
	e, ok := f.entries[key]
	if !ok {
		return host.MemoryEntry{}, false, nil
	}
	return host.MemoryEntry{
		Key:   e.Key,
		Value: e.Value,
		Tags:  append([]string(nil), e.Tags...),
	}, true, nil
}

func (f *fakeMemory) Put(key, value string, tags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.entries[key] = host.MemoryEntry{
		Key:   key,
		Value: value,
		Tags:  append([]string(nil), tags...),
	}
	return nil
}

func (f *fakeMemory) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if _, ok := f.entries[key]; !ok {
		return fmt.Errorf("memory: key not found")
	}
	delete(f.entries, key)
	return nil
}

func (f *fakeMemory) Export(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is empty")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path escapes project root")
	}
	f.exportPath = path
	return nil
}

func (f *fakeMemory) Import(path string, replace bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("path is empty")
	}
	if strings.Contains(path, "..") {
		return 0, fmt.Errorf("path escapes project root")
	}
	if f.importFn != nil {
		return f.importFn(path, replace)
	}
	if replace {
		f.entries = make(map[string]host.MemoryEntry)
	}
	// Default: no-op import of zero entries unless importEntries seeded.
	n := 0
	for _, e := range f.importEntries {
		f.entries[e.Key] = host.MemoryEntry{
			Key:   e.Key,
			Value: e.Value,
			Tags:  append([]string(nil), e.Tags...),
		}
		n++
	}
	return n, nil
}

// --- fakeIssues: an in-memory host.Issues ---------------------------------

type fakeIssues struct {
	mu          sync.Mutex
	nextID      int
	items       map[int]host.Issue
	err         error
	exportPath  string
	importItems []host.Issue
	importFn    func(path string, replace bool) (int, error)
}

func newFakeIssues(items ...host.Issue) *fakeIssues {
	f := &fakeIssues{nextID: 1, items: make(map[int]host.Issue)}
	for _, iss := range items {
		if iss.ID >= f.nextID {
			f.nextID = iss.ID + 1
		}
		if iss.Status == "" {
			iss.Status = "open"
		}
		f.items[iss.ID] = iss
	}
	return f
}

func (f *fakeIssues) List(status string) ([]host.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]host.Issue, 0, len(f.items))
	for _, iss := range f.items {
		if status != "" && iss.Status != status {
			continue
		}
		out = append(out, iss)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func (f *fakeIssues) Get(id int) (host.Issue, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return host.Issue{}, false, f.err
	}
	iss, ok := f.items[id]
	return iss, ok, nil
}

func (f *fakeIssues) Create(title, body string) (host.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return host.Issue{}, f.err
	}
	iss := host.Issue{ID: f.nextID, Title: title, Body: body, Status: "open"}
	f.nextID++
	f.items[iss.ID] = iss
	return iss, nil
}

func (f *fakeIssues) Update(id int, title, body, status *string) (host.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return host.Issue{}, f.err
	}
	iss, ok := f.items[id]
	if !ok {
		return host.Issue{}, fmt.Errorf("issue: not found")
	}
	if title != nil {
		iss.Title = *title
	}
	if body != nil {
		iss.Body = *body
	}
	if status != nil {
		iss.Status = *status
	}
	f.items[id] = iss
	return iss, nil
}

func (f *fakeIssues) Close(id int) (host.Issue, error) {
	st := "closed"
	return f.Update(id, nil, nil, &st)
}

func (f *fakeIssues) Export(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is empty")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path escapes project root")
	}
	f.exportPath = path
	return nil
}

func (f *fakeIssues) Import(path string, replace bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("path is empty")
	}
	if strings.Contains(path, "..") {
		return 0, fmt.Errorf("path escapes project root")
	}
	if f.importFn != nil {
		return f.importFn(path, replace)
	}
	if replace {
		f.items = make(map[int]host.Issue)
		f.nextID = 1
	}
	n := 0
	for _, iss := range f.importItems {
		if iss.Status == "" {
			iss.Status = "open"
		}
		f.items[iss.ID] = iss
		if iss.ID >= f.nextID {
			f.nextID = iss.ID + 1
		}
		n++
	}
	return n, nil
}

// --- fakeSessions: scriptable host.Sessions ------------------------------

// fakeSessions is an in-memory host.Sessions for transcript navigation tests.
// Optional refresh implements host.PRStateRefresher when non-nil.
// When projectKey is set, List scopes to that key (legacy empty-key omitted);
// ListAllProjects always returns every root matching rootsOnly.
type fakeSessions struct {
	byID       map[string]host.Session
	children   map[string][]host.Session // parentID → kids
	logs       map[string][]byte         // id → JSONL
	projectKey string                    // empty = no List filter
	getErr     error
	listErr    error
	replayErr  error
	refresh    func([]host.Session) []host.Session
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{
		byID:     map[string]host.Session{},
		children: map[string][]host.Session{},
		logs:     map[string][]byte{},
	}
}

func newFakeSessionsForProject(projectKey string) *fakeSessions {
	f := newFakeSessions()
	f.projectKey = projectKey
	return f
}

func (f *fakeSessions) put(s host.Session, jsonl []byte) {
	f.byID[s.ID] = s
	f.logs[s.ID] = jsonl
	if s.ParentID != "" {
		f.children[s.ParentID] = append(f.children[s.ParentID], s)
	}
}

func (f *fakeSessions) Get(id string) (host.Session, bool, error) {
	if f.getErr != nil {
		return host.Session{}, false, f.getErr
	}
	s, ok := f.byID[id]
	return s, ok, nil
}

func (f *fakeSessions) List(rootsOnly bool) ([]host.Session, error) {
	return f.list(rootsOnly, false)
}

func (f *fakeSessions) ListAllProjects(rootsOnly bool) ([]host.Session, error) {
	return f.list(rootsOnly, true)
}

func (f *fakeSessions) list(rootsOnly, allProjects bool) ([]host.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]host.Session, 0, len(f.byID))
	for _, s := range f.byID {
		if rootsOnly && s.ParentID != "" {
			continue
		}
		if !allProjects && f.projectKey != "" && s.ProjectKey != f.projectKey {
			continue
		}
		out = append(out, s)
	}
	// Stable newest-first by UpdatedAt then ID (matches session.Manager.List).
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (f *fakeSessions) Children(parentID string) ([]host.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := append([]host.Session(nil), f.children[parentID]...)
	return out, nil
}

func (f *fakeSessions) ReplayJSONL(id string) ([]byte, error) {
	if f.replayErr != nil {
		return nil, f.replayErr
	}
	data, ok := f.logs[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return data, nil
}

func (f *fakeSessions) Fork(id string) (host.Session, error) {
	id = strings.TrimSpace(id)
	src, ok := f.byID[id]
	if !ok {
		return host.Session{}, fmt.Errorf("session %q not found", id)
	}
	if src.ParentID != "" {
		return host.Session{}, fmt.Errorf("session %q is a subagent transcript; fork a root session", id)
	}
	childID := id + "-fork"
	if _, exists := f.byID[childID]; exists {
		childID = fmt.Sprintf("%s-fork-%d", id, len(f.byID))
	}
	title := strings.TrimSpace(src.Title)
	if title == "" {
		title = id
	}
	if !strings.HasPrefix(strings.ToLower(title), "fork of ") {
		title = "fork of " + title
	}
	child := host.Session{
		ID:         childID,
		Title:      title,
		ProjectKey: src.ProjectKey,
		UpdatedAt:  time.Now().UTC(),
	}
	f.byID[child.ID] = child
	f.logs[child.ID] = append([]byte(nil), f.logs[id]...)
	return child, nil
}

func (f *fakeSessions) Rename(id, title string) (host.Session, error) {
	id = strings.TrimSpace(id)
	s, ok := f.byID[id]
	if !ok {
		return host.Session{}, fmt.Errorf("session %q not found", id)
	}
	s.Title = strings.TrimSpace(title)
	f.byID[id] = s
	return s, nil
}

func (f *fakeSessions) Delete(id string, force bool) error {
	id = strings.TrimSpace(id)
	s, ok := f.byID[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	if s.Open && !force {
		return fmt.Errorf("session %q is open; force required to delete", id)
	}
	delete(f.byID, id)
	delete(f.logs, id)
	if s.ParentID != "" {
		kids := f.children[s.ParentID]
		out := kids[:0]
		for _, c := range kids {
			if c.ID != id {
				out = append(out, c)
			}
		}
		f.children[s.ParentID] = out
	}
	delete(f.children, id)
	return nil
}

func (f *fakeSessions) RefreshPRStates(in []host.Session) []host.Session {
	if f.refresh == nil {
		return in
	}
	return f.refresh(in)
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

// fakeTelemetry is a scriptable host.Telemetry for TUI tests.
type fakeTelemetry struct {
	sample host.TelemetrySample
	err    error
}

func (f *fakeTelemetry) Sample(context.Context, string) (host.TelemetrySample, error) {
	if f == nil {
		return host.TelemetrySample{}, nil
	}
	return f.sample, f.err
}

// testServices bundles the default fakes with the given agents and skills.
func testServices(agents []string, skills []host.Skill) host.Services {
	return host.Services{
		Auth:      newFakeAuth(),
		Catalog:   &fakeCatalog{ids: map[string][]string{"echo": {"echo-1"}, "openai": {"gpt-test"}}},
		Settings:  &fakeSettings{},
		Memory:    newFakeMemory(),
		Providers: &fakeProviders{},
		Telemetry: &fakeTelemetry{sample: host.TelemetrySample{
			CPUHostOK: true, CPUHostPct: 10,
			MemOK: true, MemUsedBytes: 1024 * 1024 * 1024, MemTotalBytes: 8 * 1024 * 1024 * 1024,
			DiskOK: true, DiskUsedBytes: 50 * 1024 * 1024 * 1024, DiskTotalBytes: 100 * 1024 * 1024 * 1024,
			DiskFreeBytes: 50 * 1024 * 1024 * 1024,
		}},
		Agents: agents,
		Skills: skills,
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
