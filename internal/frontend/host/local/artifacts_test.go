package local

import (
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/persist/artifact"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

func TestArtifactsNilStore(t *testing.T) {
	if NewArtifacts(nil) != nil {
		t.Fatal("nil store should yield nil host.Artifacts")
	}
}

func TestArtifactsListGetVisibilityAndBounds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := artifact.Open(t.TempDir(), "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc := NewArtifacts(store)
	if svc == nil {
		t.Fatal("NewArtifacts returned nil")
	}

	teamArt, err := store.Create(artifact.CreateInput{
		Type: artifact.TypeFindings, Title: "team find", Content: "body sk-ant-secretvalue999",
		Scope: artifact.ScopeProject, Access: artifact.AccessTeam,
		OwnerSession: "child-1", OwnerRoot: "root-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerArt, err := store.Create(artifact.CreateInput{
		Type: artifact.TypePatch, Title: "owner only", Content: "patch",
		Scope: artifact.ScopeProject, Access: artifact.AccessOwner,
		OwnerSession: "child-1", OwnerRoot: "root-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Different project root — should not be visible to root-a team.
	_, err = store.Create(artifact.CreateInput{
		Type: artifact.TypeFindings, Title: "other root", Content: "x",
		Scope: artifact.ScopeProject, Access: artifact.AccessTeam,
		OwnerSession: "other-child", OwnerRoot: "root-b",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Team member under root-a sees team artifact, not other root.
	list, err := svc.List("child-2", "root-a", host.ArtifactListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range list {
		ids[m.ID] = true
	}
	if !ids[teamArt.ID] {
		t.Fatalf("expected team artifact visible: %+v", list)
	}
	if ids[ownerArt.ID] {
		t.Fatal("owner-only artifact must not be visible to sibling")
	}
	if len(list) != 1 {
		t.Fatalf("want 1 visible artifact, got %d", len(list))
	}

	// Owner can read owner-only.
	got, ok, err := svc.Get(ownerArt.ID, "child-1", "root-a")
	if err != nil || !ok {
		t.Fatalf("owner get: ok=%v err=%v", ok, err)
	}
	if got.Version != 1 {
		t.Fatalf("version=%d", got.Version)
	}

	// Cross-root denial.
	if _, ok, err := svc.Get(teamArt.ID, "child-x", "root-b"); err != nil || ok {
		t.Fatalf("cross-root should deny: ok=%v err=%v", ok, err)
	}

	// Redaction on content.
	redacted, ok, err := svc.Get(teamArt.ID, "child-2", "root-a")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if strings.Contains(redacted.Content, "sk-ant-secretvalue999") {
		t.Fatalf("content not redacted: %q", redacted.Content)
	}
	if !strings.Contains(redacted.Content, redact.Placeholder) && !strings.Contains(redacted.Content, "REDACTED") {
		t.Fatalf("expected redaction marker: %q", redacted.Content)
	}

	// Pagination bounds.
	for i := 0; i < 5; i++ {
		_, err := store.Create(artifact.CreateInput{
			Type: artifact.TypeFindings, Title: "extra", Content: "c",
			Access: artifact.AccessTeam, OwnerSession: "child-1", OwnerRoot: "root-a",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.List("child-1", "root-a", host.ArtifactListFilter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("limit=2 offset=1 → got %d", len(page))
	}

	// GetVersion
	v1, ok, err := svc.GetVersion(teamArt.ID, 1, "child-1", "root-a")
	if err != nil || !ok || v1.Version != 1 {
		t.Fatalf("GetVersion: ok=%v ver=%d err=%v", ok, v1.Version, err)
	}
	if _, ok, err := svc.GetVersion(teamArt.ID, 99, "child-1", "root-a"); err != nil || ok {
		t.Fatalf("missing version should be not-found: ok=%v err=%v", ok, err)
	}
}

func TestArtifactsConcurrentReads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := artifact.Open(t.TempDir(), "proj-race")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewArtifacts(store)
	a, err := store.Create(artifact.CreateInput{
		Type: artifact.TypeContract, Title: "c", Content: "body",
		Access: artifact.AccessTeam, OwnerSession: "s1", OwnerRoot: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.List("s1", "r1", host.ArtifactListFilter{}); err != nil {
				t.Errorf("list: %v", err)
			}
			if _, ok, err := svc.Get(a.ID, "s1", "r1"); err != nil || !ok {
				t.Errorf("get: ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
}
