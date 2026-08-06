package session_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/fault"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

// Chaos: session.sync fail mid-append — Append errors, prior complete lines
// remain loadable, no silent garbage.
func TestChaosSessionSyncFailKeepsPriorEventsLoadable(t *testing.T) {
	t.Cleanup(fault.Reset)
	dir := t.TempDir()
	st, err := session.Open(dir, "chaos-sync")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(protocol.UserMessage{Text: "kept-before-fault"}); err != nil {
		t.Fatal(err)
	}
	path := st.Path()

	disarm := fault.Arm(fault.SessionSync, 1, nil)
	t.Cleanup(disarm)
	err = st.Append(protocol.TextDelta{Text: "should-not-persist-durably"})
	if err == nil {
		t.Fatal("expected Append to fail under session.sync fault")
	}
	if !errors.Is(err, fault.Err) && !strings.Contains(err.Error(), "fault injected") {
		// fsyncLocked wraps the fault; accept either Is or message.
		if !strings.Contains(err.Error(), string(fault.SessionSync)) && !strings.Contains(err.Error(), "session sync") {
			t.Fatalf("err = %v, want fault/session sync", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := session.Replay(path)
	if err != nil {
		t.Fatalf("Replay after sync fault: %v (must stay loadable or CorruptError)", err)
	}
	if len(got) < 1 {
		t.Fatal("expected at least the pre-fault event")
	}
	um, ok := got[0].(protocol.UserMessage)
	if !ok || um.Text != "kept-before-fault" {
		t.Fatalf("first event = %#v", got[0])
	}
	// Write may have completed before fsync failed — the line can appear on
	// Replay. Safe outcome is loadable JSONL (no CorruptError / panic), and
	// the caller saw Append error so they can stop compounding writes.
	for _, ev := range got {
		if !jsonRoundTripOK(ev) {
			t.Fatalf("replayed garbage event: %#v", ev)
		}
	}
}

func jsonRoundTripOK(ev protocol.Event) bool {
	env, err := protocol.Wrap(ev)
	if err != nil {
		return false
	}
	_, err = env.Decode()
	return err == nil
}

// Chaos: session.log_truncate — trailing partial line is skipped; interior
// corruption fails closed with CorruptError (never silent garbage).
func TestChaosSessionLogTruncateOutcomes(t *testing.T) {
	t.Cleanup(fault.Reset)
	dir := t.TempDir()

	t.Run("trailing_partial_skips", func(t *testing.T) {
		st, err := session.Open(dir, "chaos-trunc-tail")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Append(protocol.UserMessage{Text: "complete"}); err != nil {
			t.Fatal(err)
		}
		path := st.Path()
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"type":"text.delta","time":"2020-01-01T00:00:00Z","data":{"text":"torn`); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()

		got, err := session.Replay(path)
		if err != nil {
			t.Fatalf("trailing partial must not fail Replay: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("events = %d, want 1 complete", len(got))
		}
	})

	t.Run("interior_corrupt_errors", func(t *testing.T) {
		path := filepath.Join(dir, "chaos-trunc-mid.jsonl")
		body := "" +
			`{"type":"user.message","time":"2020-01-01T00:00:00Z","data":{"text":"a"}}` + "\n" +
			`NOT-JSON` + "\n" +
			`{"type":"text.delta","time":"2020-01-01T00:00:01Z","data":{"text":"b"}}` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := session.Replay(path)
		if err == nil {
			t.Fatal("expected CorruptError")
		}
		var ce *session.CorruptError
		if !errors.As(err, &ce) {
			t.Fatalf("err type %T: %v", err, err)
		}
		if ce.Line != 2 {
			t.Fatalf("line = %d, want 2", ce.Line)
		}
	})

	t.Run("hard_truncate_prefix_loadable", func(t *testing.T) {
		st, err := session.Open(dir, "chaos-trunc-hard")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Append(protocol.UserMessage{Text: "one"}); err != nil {
			t.Fatal(err)
		}
		if err := st.Append(protocol.UserMessage{Text: "two"}); err != nil {
			t.Fatal(err)
		}
		path := st.Path()
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Keep only the first complete line (header) + first event line.
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(lines) < 3 {
			t.Fatalf("fixture lines = %d", len(lines))
		}
		prefix := lines[0] + "\n" + lines[1] + "\n"
		if err := os.WriteFile(path, []byte(prefix), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := session.Replay(path)
		if err != nil {
			t.Fatalf("prefix truncate must stay loadable: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("events = %d, want 1", len(got))
		}
	})
}

// Chaos: sync fault error text must not echo secret-shaped payloads from the
// event being written.
func TestChaosSessionSyncFailNoSecretInError(t *testing.T) {
	t.Cleanup(fault.Reset)
	dir := t.TempDir()
	st, err := session.Open(dir, "chaos-secret")
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	disarm := fault.Arm(fault.SessionSync, 1, nil)
	t.Cleanup(disarm)
	err = st.Append(protocol.UserMessage{Text: "token " + secret})
	if err == nil {
		// Header already synced; first event append may be the fault hit.
		// If Open's header consumed the arm, re-arm and try again.
		disarm()
		disarm = fault.Arm(fault.SessionSync, 1, nil)
		t.Cleanup(disarm)
		err = st.Append(protocol.UserMessage{Text: "token " + secret})
	}
	if err == nil {
		t.Fatal("expected sync fault")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
	_ = st.Close()
}
