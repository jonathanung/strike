package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/knowledge"
	"github.com/jonathanung/strike/internal/persist/memory"
)

func TestMemoryModeGatesTools(t *testing.T) {
	store := openMemory(t)
	tc := allowAll(t.TempDir())
	tc.SessionID = "sess-7"
	tw := NewMemoryWrite(store)
	tr := NewMemoryRead(store)

	tc.MemoryMode = memory.ModeIncognito
	if _, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "k", "value": "v",
	}), tc); err == nil || !strings.Contains(err.Error(), "incognito") {
		t.Fatalf("incognito write err = %v", err)
	}
	if _, err := tr.Execute(context.Background(), mustJSON(t, map[string]any{"key": "missing"}), tc); err != nil {
		t.Fatalf("incognito read: %v", err)
	}

	tc.MemoryMode = memory.ModeTemporary
	if _, err := tr.Execute(context.Background(), mustJSON(t, map[string]any{}), tc); err == nil || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("temporary read err = %v", err)
	}
	if _, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "k", "value": "v",
	}), tc); err == nil || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("temporary write err = %v", err)
	}

	tc.MemoryMode = memory.ModeNormal
	if _, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "k", "value": "api_key=supersecretvalue", "tags": []string{"preference"},
	}), tc); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get("k")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if strings.Contains(got.Value, "supersecretvalue") {
		t.Fatalf("tool stored secret: %q", got.Value)
	}
	if got.Author != "sess-7" {
		t.Fatalf("author = %q", got.Author)
	}
}

func TestMemoryScopeGlobal(t *testing.T) {
	dir := t.TempDir()
	project, err := memory.Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	global, err := memory.Open(dir, memory.GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = project.Close()
		_ = global.Close()
	})
	tc := allowAll(t.TempDir())
	tw := NewMemoryWriteScoped(project, global)
	if _, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "k", "value": "global-fact", "scope": "global", "tags": []string{"instruction"},
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := project.Get("k"); err != nil || ok {
		t.Fatalf("project should not see global key ok=%v err=%v", ok, err)
	}
	got, ok, err := global.Get("k")
	if err != nil || !ok || got.Value != "global-fact" {
		t.Fatalf("global = %#v ok=%v err=%v", got, ok, err)
	}
}

func TestKnowledgeToolRedactsAndRestarts(t *testing.T) {
	dir := t.TempDir()
	store, err := knowledge.Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	tc := allowAll(t.TempDir())
	tc.SessionID = "sess-k"
	toolk := NewKnowledge(store, nil)
	if _, err := toolk.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "docs/theme.md", "note": "password=supersecretvalue",
	}), tc); err != nil {
		t.Fatal(err)
	}
	tc.MemoryMode = memory.ModeIncognito
	if _, err := toolk.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "other.md",
	}), tc); err == nil || !strings.Contains(err.Error(), "incognito") {
		t.Fatalf("incognito pin err = %v", err)
	}
	_ = store.Close()
	reopened, err := knowledge.Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	pins, err := reopened.List()
	if err != nil || len(pins) != 1 || pins[0].Author != "sess-k" || strings.Contains(pins[0].Note, "supersecretvalue") {
		t.Fatalf("pins = %#v err=%v", pins, err)
	}
}
