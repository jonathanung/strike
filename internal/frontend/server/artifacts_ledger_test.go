package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

type fakeArtifacts struct {
	mu   sync.Mutex
	arts map[string]host.Artifact
}

func (f *fakeArtifacts) List(actorSession, actorRoot string, filter host.ArtifactListFilter) ([]host.ArtifactMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.ArtifactMeta
	for _, a := range f.arts {
		if a.Access == "owner" && a.OwnerSession != actorSession {
			continue
		}
		if a.Access == "team" && a.OwnerRoot != actorRoot {
			continue
		}
		if filter.Type != "" && a.Type != filter.Type {
			continue
		}
		out = append(out, host.ArtifactMeta{
			ID: a.ID, Type: a.Type, Title: a.Title, Version: a.Version,
			Scope: a.Scope, SessionID: a.SessionID, Access: a.Access,
			OwnerSession: a.OwnerSession, OwnerRoot: a.OwnerRoot,
			CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		})
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if filter.Offset > len(out) {
		return []host.ArtifactMeta{}, nil
	}
	out = out[filter.Offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeArtifacts) Get(id, actorSession, actorRoot string) (host.Artifact, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.arts[id]
	if !ok {
		return host.Artifact{}, false, nil
	}
	if a.Access == "owner" && a.OwnerSession != actorSession {
		return host.Artifact{}, false, nil
	}
	if a.Access == "team" && a.OwnerRoot != actorRoot {
		return host.Artifact{}, false, nil
	}
	return a, true, nil
}

func (f *fakeArtifacts) GetVersion(id string, version int, actorSession, actorRoot string) (host.Artifact, bool, error) {
	a, ok, err := f.Get(id, actorSession, actorRoot)
	if err != nil || !ok || a.Version != version {
		return host.Artifact{}, false, err
	}
	return a, true, nil
}

type fakeLedger struct {
	mu      sync.Mutex
	entries map[string]host.LedgerEntry
}

func (f *fakeLedger) ActiveSlice(path, taskID string) ([]host.LedgerEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.LedgerEntry
	for _, e := range f.entries {
		if e.Status == "active" {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeLedger) List(filter host.LedgerListFilter) ([]host.LedgerEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []host.LedgerEntry
	for _, e := range f.entries {
		if filter.Status != "" && e.Status != filter.Status {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeLedger) Get(id string) (host.LedgerEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	return e, ok, nil
}

func TestArtifactsLedgerAPI(t *testing.T) {
	now := time.Now().UTC()
	arts := &fakeArtifacts{arts: map[string]host.Artifact{
		"a1": {
			ID: "a1", Type: "findings", Title: "F", Content: "body", Version: 2,
			Scope: "project", Access: "team", OwnerSession: "s1", OwnerRoot: "root-a",
			CreatedAt: now, UpdatedAt: now,
		},
		"a2": {
			ID: "a2", Type: "patch", Title: "P", Content: "diff", Version: 1,
			Scope: "project", Access: "owner", OwnerSession: "s1", OwnerRoot: "root-a",
			CreatedAt: now, UpdatedAt: now,
		},
	}}
	led := &fakeLedger{entries: map[string]host.LedgerEntry{
		"l1": {ID: "l1", Kind: "decision", Statement: "ship it", Status: "active", AuthorSession: "s1", CreatedAt: now, UpdatedAt: now},
		"l2": {ID: "l2", Kind: "assumption", Statement: "old", Status: "invalidated", InvalidateReason: "nope", AuthorSession: "s1", CreatedAt: now, UpdatedAt: now},
	}}

	srv, err := New(Options{Services: &host.Services{Artifacts: arts, Ledger: led}})
	if err != nil {
		t.Fatal(err)
	}

	// Bootstrap advertises capabilities.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if rr.Code != 200 {
		t.Fatalf("bootstrap %d", rr.Code)
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if !boot.Capabilities.Artifacts || !boot.Capabilities.Ledger {
		t.Fatalf("caps: %+v", boot.Capabilities)
	}

	// List team-visible artifacts.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts?actorSession=s2&actorRoot=root-a", nil)
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list %d %s", rr.Code, rr.Body.String())
	}
	var list struct {
		Artifacts []artifactMetaDTO `json:"artifacts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Artifacts) != 1 || list.Artifacts[0].ID != "a1" {
		t.Fatalf("list: %+v", list.Artifacts)
	}

	// Cross-root denial → empty list / not found.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/artifacts?actorSession=x&actorRoot=root-b", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Artifacts) != 0 {
		t.Fatalf("cross-root list: %+v", list.Artifacts)
	}
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/artifacts/a1?actorSession=x&actorRoot=root-b", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-root get want 404 got %d", rr.Code)
	}

	// Get by id + version.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/artifacts/a1?actorSession=s2&actorRoot=root-a&version=2", nil))
	if rr.Code != 200 {
		t.Fatalf("get %d %s", rr.Code, rr.Body.String())
	}
	var one artifactDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || one.Content != "body" || one.Version != 2 {
		t.Fatalf("get dto: %+v err=%v", one, err)
	}

	// Ledger active + history + get.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ledger?active=1", nil))
	if rr.Code != 200 {
		t.Fatalf("active %d", rr.Code)
	}
	var active struct {
		Entries []ledgerEntryDTO `json:"entries"`
		Slice   string           `json:"slice"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &active)
	if active.Slice != "active" || len(active.Entries) != 1 || active.Entries[0].ID != "l1" {
		t.Fatalf("active: %+v", active)
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ledger", nil))
	var hist struct {
		Entries []ledgerEntryDTO `json:"entries"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &hist)
	if len(hist.Entries) != 2 {
		t.Fatalf("history len=%d", len(hist.Entries))
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ledger/l2", nil))
	if rr.Code != 200 {
		t.Fatalf("ledger get %d", rr.Code)
	}
	var le ledgerEntryDTO
	_ = json.Unmarshal(rr.Body.Bytes(), &le)
	if le.Status != "invalidated" || le.InvalidateReason != "nope" {
		t.Fatalf("provenance: %+v", le)
	}

	// Missing capability.
	bare, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("missing artifacts cap want 501 got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "artifacts") {
		t.Fatalf("body: %s", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ledger", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("missing ledger cap want 501 got %d", rr.Code)
	}

	// No mutation routes — POST should 404/method not allowed via mux.
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/artifacts", strings.NewReader(`{}`)))
	if rr.Code == 200 {
		t.Fatal("POST artifacts must not succeed")
	}
}
