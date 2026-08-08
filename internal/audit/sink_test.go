package audit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/audit"
	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

func TestRecordPermissionRedacted(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.Observe(protocol.PermissionDecided{
		Correlation: protocol.Correlation{SessionID: "sess1", TurnID: "t1"},
		Permission:  "bash",
		Patterns:    []string{"OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv"},
		Action:      "deny",
		Layer:       "config",
		RulePattern: "Bearer supersecrettokenvalue",
		RuleAction:  "deny",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read segment
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("no segment")
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	if strings.Contains(raw, "sk-abcdefghijklmnopqrstuv") || strings.Contains(raw, "supersecrettokenvalue") {
		t.Fatalf("secret leaked: %s", raw)
	}
	if !strings.Contains(raw, `"family":"permission"`) {
		t.Fatalf("missing family: %s", raw)
	}
	if !strings.Contains(raw, "REDACTED") && !strings.Contains(raw, redact.Placeholder) {
		// may use specific placeholders
		if !strings.Contains(raw, "[REDACTED") {
			t.Fatalf("expected redaction marker: %s", raw)
		}
	}
}

func TestRetentionPrunesOld(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s, err := audit.Open(audit.Options{
		Dir:   dir,
		Clock: clock,
		Retention: audit.Retention{
			MaxEvents: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Record(audit.FamilyPermission, "s", "t", "", "", map[string]any{
			"permission": "bash",
			"action":     "deny",
			"n":          i,
		}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen and export
	s2, err := audit.Open(audit.Options{Dir: dir, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle.json")
	if err := s2.ExportBundle(out); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Count   int `json:"count"`
		Records []struct {
			Family string `json:"family"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Count != 2 {
		t.Fatalf("count = %d, want 2 after retention", bundle.Count)
	}
}

func TestExportNotTranscript(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// User message style content must not appear via Observe
	_ = s.Observe(protocol.UserMessage{Text: "full conversational payload with secrets"})
	_ = s.Record(audit.FamilyEgress, "s", "t", "c", "", map[string]any{
		"tool":        "webfetch",
		"action":      "allow",
		"destination": "example.com",
	})
	out := filepath.Join(t.TempDir(), "b.json")
	if err := s.ExportBundle(out); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	data, _ := os.ReadFile(out)
	if strings.Contains(string(data), "full conversational") {
		t.Fatal("transcript leaked into audit export")
	}
	if !strings.Contains(string(data), `"redacted": true`) && !strings.Contains(string(data), `"redacted":true`) {
		t.Fatalf("missing redacted flag: %s", data)
	}
}

func TestAgeRetention(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	s, err := audit.Open(audit.Options{
		Dir:   dir,
		Clock: func() time.Time { return now },
		Retention: audit.Retention{
			MaxAge: 24 * time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(audit.FamilySandbox, "s", "", "", "", map[string]any{"mode": "read-only", "errorCode": "sandbox_denied"}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(48 * time.Hour)
	if err := s.Record(audit.FamilySandbox, "s", "", "", "", map[string]any{"mode": "workspace-write"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _ := audit.Open(audit.Options{Dir: dir, Clock: func() time.Time { return now }})
	out := filepath.Join(t.TempDir(), "a.json")
	_ = s2.ExportBundle(out)
	_ = s2.Close()
	var bundle struct {
		Count int `json:"count"`
	}
	data, _ := os.ReadFile(out)
	_ = json.Unmarshal(data, &bundle)
	if bundle.Count != 1 {
		t.Fatalf("age prune count = %d, want 1; body=%s", bundle.Count, data)
	}
}

func TestObserveAllFamilies(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1"}

	// permission + toolchain_match
	if err := s.Observe(protocol.PermissionDecided{
		Correlation:  corr,
		Permission:   "bash",
		Patterns:     []string{"rm -rf /"},
		Action:       "deny",
		ChainID:      "ch1",
		ChainRule:    "write_exec_bash",
		ChainSummary: "write then bash",
	}); err != nil {
		t.Fatal(err)
	}
	// content_guard via permission
	if err := s.Observe(protocol.PermissionDecided{
		Correlation: corr,
		Permission:  "content_guard",
		Action:      "deny",
		Patterns:    []string{"secret.env"},
	}); err != nil {
		t.Fatal(err)
	}
	// sandbox
	if err := s.Observe(protocol.ToolCallEnd{
		Correlation: corr,
		CallID:      "c1",
		Title:       "bash",
		IsError:     true,
		ErrorCode:   "sandbox_denied",
		Output:      "Read-only file system",
	}); err != nil {
		t.Fatal(err)
	}
	// egress
	if err := s.Observe(protocol.ToolCallEnd{
		Correlation: corr,
		CallID:      "c2",
		Title:       "bash",
		IsError:     true,
		ErrorCode:   "network_denied",
		Output:      `host "evil.com" is not on the network allowlist`,
	}); err != nil {
		t.Fatal(err)
	}
	// content_guard tool end
	if err := s.Observe(protocol.ToolCallEnd{
		Correlation: corr,
		CallID:      "c3",
		Title:       "write",
		IsError:     true,
		ErrorCode:   "content_guard_denied",
		Output:      "credential shape denied",
	}); err != nil {
		t.Fatal(err)
	}
	// hook
	if err := s.Observe(protocol.HookMatched{
		Correlation: corr,
		Event:       "pre_tool_use",
		Action:      "shell_fail_closed",
		Tool:        "bash",
		Message:     "hook timed out",
		CallID:      "c4",
	}); err != nil {
		t.Fatal(err)
	}
	// admission
	if err := s.Observe(protocol.SchedulerQueued{
		Correlation: corr,
		Pools:       []string{"process"},
	}); err != nil {
		t.Fatal(err)
	}
	// secret_ref_use (direct production emitter)
	if err := s.RecordSecretRefUse("s1", "t1", "c5", "env", "abcd1234", "inject", "bash"); err != nil {
		t.Fatal(err)
	}
	// serve_op (strike serve control plane — type + IP only)
	if err := s.Record(audit.FamilyServeOp, "s1", "", "", "", map[string]string{
		"opType":   "interrupt",
		"sourceIp": "127.0.0.1",
		"channel":  "http",
		"outcome":  "ok",
	}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "all.json")
	if err := s.ExportBundle(out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	// Must not contain raw secrets
	if strings.Contains(raw, "sk-") || strings.Contains(raw, "supersecret") {
		t.Fatalf("secret leaked: %s", raw)
	}
	wantFamilies := map[string]bool{
		"permission":      false,
		"toolchain_match": false,
		"content_guard":   false,
		"sandbox":         false,
		"egress":          false,
		"hook":            false,
		"admission":       false,
		"secret_ref_use":  false,
		"serve_op":        false,
	}
	var bundle struct {
		Records []struct {
			Family string `json:"family"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	for _, r := range bundle.Records {
		if _, ok := wantFamilies[r.Family]; ok {
			wantFamilies[r.Family] = true
		}
	}
	for fam, ok := range wantFamilies {
		if !ok {
			t.Errorf("missing family %q in export (%d records)", fam, len(bundle.Records))
		}
	}
	// Documented Families list matches emitters
	for _, f := range audit.Families {
		if _, ok := wantFamilies[f]; !ok {
			t.Errorf("Families lists %q but test map incomplete", f)
		}
	}
}

func TestObserveIgnoresTranscript(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Observe(protocol.UserMessage{Text: "hello agent with OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv"})
	_ = s.Observe(protocol.TextDelta{Text: "model reply"})
	out := filepath.Join(t.TempDir(), "empty.json")
	if err := s.ExportBundle(out); err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Count int `json:"count"`
	}
	data, _ := os.ReadFile(out)
	_ = json.Unmarshal(data, &bundle)
	if bundle.Count != 0 {
		t.Fatalf("count = %d, want 0 for non-security events", bundle.Count)
	}
}
