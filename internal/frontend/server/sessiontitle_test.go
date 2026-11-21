package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

// titleRecord builds a JSONL session log from events; extra raw lines are
// appended verbatim (malformed-line cases).
func titleRecord(t *testing.T, evs []protocol.Event, extra ...string) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"session.header","data":{"schemaVersion":1}}` + "\n")
	for _, ev := range evs {
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
	for _, l := range extra {
		b.WriteString(l + "\n")
	}
	return []byte(b.String())
}

func TestProposeSessionTitle(t *testing.T) {
	own := protocol.Correlation{SessionID: "s1"}
	child := protocol.Correlation{SessionID: "child", ParentSessionID: "s1", Depth: 1}
	user := func(c protocol.Correlation, text string) protocol.Event {
		return protocol.UserMessage{Correlation: c, Text: text}
	}
	titled := func(title string) protocol.Event {
		return protocol.SessionTitled{Correlation: own, Title: title}
	}
	tests := []struct {
		name          string
		current       string
		evs           []protocol.Event
		extra         []string
		wantCode      string
		wantTitle     string
		wantManual    bool
		wantUnchanged bool
		wantMessages  int
	}{
		{
			name:     "empty session",
			evs:      nil,
			wantCode: titleCodeEmpty,
		},
		{
			name:         "single message",
			current:      "fix the login bug",
			evs:          []protocol.Event{user(own, "fix the login bug"), titled("fix the login bug")},
			wantCode:     titleCodeSingle,
			wantMessages: 1,
		},
		{
			name:    "proposal comes from the current conversation, not the first prompt",
			current: "fix the login bug",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "now refactor the session store into packages"),
			},
			wantTitle:    "now refactor the session store i",
			wantMessages: 2,
		},
		{
			name:    "short replies are skipped for the newest substantive message",
			current: "fix the login bug",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "migrate auth to oauth"),
				user(own, "yes"),
				user(own, "go on"),
			},
			wantTitle:    "migrate auth to oauth",
			wantMessages: 4,
		},
		{
			name:         "all short falls back to the newest",
			current:      "hi",
			evs:          []protocol.Event{user(own, "hi"), titled("hi"), user(own, "ok")},
			wantTitle:    "ok",
			wantMessages: 2,
		},
		{
			name:    "manual title is flagged",
			current: "My hand-picked name",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "write the release notes now"),
			},
			wantTitle:    "write the release notes now",
			wantManual:   true,
			wantMessages: 2,
		},
		{
			name:    "a previously regenerated title is not manual",
			current: "write the release notes now",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "write the release notes now"),
				user(own, "and tag the release candidate"),
			},
			wantTitle:    "and tag the release candidate",
			wantMessages: 3,
		},
		{
			name:    "unchanged proposal",
			current: "write the release notes now",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "write the release notes now"),
			},
			wantTitle:     "write the release notes now",
			wantUnchanged: true,
			wantMessages:  2,
		},
		{
			name:    "child session messages are ignored",
			current: "fix the login bug",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "review the auth module please"),
				user(child, "child task about something else entirely"),
			},
			wantTitle:    "review the auth module please",
			wantMessages: 2,
		},
		{
			name:    "malformed lines are skipped",
			current: "fix the login bug",
			evs: []protocol.Event{
				user(own, "fix the login bug"), titled("fix the login bug"),
				user(own, "rewrite the parser in go"),
			},
			extra:        []string{`{not json`, `{"type":"no.such.event","data":{}}`},
			wantTitle:    "rewrite the parser in go",
			wantMessages: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, code := proposeSessionTitle("s1", tt.current, titleRecord(t, tt.evs, tt.extra...))
			if code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
			if p.Messages != tt.wantMessages {
				t.Errorf("messages = %d, want %d", p.Messages, tt.wantMessages)
			}
			if code != "" {
				if p.Title != "" {
					t.Errorf("refused proposal carried a title %q", p.Title)
				}
				return
			}
			if p.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", p.Title, tt.wantTitle)
			}
			if p.Manual != tt.wantManual {
				t.Errorf("manual = %v, want %v", p.Manual, tt.wantManual)
			}
			if p.Unchanged != tt.wantUnchanged {
				t.Errorf("unchanged = %v, want %v", p.Unchanged, tt.wantUnchanged)
			}
			if p.Current != tt.current {
				t.Errorf("current = %q, want %q", p.Current, tt.current)
			}
		})
	}
}

func TestProposeSessionTitleRedactsSecrets(t *testing.T) {
	own := protocol.Correlation{SessionID: "s1"}
	raw := titleRecord(t, []protocol.Event{
		protocol.UserMessage{Correlation: own, Text: "hello there"},
		protocol.UserMessage{Correlation: own, Text: reportSecret + " use this key now"},
	})
	p, code := proposeSessionTitle("s1", "hello there", raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if strings.Contains(p.Title, "REPORTSECRET") {
		t.Fatalf("proposal leaked secret: %q", p.Title)
	}
}

// renameCountingSessions fails the test if the propose route ever renames.
type renameCountingSessions struct {
	reportSessions
	renames *int
}

func (r renameCountingSessions) Rename(string, string) (host.Session, error) {
	*r.renames++
	return host.Session{}, nil
}

func TestSessionTitleProposeRoute(t *testing.T) {
	own := protocol.Correlation{SessionID: "s1"}
	drifted := titleRecord(t, []protocol.Event{
		protocol.UserMessage{Correlation: own, Text: "fix the login bug"},
		protocol.SessionTitled{Correlation: own, Title: "fix the login bug"},
		protocol.UserMessage{Correlation: own, Text: "split the session store"},
	})
	single := titleRecord(t, []protocol.Event{
		protocol.UserMessage{Correlation: own, Text: "fix the login bug"},
	})
	tests := []struct {
		name      string
		path      string
		raw       []byte
		wantCode  int
		wantErr   string
		wantTitle string
	}{
		{name: "proposal", path: "/v1/sessions/s1/title/propose", raw: drifted, wantCode: http.StatusOK, wantTitle: "split the session store"},
		{name: "single message refused", path: "/v1/sessions/s1/title/propose", raw: single, wantCode: http.StatusUnprocessableEntity, wantErr: titleCodeSingle},
		{name: "empty refused", path: "/v1/sessions/s1/title/propose", raw: titleRecord(t, nil), wantCode: http.StatusUnprocessableEntity, wantErr: titleCodeEmpty},
		{name: "unknown session", path: "/v1/sessions/nope/title/propose", raw: drifted, wantCode: http.StatusNotFound, wantErr: protocol.ErrorCodeSessionNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renames := 0
			sessions := renameCountingSessions{
				reportSessions: reportSessions{sess: host.Session{ID: "s1", Title: "fix the login bug"}, raw: tt.raw},
				renames:        &renames,
			}
			srv, err := New(Options{Services: &host.Services{Sessions: sessions}})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if renames != 0 {
				t.Fatalf("propose renamed the session %d time(s); only a confirmed PATCH may", renames)
			}
			if tt.wantErr != "" {
				var body opErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != tt.wantErr {
					t.Fatalf("code = %q, want %q", body.Code, tt.wantErr)
				}
				return
			}
			var p titleProposal
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Title != tt.wantTitle || p.Current != "fix the login bug" || p.Manual {
				t.Fatalf("proposal = %+v", p)
			}
		})
	}
}

func TestSessionTitleProposeNeedsSessions(t *testing.T) {
	srv, err := New(Options{Services: &host.Services{}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/s1/title/propose", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d without a sessions service", rec.Code)
	}
}
