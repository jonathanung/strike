package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/deploywatch"
	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

func newDeployWatchServer(t *testing.T, readOnly bool) (http.Handler, *httptest.Server) {
	t.Helper()
	st, err := deploywatch.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := deploywatch.New(st, deploywatch.Config{Now: func() time.Time { return now }})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(target.Close)
	srv, err := New(Options{SessionDir: t.TempDir(), Auth: true, Token: "t", DeployWatches: svc, ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), target
}

func dwDo(t *testing.T, h http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if auth {
		req.Header.Set("Authorization", "Bearer t")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeployWatchRoutes(t *testing.T) {
	h, target := newDeployWatchServer(t, false)

	if rec := dwDo(t, h, http.MethodGet, "/v1/deploy-watches", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list = %d", rec.Code)
	}
	rec := dwDo(t, h, http.MethodGet, "/v1/deploy-watches", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"watches":[]`) {
		t.Fatalf("empty list = %d %s", rec.Code, rec.Body.String())
	}

	if rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches", `{"repo":"acme","sha":"abc1234","checks":[{"kind":"github_checks"}]}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches", `{"repo":"acme/app","sha":"abc1234","failureThreshold":99,"checks":[{"kind":"github_checks"}]}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range threshold create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches", `{"repo":"acme/app","bogus":1}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field create = %d", rec.Code)
	}

	body := `{"repo":"acme/app","sha":"abc1234","pr":7,"windowSeconds":600,"checks":[{"kind":"http","name":"api","url":"` + target.URL + `/health"}]}`
	rec = dwDo(t, h, http.MethodPost, "/v1/deploy-watches", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created deploywatch.Watch
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != deploywatch.StatusWatching || created.PR != 7 {
		t.Fatalf("created = %+v", created)
	}

	rec = dwDo(t, h, http.MethodGet, "/v1/deploy-watches/"+created.ID, "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dwDo(t, h, http.MethodGet, "/v1/deploy-watches/dw_missing", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing get = %d", rec.Code)
	}

	if created.FailureThreshold != deploywatch.DefaultFailureThreshold {
		t.Fatalf("threshold = %d", created.FailureThreshold)
	}

	// The target always fails; only the third consecutive failure settles.
	var polled deploywatch.Watch
	for i := 1; i <= deploywatch.DefaultFailureThreshold; i++ {
		rec = dwDo(t, h, http.MethodPost, "/v1/deploy-watches/"+created.ID+"/poll", "", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll %d = %d %s", i, rec.Code, rec.Body.String())
		}
		polled = deploywatch.Watch{}
		if err := json.Unmarshal(rec.Body.Bytes(), &polled); err != nil {
			t.Fatal(err)
		}
		if len(polled.Latest) != 1 || polled.Latest[0].State != deploywatch.StateFailing {
			t.Fatalf("poll %d latest = %+v", i, polled.Latest)
		}
		if i < deploywatch.DefaultFailureThreshold && polled.Verdict != "" {
			t.Fatalf("poll %d settled early: %+v", i, polled)
		}
	}
	if polled.Verdict != deploywatch.VerdictRegression {
		t.Fatalf("polled = %+v", polled)
	}

	if rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches/dw_missing/cancel", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing cancel = %d", rec.Code)
	}
	rec = dwDo(t, h, http.MethodPost, "/v1/deploy-watches/"+created.ID+"/cancel", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"verdict":"regression"`) {
		t.Fatalf("cancel after verdict should keep it: %d %s", rec.Code, rec.Body.String())
	}

	rec = dwDo(t, h, http.MethodGet, "/v1/deploy-watches", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeployWatchRoutesReadOnlyAndUnavailable(t *testing.T) {
	h, _ := newDeployWatchServer(t, true)
	if rec := dwDo(t, h, http.MethodGet, "/v1/deploy-watches", "", true); rec.Code != http.StatusOK {
		t.Fatalf("read-only list = %d", rec.Code)
	}
	for _, path := range []string{"/v1/deploy-watches", "/v1/deploy-watches/dw_x/poll", "/v1/deploy-watches/dw_x/cancel"} {
		if rec := dwDo(t, h, http.MethodPost, path, `{}`, true); rec.Code != http.StatusForbidden {
			t.Fatalf("read-only POST %s = %d", path, rec.Code)
		}
	}

	srv, err := New(Options{SessionDir: t.TempDir(), Auth: true, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := dwDo(t, srv.Handler(), http.MethodGet, "/v1/deploy-watches", "", true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service = %d", rec.Code)
	}
}

func TestDeployWatchHandoffRoutes(t *testing.T) {
	h, target := newDeployWatchServer(t, false)
	body := `{"repo":"acme/app","sha":"abc1234","failureThreshold":1,"windowSeconds":600,"checks":[{"kind":"http","name":"api","url":"` + target.URL + `"}]}`
	rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches", body, true)
	var created deploywatch.Watch
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	fix := "/v1/deploy-watches/" + created.ID + "/fix-session"
	revert := "/v1/deploy-watches/" + created.ID + "/revert-pr"

	if rec := dwDo(t, h, http.MethodPost, fix, `{"confirm":true}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth fix = %d", rec.Code)
	}
	for _, path := range []string{fix, revert} {
		if rec := dwDo(t, h, http.MethodPost, path, `{}`, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation required") {
			t.Fatalf("unconfirmed %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// This server has no launcher or reverter wired.
	if rec := dwDo(t, h, http.MethodPost, fix, `{"confirm":true}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fix without launcher = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dwDo(t, h, http.MethodPost, revert, `{"confirm":true,"permissionMode":"plan"}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("revert with mode = %d", rec.Code)
	}
	if rec := dwDo(t, h, http.MethodPost, revert, `{"confirm":true}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("revert without reverter = %d %s", rec.Code, rec.Body.String())
	}
	if rec := dwDo(t, h, http.MethodPost, "/v1/deploy-watches/dw_missing/fix-session", `{"confirm":true}`, true); rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusNotFound {
		t.Fatalf("missing fix = %d", rec.Code)
	}

	ro, _ := newDeployWatchServer(t, true)
	if rec := dwDo(t, ro, http.MethodPost, fix, `{"confirm":true}`, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only fix = %d", rec.Code)
	}
}

func TestDeployWatchFixSessionRoute(t *testing.T) {
	st, err := deploywatch.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch := &handoffLaunch{}
	svc := deploywatch.New(st, deploywatch.Config{Launcher: launch})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(target.Close)
	srv, err := New(Options{SessionDir: t.TempDir(), Auth: true, Token: "t", DeployWatches: svc})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	w, err := svc.Create(deploywatch.Input{Repo: "acme/app", SHA: "abc1234", FailureThreshold: 1, WindowSeconds: 600,
		Checks: []deploywatch.CheckSpec{{Kind: deploywatch.KindHTTP, URL: target.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	fix := "/v1/deploy-watches/" + w.ID + "/fix-session"
	if rec := dwDo(t, h, http.MethodPost, fix, `{"confirm":true}`, true); rec.Code != http.StatusConflict {
		t.Fatalf("fix before regression = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := svc.Poll(context.Background(), w.ID); err != nil {
		t.Fatal(err)
	}
	rec := dwDo(t, h, http.MethodPost, fix, `{"confirm":true,"permissionMode":"plan"}`, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fixSession":{`) || !strings.Contains(rec.Body.String(), `"sessionId":"root-fix"`) {
		t.Fatalf("fix = %d %s", rec.Code, rec.Body.String())
	}
	if len(launch.reqs) != 1 || launch.reqs[0].PermissionMode != "plan" {
		t.Fatalf("launch = %+v", launch.reqs)
	}
}

type handoffLaunch struct {
	reqs []rootstart.Request
}

func (l *handoffLaunch) StartRoot(_ context.Context, req rootstart.Request) (rootstart.Result, error) {
	l.reqs = append(l.reqs, req)
	return rootstart.Result{SessionID: "root-fix", Created: true}, nil
}
