package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/persist/issue"
)

func openIssue(t *testing.T) *issue.Store {
	t.Helper()
	s, err := issue.Open(t.TempDir(), "test-project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestIssueWriteAndRead(t *testing.T) {
	store := openIssue(t)
	tc := allowAll(t.TempDir())
	tw := NewIssueWrite(store)
	tr := NewIssueRead(store)

	res, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"title": "fix auth",
		"body":  "login fails",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"title": "fix auth"`) {
		t.Errorf("write output = %s", res.Output)
	}
	if res.Title != "issue #1 open" {
		t.Errorf("title = %q", res.Title)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"id": 1}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "login fails") {
		t.Errorf("read output = %s", res.Output)
	}

	res, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{
		"id":     1,
		"status": "closed",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "issue #1 closed" {
		t.Errorf("close title = %q", res.Title)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"status": "open"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 issues status:open" {
		t.Errorf("list title = %q", res.Title)
	}

	res, err = tr.Execute(context.Background(), mustJSON(t, map[string]any{"id": 99}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no issue #99") {
		t.Errorf("miss output = %s", res.Output)
	}
}

func TestIssueWriteValidation(t *testing.T) {
	store := openIssue(t)
	tw := NewIssueWrite(store)
	tc := allowAll(t.TempDir())

	_, err := tw.Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("missing title err = %v", err)
	}
	_, err = tw.Execute(context.Background(), mustJSON(t, map[string]any{"id": 1}), tc)
	if err == nil || !strings.Contains(err.Error(), "title, body, and/or status") {
		t.Fatalf("empty update err = %v", err)
	}
	_, err = tw.Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestIssuePermissionDenied(t *testing.T) {
	store := openIssue(t)
	deny := errors.New("denied")
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return deny },
	}
	_, err := NewIssueWrite(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"title": "x",
	}), tc)
	if !errors.Is(err, deny) {
		t.Fatalf("write err = %v", err)
	}
	_, err = NewIssueRead(store).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if !errors.Is(err, deny) {
		t.Fatalf("read err = %v", err)
	}
}

func TestIssueReadListEmpty(t *testing.T) {
	store := openIssue(t)
	res, err := NewIssueRead(store).Execute(context.Background(), mustJSON(t, map[string]any{}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "0 issues" {
		t.Errorf("title = %q", res.Title)
	}
	if strings.TrimSpace(res.Output) != "[]" {
		t.Errorf("output = %q", res.Output)
	}
}

func TestIssueWriteUpdateMiss(t *testing.T) {
	store := openIssue(t)
	res, err := NewIssueWrite(store).Execute(context.Background(), mustJSON(t, map[string]any{
		"id":     42,
		"status": "closed",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no issue #42") {
		t.Errorf("output = %s", res.Output)
	}
}
