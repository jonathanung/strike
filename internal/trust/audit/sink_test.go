package audit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
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
	// script_run (schedule clock + HTTP lifecycle handlers; direct Record)
	if err := s.Record(audit.FamilyScriptRun, "", "", "", "", audit.ScriptRunPayload{
		ScheduleID:  "sched1",
		Status:      "armed",
		Decision:    "armed",
		Command:     "make build",
		CommandHash: "deadbeef",
		Dir:         "/repo",
		ClientIP:    "127.0.0.1",
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
		"script_run":      false,
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

// TestRecordScriptRunRedacted is the S2 negative test from
// docs/design/script-schedules.md: credential-shaped sentinels placed in
// Command, OutputTail and Error never reach the segment file, while the
// correlation (ids, commandHash) and run metadata survive unchanged.
func TestRecordScriptRunRedacted(t *testing.T) {
	const (
		ghToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
		bearer  = "Bearer supersecrettokenvalue123"
	)
	exit := 1
	started := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		payload any // struct or map (both arrive via Sink.Record)
	}{
		{
			name: "struct",
			payload: audit.ScriptRunPayload{
				ScheduleID:  "sched-1",
				RunID:       "run-1",
				Status:      "failed",
				Decision:    "allow",
				Mode:        "bash",
				Sandbox:     "strict/applied",
				Command:     "curl -H 'Authorization: " + bearer + "' https://x " + ghToken,
				CommandHash: "0123456789abcdef",
				Dir:         "/home/u/" + ghToken,
				StartedAt:   started,
				EndedAt:     started.Add(2 * time.Second),
				ExitCode:    &exit,
				Timeout:     "30s",
				OutputTail:  "pushing with " + ghToken + "\nauth: " + bearer,
				Error:       "exit 1: " + bearer + " rejected, token " + ghToken,
			},
		},
		{
			name: "map",
			payload: map[string]any{
				"scheduleId":  "sched-1",
				"runId":       "run-1",
				"status":      "failed",
				"command":     "echo " + ghToken,
				"commandHash": "0123456789abcdef",
				"dir":         "/repo",
				"exitCode":    1,
				"outputTail":  bearer,
				"error":       ghToken,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := audit.Open(audit.Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Record(audit.FamilyScriptRun, "", "", "", "", tc.payload); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("segments = %d, want 1", len(entries))
			}
			data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			raw := string(data)
			for _, leak := range []string{ghToken, "supersecrettokenvalue123"} {
				if strings.Contains(raw, leak) {
					t.Fatalf("sentinel %q leaked into segment: %s", leak, raw)
				}
			}
			var rec struct {
				Family  string                 `json:"family"`
				Payload audit.ScriptRunPayload `json:"payload"`
			}
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatalf("segment line not JSON: %v\n%s", err, raw)
			}
			if rec.Family != audit.FamilyScriptRun {
				t.Fatalf("family = %q, want %q", rec.Family, audit.FamilyScriptRun)
			}
			p := rec.Payload
			if p.ScheduleID != "sched-1" || p.RunID != "run-1" || p.Status != "failed" {
				t.Fatalf("ids/status changed: %+v", p)
			}
			if p.CommandHash != "0123456789abcdef" {
				t.Fatalf("commandHash = %q, want unchanged", p.CommandHash)
			}
			if p.ExitCode == nil || *p.ExitCode != 1 {
				t.Fatalf("exitCode = %v, want 1", p.ExitCode)
			}
			// Scrubbed fields keep their non-secret shape (prefix survives, secret replaced).
			if !strings.HasPrefix(p.Command, "curl") && !strings.HasPrefix(p.Command, "echo") {
				t.Fatalf("command prefix lost: %q", p.Command)
			}
			if !strings.Contains(raw, "[REDACTED") {
				t.Fatalf("expected redaction marker in segment: %s", raw)
			}
			if tc.name == "struct" {
				if !p.StartedAt.Equal(started) || !p.EndedAt.Equal(started.Add(2*time.Second)) {
					t.Fatalf("timestamps changed: %v %v", p.StartedAt, p.EndedAt)
				}
				if p.Mode != "bash" || p.Sandbox != "strict/applied" || p.Timeout != "30s" || p.Decision != "allow" {
					t.Fatalf("metadata changed: %+v", p)
				}
				if !strings.HasPrefix(p.Dir, "/home/u/") {
					t.Fatalf("dir prefix lost: %q", p.Dir)
				}
			}
		})
	}
}

// TestRecordScriptRunLifecycleOmitsZero: lifecycle records (no run) carry no
// runId, timestamps or exit code on disk, and unknown fields are dropped.
func TestRecordScriptRunLifecycleOmitsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(audit.FamilyScriptRun, "", "", "", "", map[string]any{
		"scheduleId":  "sched-2",
		"status":      "deleted",
		"clientIp":    "10.0.0.7",
		"command":     "true",
		"commandHash": "abc",
		"dir":         "/repo",
		"rawBody":     "must not persist",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("segments = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"runId", "startedAt", "endedAt", "exitCode", "outputTail", "error", "rawBody"} {
		if _, ok := rec.Payload[absent]; ok {
			t.Errorf("lifecycle record should omit %q: %s", absent, data)
		}
	}
	for key, want := range map[string]string{"scheduleId": `"sched-2"`, "status": `"deleted"`, "clientIp": `"10.0.0.7"`, "commandHash": `"abc"`} {
		if got := string(rec.Payload[key]); got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
}

// TestRecordScriptRunMalformedPayloadErrors: a payload that does not decode
// into ScriptRunPayload is rejected rather than written truncated.
func TestRecordScriptRunMalformedPayloadErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := audit.Open(audit.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Record(audit.FamilyScriptRun, "", "", "", "", map[string]any{
		"scheduleId": "sched-3",
		"status":     "finished",
		"startedAt":  1763100000, // unix int, not RFC 3339
		"exitCode":   0,
	})
	if err == nil {
		t.Fatal("Record accepted a payload with a non-RFC3339 startedAt")
	}
	if !strings.Contains(err.Error(), "script_run") {
		t.Fatalf("error = %v, want script_run context", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("segment written despite decode error: %v", entries)
	}
}
