package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/skillusage"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestSkillUsageCountsRecordedRuns(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	sessions := t.TempDir()
	t.Setenv("HOME", home)
	skills := filepath.Join(home, ".strike", "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"used", "idle"} {
		body := "---\nname: " + name + "\ndescription: seeded\n---\nSay hello.\n"
		if err := os.WriteFile(filepath.Join(skills, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env, err := protocol.Wrap(protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: "s1", TurnID: "t1"},
		Text:        "/used please",
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Time = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	line, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "s1.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := New(Options{Auth: true, Token: "sekret", SessionDir: sessions, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	locked.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/customize/skill-usage", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", res.Code)
	}

	srv, err := New(Options{SessionDir: sessions, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/customize/skill-usage", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	var got skillusage.Report
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Skills["used"].Count != 1 || !got.Skills["used"].LastUsed.Equal(env.Time) {
		t.Fatalf("used = %+v", got.Skills["used"])
	}
	idle, ok := got.Skills["idle"]
	if !ok || idle.Count != 0 {
		t.Fatalf("idle = %+v ok=%v, want listed and never used", idle, ok)
	}
	if got.Sessions != 1 {
		t.Fatalf("sessions = %d", got.Sessions)
	}
}
