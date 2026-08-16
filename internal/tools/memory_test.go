package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/persist/memory"
)

func openMemory(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir(), "test-project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMemoryWriteAndRead(t *testing.T) {
	store := openMemory(t)
	tc := allowAll(t.TempDir())
	tw := NewMemoryWrite(store)
	tr := NewMemoryRead(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key":   "build.cmd",
		"value": "make test",
		"tags":  []string{"build"},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"key": "build.cmd"`) {
		t.Errorf("write output = %s", res.Output)
	}
	if res.Title != "memory build.cmd" {
		t.Errorf("title = %q", res.Title)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"key": "build.cmd"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "make test") {
		t.Errorf("read output = %s", res.Output)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"tag": "build"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "1 memories tag:build" {
		t.Errorf("list title = %q", res.Title)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"key":    "build.cmd",
		"delete": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "deleted") {
		t.Errorf("delete output = %s", res.Output)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"key": "build.cmd"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no memory entry") {
		t.Errorf("miss output = %s", res.Output)
	}
}

func TestMemoryWriteValidation(t *testing.T) {
	store := openMemory(t)
	tw := NewMemoryWrite(store)
	tc := allowAll(t.TempDir())

	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{"value": "x"}), tc)
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("missing key err = %v", err)
	}
	_, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{"key": "k"}), tc)
	if err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("missing value err = %v", err)
	}
	_, err = tw.Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestMemoryPermissionDenied(t *testing.T) {
	store := openMemory(t)
	deny := errors.New("denied")
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return deny },
	}
	_, err := NewMemoryWrite(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "k", "value": "v",
	}), tc)
	if !errors.Is(err, deny) {
		t.Fatalf("write err = %v", err)
	}
	_, err = NewMemoryRead(store).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if !errors.Is(err, deny) {
		t.Fatalf("read err = %v", err)
	}
}

func TestMemoryReadListEmpty(t *testing.T) {
	store := openMemory(t)
	res, err := NewMemoryRead(store).Execute(context.Background(), mustJSON(t, map[string]any{}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 memories" {
		t.Errorf("title = %q", res.Title)
	}
	if strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("output = %q", res.Output)
	}
}
