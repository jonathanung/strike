package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/product/goal"
	"github.com/jonathanung/strike/internal/protocol"
)

func newGoalServer(t *testing.T) (*Server, *goal.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := goal.Open(dir, "goals-guard")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ops := make(chan protocol.Op, 4)
	live := NewLive("live-goals", dir, nil, ops)
	t.Cleanup(func() { live.Close() })
	srv, err := New(Options{
		SessionDir: dir,
		Live:       live,
		Services:   &host.Services{Goals: local.NewGoals(store, dir)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, store
}

func postGoal(t *testing.T, srv *Server, body string) (int, goalDTO) {
	t.Helper()
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	var g goalDTO
	if res.Code < 300 {
		if err := json.Unmarshal(res.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
	}
	return res.Code, g
}

func waitGoal(t *testing.T, srv *Server, id, status string) goalDTO {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last goalDTO
	for time.Now().Before(deadline) {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/goals/"+id, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("get = %d %s", res.Code, res.Body.String())
		}
		if err := json.Unmarshal(res.Body.Bytes(), &last); err != nil {
			t.Fatal(err)
		}
		if last.Status == status {
			return last
		}
		if last.Status == "done" || last.Status == "failed" || last.Status == "aborted" || last.Status == "blocked" {
			if last.Status != status {
				t.Fatalf("stopped at %s (%s), want %s", last.Status, last.FailReason, status)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out in %s (%s), want %s", last.Status, last.FailReason, status)
	return last
}

// A pause issued right after a background start must stick: the goal
// ends paused (never reversed back into a run by the start path) and
// stays paused.
func TestGoalsBackgroundStartPauseSticks(t *testing.T) {
	srv, _ := newGoalServer(t)

	code, created := postGoal(t, srv, `{
		"description":"slow check",
		"verification":"exec sleep 30",
		"maxIterations":5,
		"maxNoProgressIters":5,
		"maxVerifyFailures":5,
		"maxWallClockS":120
	}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	run := httptest.NewRecorder()
	srv.Handler().ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/run?background=1", nil))
	if run.Code != http.StatusAccepted {
		t.Fatalf("background = %d %s", run.Code, run.Body.String())
	}
	// A pause that lands before the background goroutine registers its
	// run is rejected (the goal is not active yet) rather than silently
	// reversed, so retry briefly like a user would; once accepted, it
	// must stick.
	pauseDeadline := time.Now().Add(3 * time.Second)
	for {
		pause := httptest.NewRecorder()
		srv.Handler().ServeHTTP(pause, httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/pause", nil))
		if pause.Code == http.StatusOK {
			break
		}
		if time.Now().After(pauseDeadline) {
			t.Fatalf("pause never accepted: %d %s", pause.Code, pause.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	paused := waitGoal(t, srv, created.ID, "paused")
	if paused.LastIteration != 0 {
		t.Fatalf("paused=%+v, want no committed iterations", paused)
	}
	time.Sleep(300 * time.Millisecond)
	again := waitGoal(t, srv, created.ID, "paused")
	if again.Status != "paused" {
		t.Fatalf("status moved to %s after pause", again.Status)
	}
	// Leave no 30s check running behind the test.
	abort := httptest.NewRecorder()
	srv.Handler().ServeHTTP(abort, httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/abort", nil))
	if abort.Code != http.StatusOK {
		t.Fatalf("abort = %d %s", abort.Code, abort.Body.String())
	}
}

func TestGoalsVerificationAndAutopilotHTTP(t *testing.T) {
	srv, _ := newGoalServer(t)

	code, _ := postGoal(t, srv, `{"description":"hands off","verification":"true","autopilot":true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("autopilot without allowlist = %d", code)
	}

	code, _ = postGoal(t, srv, `{
		"description":"overlap",
		"verification":"true",
		"autopilot":true,
		"allowedTools":["bash"],
		"denyTools":["bash"]
	}`)
	if code != http.StatusBadRequest {
		t.Fatalf("deny overlap = %d", code)
	}

	code, created := postGoal(t, srv, `{
		"description":"pass",
		"verification":"true",
		"maxIterations":4,
		"maxTokens":1000,
		"maxWallClockS":60,
		"autopilot":true,
		"allowedTools":["bash"],
		"denyTools":["write"]
	}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if !created.Autopilot || created.MaxTokens != 1000 || len(created.Criteria) != 1 || created.Criteria[0].Check != "cmd: true" {
		t.Fatalf("created=%+v", created)
	}
	if len(created.DenyTools) != 1 || created.DenyTools[0] != "write" {
		t.Fatalf("deny=%v", created.DenyTools)
	}

	run := httptest.NewRecorder()
	srv.Handler().ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/goals/"+created.ID+"/run?background=1", nil))
	if run.Code != http.StatusAccepted {
		t.Fatalf("background = %d %s", run.Code, run.Body.String())
	}
	done := waitGoal(t, srv, created.ID, "done")
	if done.LastIteration < 1 {
		t.Fatalf("done=%+v", done)
	}

	code, failing := postGoal(t, srv, `{
		"description":"never",
		"verification":"false",
		"maxIterations":20,
		"maxNoProgressIters":20,
		"maxVerifyFailures":2,
		"maxTokens":200000,
		"maxWallClockS":60
	}`)
	if code != http.StatusCreated {
		t.Fatalf("failing create = %d", code)
	}
	run = httptest.NewRecorder()
	srv.Handler().ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/goals/"+failing.ID+"/run?background=1", nil))
	if run.Code != http.StatusAccepted {
		t.Fatalf("background fail = %d %s", run.Code, run.Body.String())
	}
	blocked := waitGoal(t, srv, failing.ID, "blocked")
	if blocked.Escalation != "human" || blocked.LastIteration != 2 || !strings.Contains(blocked.FailReason, "verification failed") {
		t.Fatalf("blocked=%+v", blocked)
	}

	code, tokens := postGoal(t, srv, `{
		"description":"tokens",
		"verification":"false",
		"maxIterations":20,
		"maxNoProgressIters":40,
		"maxVerifyFailures":40,
		"maxTokens":1,
		"maxWallClockS":60
	}`)
	if code != http.StatusCreated {
		t.Fatalf("token create = %d", code)
	}
	run = httptest.NewRecorder()
	srv.Handler().ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/goals/"+tokens.ID+"/run?background=1", nil))
	if run.Code != http.StatusAccepted {
		t.Fatalf("token run = %d %s", run.Code, run.Body.String())
	}
	stopped := waitGoal(t, srv, tokens.ID, "failed")
	if !strings.Contains(stopped.FailReason, "token budget") || stopped.LastIteration < 1 {
		t.Fatalf("tokens=%+v", stopped)
	}
}
