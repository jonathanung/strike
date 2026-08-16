package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

func TestReplayTimedAndExportTrace(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(dir, "sess-timeline")
	if err != nil {
		t.Fatal(err)
	}
	corr := protocol.Correlation{SessionID: "sess-timeline", TurnID: "turn-1"}
	secret := "sk-ant-api03-SESSIONEXPORTLEAK99"
	for _, ev := range []protocol.Event{
		protocol.TurnStarted{Correlation: corr},
		protocol.ToolCallBegin{
			Correlation: corr,
			CallID:      "c1",
			Name:        "bash",
			Args:        json.RawMessage(`{"command":"echo ` + secret + `"}`),
		},
		protocol.ToolCallEnd{
			Correlation: corr,
			CallID:      "c1",
			Output:      "OPENAI_API_KEY=sk-proj-nested-from-session-99\n" + secret,
		},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	} {
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
		// Ensure distinct envelope times for duration math.
		time.Sleep(2 * time.Millisecond)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	logPath := session.LogPath(dir, "sess-timeline")
	timed, err := session.ReplayTimed(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(timed) != 4 {
		t.Fatalf("timed len = %d", len(timed))
	}
	for i := 1; i < len(timed); i++ {
		if timed[i].Time.Before(timed[i-1].Time) {
			t.Fatalf("times not ordered: %v then %v", timed[i-1].Time, timed[i].Time)
		}
	}

	out := filepath.Join(dir, "trace.json")
	tr, err := session.ExportTrace(logPath, out, timeline.Options{SessionID: "sess-timeline"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Summary.Turns != 1 || tr.Summary.Tools != 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, banned := range []string{secret, "sk-proj-nested-from-session-99"} {
		if strings.Contains(body, banned) {
			t.Errorf("trace still contains %q", banned)
		}
	}
}

func TestBuildTraceEmptyPath(t *testing.T) {
	if _, err := session.BuildTrace("", timeline.Options{}); err == nil {
		t.Fatal("expected error")
	}
}
