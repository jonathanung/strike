package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

func rewindEvents() []protocol.Event {
	root := protocol.Correlation{SessionID: "s1"}
	child := protocol.Correlation{SessionID: "kid", ParentSessionID: "s1", Depth: 1}
	return []protocol.Event{
		protocol.UserMessage{Correlation: root, Text: "add login"},
		protocol.TurnStarted{Correlation: root},
		protocol.TurnCompleted{Correlation: root, StopReason: "end_turn", Files: []protocol.TurnFileChange{
			{Path: "auth/login.go", Kind: "create"},
		}},
		protocol.UserMessage{Correlation: root, Text: "add tests"},
		protocol.UserMessage{Correlation: child, Text: "child prompt"},
		protocol.TurnCompleted{Correlation: child, Files: []protocol.TurnFileChange{{Path: "child.go", Kind: "create"}}},
		protocol.TurnCompleted{Correlation: root, StopReason: "end_turn", Files: []protocol.TurnFileChange{
			{Path: "auth/login_test.go", Kind: "create"}, {Path: "auth/login.go", Kind: "update"},
		}, CheckpointSkipped: 1},
		protocol.UserMessage{Correlation: root, Text: "run the migration script"},
		protocol.TurnCompleted{Correlation: root, StopReason: "end_turn", Files: []protocol.TurnFileChange{
			{Path: "db/schema.sql", Kind: "update"},
		}, Uncovered: []string{"bash"}},
	}
}

func TestBuildRewindPreview(t *testing.T) {
	evs := rewindEvents()
	points := protocol.RewindPoints(evs)
	if len(points) != 3 {
		t.Fatalf("points = %+v", points)
	}
	tests := []struct {
		name          string
		keep          int
		wantTurn      int
		wantTurns     int
		wantPrompt    string
		wantFiles     []string
		wantSkipped   int
		wantUncovered []string
	}{
		{
			name: "last turn only", keep: points[2].KeepEvents,
			wantTurn: 3, wantTurns: 1, wantPrompt: "run the migration script",
			wantFiles: []string{"db/schema.sql:update"}, wantUncovered: []string{"bash"},
		},
		{
			name: "middle turn undoes it and everything after", keep: points[1].KeepEvents,
			wantTurn: 2, wantTurns: 2, wantPrompt: "add tests",
			wantFiles:   []string{"auth/login.go:update", "auth/login_test.go:create", "db/schema.sql:update"},
			wantSkipped: 1, wantUncovered: []string{"bash"},
		},
		{
			name: "first turn: newest kind wins per path, child files excluded", keep: points[0].KeepEvents,
			wantTurn: 1, wantTurns: 3, wantPrompt: "add login",
			wantFiles:   []string{"auth/login.go:update", "auth/login_test.go:create", "db/schema.sql:update"},
			wantSkipped: 1, wantUncovered: []string{"bash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := buildRewindPreview(evs, tt.keep)
			if !ok {
				t.Fatal("preview not found")
			}
			if p.Turn != tt.wantTurn || p.Turns != tt.wantTurns || p.TotalTurns != 3 || p.Prompt != tt.wantPrompt {
				t.Fatalf("preview = %+v", p)
			}
			var files []string
			for _, f := range p.Files {
				files = append(files, f.Path+":"+f.Kind)
			}
			if strings.Join(files, ",") != strings.Join(tt.wantFiles, ",") {
				t.Fatalf("files = %v, want %v", files, tt.wantFiles)
			}
			if p.CheckpointSkipped != tt.wantSkipped {
				t.Fatalf("skipped = %d, want %d", p.CheckpointSkipped, tt.wantSkipped)
			}
			if strings.Join(p.Uncovered, ",") != strings.Join(tt.wantUncovered, ",") {
				t.Fatalf("uncovered = %v", p.Uncovered)
			}
			if p.BeyondCheckpoints {
				t.Fatal("beyond checkpoints on a 3-turn session")
			}
		})
	}
}

func TestBuildRewindPreviewAfterRewindAndUnknownKeep(t *testing.T) {
	evs := rewindEvents()
	last := protocol.RewindPoints(evs)[2].KeepEvents
	evs = append(evs, protocol.SessionRewound{Correlation: protocol.Correlation{SessionID: "s1"}, Removed: 2})
	if _, ok := buildRewindPreview(evs, last); ok {
		t.Fatal("a rewound turn must not be previewable")
	}
	p, ok := buildRewindPreview(evs, protocol.RewindPoints(evs)[1].KeepEvents)
	if !ok || p.Turns != 1 || p.TotalTurns != 2 {
		t.Fatalf("after rewind = %+v ok=%v", p, ok)
	}
	if _, ok := buildRewindPreview(evs, 999); ok {
		t.Fatal("unknown keep must not match")
	}
}

func TestBuildRewindPreviewBeyondCheckpoints(t *testing.T) {
	root := protocol.Correlation{SessionID: "s1"}
	var evs []protocol.Event
	for i := 0; i < rewindCheckpointTurns+2; i++ {
		evs = append(evs,
			protocol.UserMessage{Correlation: root, Text: "turn " + strconv.Itoa(i)},
			protocol.TurnCompleted{Correlation: root},
		)
	}
	first := protocol.RewindPoints(evs)[0].KeepEvents
	p, ok := buildRewindPreview(evs, first)
	if !ok || !p.BeyondCheckpoints || p.Turns != rewindCheckpointTurns+2 {
		t.Fatalf("preview = %+v", p)
	}
}

// rewindSessions serves one seeded record with a configurable parent.
type rewindSessions struct {
	reportSessions
}

func rewindRecord(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"session.header","data":{"schemaVersion":1}}` + "\n")
	for _, ev := range rewindEvents() {
		env, err := protocol.Wrap(ev)
		if err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func TestSessionRewindPreviewRoute(t *testing.T) {
	raw := rewindRecord(t)
	keep := protocol.RewindPoints(rewindEvents())[1].KeepEvents
	tests := []struct {
		name         string
		sess         host.Session
		readOnly     bool
		query        string
		wantCode     int
		wantDisabled string
	}{
		{name: "preview", sess: host.Session{ID: "s1"}, query: "keep=" + strconv.Itoa(keep), wantCode: http.StatusOK},
		{name: "read-only states why", sess: host.Session{ID: "s1"}, readOnly: true, query: "keep=" + strconv.Itoa(keep), wantCode: http.StatusOK, wantDisabled: "read-only"},
		{name: "delegated child states why", sess: host.Session{ID: "s1", ParentID: "p"}, query: "keep=" + strconv.Itoa(keep), wantCode: http.StatusOK, wantDisabled: "delegated child"},
		{name: "bad keep", sess: host.Session{ID: "s1"}, query: "keep=abc", wantCode: http.StatusBadRequest},
		{name: "missing keep", sess: host.Session{ID: "s1"}, query: "", wantCode: http.StatusBadRequest},
		{name: "stale keep", sess: host.Session{ID: "s1"}, query: "keep=2", wantCode: http.StatusConflict},
		{name: "unknown session", sess: host.Session{ID: "other"}, query: "keep=" + strconv.Itoa(keep), wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := New(Options{
				ReadOnly: tt.readOnly,
				Services: &host.Services{Sessions: rewindSessions{reportSessions{sess: tt.sess, raw: raw}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/s1/rewind_preview?"+tt.query, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if rec.Code != http.StatusOK {
				return
			}
			var p rewindPreview
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Turn != 2 || p.Turns != 2 || p.Prompt != "add tests" {
				t.Fatalf("preview = %+v", p)
			}
			if tt.wantDisabled == "" && p.Disabled != "" {
				t.Fatalf("unexpected disabled reason %q", p.Disabled)
			}
			if tt.wantDisabled != "" && !strings.Contains(p.Disabled, tt.wantDisabled) {
				t.Fatalf("disabled = %q, want it to mention %q", p.Disabled, tt.wantDisabled)
			}
		})
	}
}
