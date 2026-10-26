package usageaudit

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

func TestChainVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	log.SetClock(func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) })

	first, err := log.Append(Input{Action: ActionApproval, SessionID: "s1", Summary: "allow once bash"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || first.Prev != GenesisPrev || first.Hash == "" {
		t.Fatalf("first = %+v", first)
	}
	if _, err := log.Append(Input{Action: ActionConfig, Summary: "settings updated: model"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "chain.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), "allow once bash", "allow once rm", 1)
	if tampered == string(raw) {
		t.Fatal("summary was not in the chain file")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	err = log.Verify()
	if err == nil {
		t.Fatal("verify succeeded after the summary was edited")
	}
	var ve *VerifyError
	if !asVerify(err, &ve) || ve.Seq != 1 {
		t.Fatalf("verify error = %v, want seq 1", err)
	}
}

func TestChainVerifyDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if _, err := log.Append(Input{Action: ActionToken, Summary: "daemon token file rotated"}); err != nil {
		t.Fatal(err)
	}
	second, err := log.Append(Input{Action: ActionTerminal, Summary: "terminal opened"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(cut) != 2 {
		t.Fatalf("lines = %d", len(cut))
	}
	if err := os.WriteFile(filepath.Join(dir, "chain.jsonl"), []byte(cut[0]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = log.Verify()
	if err == nil {
		t.Fatal("verify succeeded after the last line was deleted")
	}
	if !strings.Contains(err.Error(), "tip") {
		t.Fatalf("error = %v, want a tip mismatch (deleted seq %d)", err, second.Seq)
	}
	if _, err := log.Append(Input{Action: ActionConfig, Summary: "should not heal the chain"}); err == nil {
		t.Fatal("append healed a truncated chain")
	}
	if err := log.Verify(); err == nil || !strings.Contains(err.Error(), "tip") {
		t.Fatalf("verify after refused append = %v", err)
	}
}

func TestVerifyMissingChainFileKeepsTheTip(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if _, err := log.Append(Input{Action: ActionToken, Summary: "daemon token file rotated"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "chain.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := log.Verify(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("verify = %v", err)
	}
	if _, err := log.Append(Input{Action: ActionConfig, Summary: "should not start a new chain"}); err == nil {
		t.Fatal("append recreated a chain over a leftover tip")
	}
	if err := log.Verify(); err == nil {
		t.Fatal("verify succeeded after the chain file was removed")
	}
}

func TestChainRedactsBeforeHash(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	secret := "sk-abcdefghijklmnopqrstuvwxyz"
	entry, err := log.Append(Input{
		Action:  ActionShell,
		Summary: "bash " + secret,
		Detail:  map[string]string{"command": "echo " + secret, "ok": "plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(entry.Summary, secret) || strings.Contains(string(entry.Detail), secret) {
		t.Fatalf("stored entry kept the secret: %+v", entry)
	}
	if !strings.Contains(entry.Summary, redact.Placeholder) && !strings.Contains(entry.Summary, "REDACTED") {
		t.Fatalf("summary = %q, want a redaction marker", entry.Summary)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("chain file contains the raw secret")
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
	exp, err := log.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Redacted || !exp.Verified || len(exp.Entries) != 1 {
		t.Fatalf("export = %+v", exp)
	}
}

func TestChainConcurrentAppendVerifies(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := log.Append(Input{Action: ActionConfig, Summary: "edit " + string(rune('a'+i))})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, err := log.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 8 {
		t.Fatalf("entries = %d", len(entries))
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestNoteEventRedactsShellAndSkipsUnrelated(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	NoteEvent(protocol.TextDelta{Text: "hello"})
	NoteEvent(protocol.PermissionDecided{
		Correlation: protocol.Correlation{SessionID: "root"},
		Permission:  "bash",
		Action:      "allow",
		Decision:    protocol.DecisionOnce,
		Patterns:    []string{"echo sk-abcdefghijklmnopqrstuvwxyz"},
	})
	NoteEvent(protocol.ToolCallBegin{
		Correlation: protocol.Correlation{SessionID: "root"},
		CallID:      "c1",
		Name:        "bash",
		Args:        []byte(`{"command":"echo sk-abcdefghijklmnopqrstuvwxyz"}`),
	})
	NoteEvent(protocol.ProcessStarted{
		Correlation: protocol.Correlation{SessionID: "root"},
		CallID:      "c1",
		Argv:        []string{"echo", "sk-abcdefghijklmnopqrstuvwxyz"},
	})
	entries, err := log.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want approval + shell (process with callId skipped)", len(entries))
	}
	if entries[0].Action != ActionApproval || entries[1].Action != ActionShell {
		t.Fatalf("actions = %s %s", entries[0].Action, entries[1].Action)
	}
	blob := entries[0].Summary + string(entries[0].Detail) + entries[1].Summary + string(entries[1].Detail)
	if strings.Contains(blob, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("event chain kept a secret: %s", blob)
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetRejectsNegative(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if err := log.SetDailyBudget(-1); err == nil {
		t.Fatal("negative budget accepted")
	}
	usd, set, err := log.DailyBudget()
	if err != nil || set || usd != 0 {
		t.Fatalf("budget = %v %v %v", usd, set, err)
	}
	if err := log.SetDailyBudget(2.5); err != nil {
		t.Fatal(err)
	}
	usd, set, err = log.DailyBudget()
	if err != nil || !set || usd != 2.5 {
		t.Fatalf("budget = %v %v %v", usd, set, err)
	}
}

func asVerify(err error, dst **VerifyError) bool {
	ve, ok := err.(*VerifyError)
	if ok {
		*dst = ve
	}
	return ok
}
