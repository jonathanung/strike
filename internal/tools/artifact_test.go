package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/artifact"
)

func artifactTC(dir, session, root string) *tool.Context {
	return &tool.Context{
		WorkDir:       dir,
		SessionID:     session,
		RootSessionID: root,
		Ask:           func(ctx context.Context, req tool.AskRequest) error { return nil },
	}
}

func TestArtifactWriteCreateReadList(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var notified []string
	tc := artifactTC(dir, "sess-1", "root-1")
	tc.NotifyArtifact = func(op string, payload any) {
		a := payload.(artifact.Artifact)
		notified = append(notified, op+":"+a.Type)
	}

	w := NewArtifactWrite(store)
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"create",
		"type":"findings",
		"title":"Review",
		"content":"[\"risk\"]"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "findings") {
		t.Fatalf("title = %q", res.Title)
	}
	if len(notified) != 1 || notified[0] != "create:findings" {
		t.Fatalf("notify = %#v", notified)
	}

	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Version != 1 || created.Type != "findings" {
		t.Fatalf("created = %#v", created)
	}

	// Second type for e2e three-type coverage with patch + test_report below.
	for _, typ := range []string{"patch", "test_report"} {
		_, err := w.Execute(context.Background(), json.RawMessage(`{
			"action":"create","type":"`+typ+`","content":"body-`+typ+`"
		}`), tc)
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}

	r := NewArtifactRead(store)
	got, err := r.Execute(context.Background(), json.RawMessage(`{"id":"`+created.ID+`"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Output, "risk") {
		t.Fatalf("get output = %s", got.Output)
	}

	list, err := r.Execute(context.Background(), json.RawMessage(`{"type":"findings"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Title, "1 artifacts") {
		t.Fatalf("list title = %q", list.Title)
	}
}

func TestArtifactWriteCASConflict(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := artifactTC(dir, "s", "r")
	w := NewArtifactWrite(store)

	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"patch","content":"v1"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal([]byte(res.Output), &created)

	_, err = w.Execute(context.Background(), json.RawMessage(`{
		"action":"update","id":"`+created.ID+`","expected_version":1,"content":"v2"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}

	// Stale version → soft conflict result (not hard error).
	stale, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"update","id":"`+created.ID+`","expected_version":1,"content":"stale"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stale.Title, "conflict") {
		t.Fatalf("title = %q out=%s", stale.Title, stale.Output)
	}
	var view struct {
		Conflict bool   `json:"conflict"`
		Version  int    `json:"version"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(stale.Output), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Conflict || view.Version != 2 || view.Content != "v2" {
		t.Fatalf("conflict view = %#v", view)
	}
}

func TestArtifactWriteTypeValidation(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := artifactTC(dir, "s", "r")
	w := NewArtifactWrite(store)
	_, err = w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"nope","content":"x"
	}`), tc)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("want unknown type err, got %v", err)
	}
}

func TestArtifactPermissionsAsk(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := artifactTC(dir, "s", "r")
	tc.Ask = func(ctx context.Context, req tool.AskRequest) error {
		if req.Permission != "artifact_write" {
			t.Fatalf("perm = %q", req.Permission)
		}
		return errors.New("denied by user")
	}
	w := NewArtifactWrite(store)
	_, err = w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"findings","content":"x"
	}`), tc)
	if err == nil || !strings.Contains(err.Error(), "denied by user") {
		t.Fatalf("want ask denial, got %v", err)
	}

	// Owner-only: peer under same root denied by store.
	tc.Ask = func(ctx context.Context, req tool.AskRequest) error { return nil }
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"contract","content":"secret","access":"owner"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(res.Output), &created)

	peer := artifactTC(dir, "peer", "r")
	r := NewArtifactRead(store)
	out, err := r.Execute(context.Background(), json.RawMessage(`{"id":"`+created.ID+`"}`), peer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "denied") && !strings.Contains(out.Title, "denied") {
		t.Fatalf("peer read = title=%q out=%s", out.Title, out.Output)
	}
}

func TestArtifactSessionScope(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := artifactTC(dir, "sess-a", "root")
	w := NewArtifactWrite(store)
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"test_report","content":"{}","scope":"session"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Scope     string `json:"scope"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}
	if created.Scope != "session" || created.SessionID != "sess-a" {
		t.Fatalf("session fields = %#v", created)
	}
}

func TestArtifactReadMiss(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r := NewArtifactRead(store)
	res, err := r.Execute(context.Background(), json.RawMessage(`{"id":"missing"}`), artifactTC(dir, "s", "r"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "artifact miss" {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestArtifactWriteRequiresSession(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	w := NewArtifactWrite(store)
	_, err = w.Execute(context.Background(), json.RawMessage(`{
		"action":"create","type":"findings","content":"x"
	}`), &tool.Context{
		WorkDir: dir,
		Ask:     func(ctx context.Context, req tool.AskRequest) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "session identity") {
		t.Fatalf("want session identity err, got %v", err)
	}
}
