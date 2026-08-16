package tools

import (
	"context"
	"encoding/json"
	"github.com/jonathanung/strike-cli/internal/tool"
	"os"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/ledger"
)

func ledgerTC(dir, session, root string) *tool.Context {
	return &tool.Context{
		WorkDir:       dir,
		SessionID:     session,
		RootSessionID: root,
		MemberName:    "orchestrator",
		Ask:           func(ctx context.Context, req tool.AskRequest) error { return nil },
	}
}

func TestLedgerWriteAppendReadList(t *testing.T) {
	dir := t.TempDir()
	store, err := ledger.Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var notified []string
	tc := ledgerTC(dir, "sess-1", "root-1")
	tc.NotifyLedger = func(op string, payload any) {
		e := payload.(ledger.Entry)
		notified = append(notified, op+":"+e.Kind+":"+e.Status)
	}

	w := NewLedgerWrite(store)
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"append",
		"kind":"assumption",
		"statement":"API X is dead code",
		"confidence":"high",
		"evidence_refs":["artifact:ab12"],
		"scope_paths":["internal/api"]
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "assumption") {
		t.Fatalf("title = %q", res.Title)
	}
	if len(notified) != 1 || !strings.HasPrefix(notified[0], "append:assumption:active") {
		t.Fatalf("notify = %#v", notified)
	}

	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != "active" || created.Kind != "assumption" {
		t.Fatalf("created = %#v", created)
	}

	r := NewLedgerRead(store)
	got, err := r.Execute(context.Background(), json.RawMessage(`{"id":"`+created.ID+`"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Output, "dead code") {
		t.Fatalf("get = %s", got.Output)
	}

	list, err := r.Execute(context.Background(), json.RawMessage(`{"path":"internal/api/x.go"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Title, "1 ledger") {
		t.Fatalf("list title = %q", list.Title)
	}
}

func TestLedgerInvalidateAndSupersede(t *testing.T) {
	dir := t.TempDir()
	store, err := ledger.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := ledgerTC(dir, "s", "r")
	var ops []string
	tc.NotifyLedger = func(op string, payload any) {
		ops = append(ops, op)
	}

	w := NewLedgerWrite(store)
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"append","kind":"decision","statement":"use lib A"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &a); err != nil {
		t.Fatal(err)
	}

	// Supersede
	res2, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"supersede","id":"`+a.ID+`","kind":"decision","statement":"use lib B"
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		ID         string `json:"id"`
		Supersedes string `json:"supersedes"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res2.Output), &b); err != nil {
		t.Fatal(err)
	}
	if b.Supersedes != a.ID || b.Status != "active" {
		t.Fatalf("supersede = %#v", b)
	}

	// Invalidate the new one
	res3, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"invalidate","id":"`+b.ID+`","reason":"lib B yanked","evidence":["advisory:1"]
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res3.Title, "invalidated") {
		t.Fatalf("title = %q", res3.Title)
	}
	var inv struct {
		Status           string `json:"status"`
		InvalidateReason string `json:"invalidate_reason"`
	}
	if err := json.Unmarshal([]byte(res3.Output), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status != "invalidated" || inv.InvalidateReason == "" {
		t.Fatalf("inv = %#v", inv)
	}

	// History still readable
	r := NewLedgerRead(store)
	hist, err := r.Execute(context.Background(), json.RawMessage(`{"active_only":false}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hist.Title, "2 ledger") {
		t.Fatalf("history title = %q out=%s", hist.Title, hist.Output)
	}

	wantOps := []string{"append", "supersede", "invalidate"}
	if len(ops) != 3 {
		t.Fatalf("ops = %#v", ops)
	}
	for i, w := range wantOps {
		if ops[i] != w {
			t.Fatalf("ops[%d]=%s want %s", i, ops[i], w)
		}
	}
}

func TestLedgerReadActiveOnlyDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := ledger.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := ledgerTC(dir, "s", "r")
	w := NewLedgerWrite(store)
	res, _ := w.Execute(context.Background(), json.RawMessage(`{
		"action":"append","kind":"constraint","statement":"no secrets in logs"
	}`), tc)
	var e struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(res.Output), &e)
	_, _ = w.Execute(context.Background(), json.RawMessage(`{
		"action":"invalidate","id":"`+e.ID+`","reason":"done"
	}`), tc)

	r := NewLedgerRead(store)
	list, err := r.Execute(context.Background(), json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Title, "0 ledger") {
		t.Fatalf("default active list = %q", list.Title)
	}
}

func TestLedgerEvidencePinsAndRevalidate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pinned.go"
	if err := os.WriteFile(path, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tc := ledgerTC(dir, "s", "r")
	var ops []string
	tc.NotifyLedger = func(op string, payload any) { ops = append(ops, op) }
	w := NewLedgerWrite(store)
	res, err := w.Execute(context.Background(), json.RawMessage(`{
		"action":"append","kind":"assumption","statement":"pinned file is package p",
		"evidence_pins":[{"kind":"path","path":"pinned.go"}]
	}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID           string `json:"id"`
		Freshness    string `json:"freshness"`
		EvidencePins []struct {
			Hash string `json:"hash"`
			Path string `json:"path"`
		} `json:"evidence_pins"`
	}
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatal(err)
	}
	if created.Freshness != "validated" || len(created.EvidencePins) != 1 || created.EvidencePins[0].Hash == "" {
		t.Fatalf("created = %s", res.Output)
	}
	if err := os.WriteFile(path, []byte("package q\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewLedgerRead(store)
	got, err := r.Execute(context.Background(), json.RawMessage(`{"id":"`+created.ID+`"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Output, `"freshness": "stale"`) {
		t.Fatalf("read after change = %s", got.Output)
	}
	res2, err := w.Execute(context.Background(), json.RawMessage(`{"action":"revalidate","id":"`+created.ID+`"}`), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Output, `"freshness": "validated"`) {
		t.Fatalf("revalidate = %s", res2.Output)
	}
	if len(ops) != 2 || ops[0] != "append" || ops[1] != "revalidate" {
		t.Fatalf("ops = %#v", ops)
	}
}
