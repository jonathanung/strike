package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

const fakeGH = `#!/bin/sh
if [ -z "${GH_FAKE_LOG:-}" ]; then
  echo "GH_FAKE_LOG unset" >&2
  exit 2
fi
printf '%s\n' "$*" >> "$GH_FAKE_LOG"
printf 'PROMPT=%s\n' "${GH_PROMPT_DISABLED:-}" >> "$GH_FAKE_LOG"
if [ "${GH_FAKE_FAIL:-}" = "1" ]; then
  echo "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA denied" >&2
  exit 1
fi
if [ "$2" = "comment" ]; then
  cat > "${GH_FAKE_STDIN:-/dev/null}"
fi
state=open
prev=
for a in "$@"; do
  if [ "$prev" = "--state" ]; then
    state=$a
  fi
  prev=$a
done
case "$1:$2:$state" in
  issue:list:open)
    cat <<'EOF'
[{"number":2,"title":"Older","state":"OPEN","url":"https://github.com/example/widgets/issues/2","updatedAt":"2026-10-01T00:00:00Z","author":{"login":"ada"},"labels":[{"name":"bug"}]},{"number":10,"title":"token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked","state":"OPEN","url":"https://github.com/example/widgets/issues/10","updatedAt":"2026-10-20T00:00:00Z","author":{"login":"grace"},"labels":[{"name":"webui"}]}]
EOF
    ;;
  issue:list:closed)
    cat <<'EOF'
[{"number":3,"title":"Done","state":"CLOSED","url":"https://github.com/example/widgets/issues/3","updatedAt":"2026-09-01T00:00:00Z","author":{"login":"ada"},"labels":[]}]
EOF
    ;;
  pr:list:open)
    cat <<'EOF'
[{"number":7,"title":"Add queue","state":"OPEN","url":"https://github.com/example/widgets/pull/7","updatedAt":"2026-10-19T00:00:00Z","author":{"login":"lin"},"labels":[]}]
EOF
    ;;
  pr:list:closed)
    printf '%s\n' '[]'
    ;;
  pr:list:merged)
    cat <<'EOF'
[{"number":4,"title":"Landed","state":"MERGED","url":"https://github.com/example/widgets/pull/4","updatedAt":"2026-09-02T00:00:00Z","author":{"login":"lin"},"labels":[]}]
EOF
    ;;
  issue:view:*|pr:view:*)
    cat <<'EOF'
{"number":10,"title":"token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked","body":"Please look.\nsecret ghp_CCCCCCCCCCCCCCCCCCCCCCCCCCCC","state":"OPEN","url":"https://github.com/example/widgets/issues/10","updatedAt":"2026-10-20T00:00:00Z","author":{"login":"grace"},"labels":[{"name":"webui"}],"comments":[{"author":{"login":"ada"},"body":"On it","createdAt":"2026-10-21T00:00:00Z"}]}
EOF
    ;;
  issue:comment:*|pr:comment:*|issue:close:*|pr:close:*|issue:reopen:*|pr:reopen:*)
    printf '%s\n' '{}'
    ;;
  *)
    echo "unexpected gh $*" >&2
    exit 2
    ;;
esac
`

func installFakeGH(t *testing.T) (logPath, stdinPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "gh.log")
	stdinPath = filepath.Join(dir, "stdin")
	script := filepath.Join(dir, "gh")
	if err := os.WriteFile(script, []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := ghBin
	ghBin = script
	t.Cleanup(func() { ghBin = prev })
	t.Setenv("GH_FAKE_LOG", logPath)
	t.Setenv("GH_FAKE_STDIN", stdinPath)
	t.Setenv("GH_TOKEN", "ghp_BBBBBBBBBBBBBBBBBBBBBBBB")
	return logPath, stdinPath
}

func gitWorkspace(t *testing.T, remotes map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for name, url := range remotes {
		cmd := exec.Command("git", "remote", "add", name, url)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote %s: %v %s", name, err, out)
		}
	}
	return dir
}

func appsTestServer(t *testing.T, dir string, hub *LiveHub, readOnly bool) *httptest.Server {
	t.Helper()
	srv, err := New(Options{
		Auth:       true,
		Token:      "t",
		SessionDir: t.TempDir(),
		WorkDir:    dir,
		LiveHub:    hub,
		ReadOnly:   readOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func appsDo(t *testing.T, method, url, body, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := res.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return res.StatusCode, buf
}

func TestParseGitHubRemote(t *testing.T) {
	okCases := map[string]string{
		"https://github.com/jonathanung/strike.git":                                              "jonathanung/strike",
		"https://github.com/jonathanung/strike":                                                  "jonathanung/strike",
		"https://github.com/jonathanung/strike/":                                                 "jonathanung/strike",
		"http://github.com/jonathanung/strike.git":                                               "jonathanung/strike",
		"git@github.com:jonathanung/strike.git":                                                  "jonathanung/strike",
		"ssh://git@github.com/jonathanung/strike.git":                                            "jonathanung/strike",
		"https://github.com/jonathanung/strike/issues/1":                                         "jonathanung/strike",
		"https://x-access-token:ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA@github.com/example/widgets.git": "example/widgets",
		"https://user:ghs_BBBBBBBBBBBBBBBBBBBBBBBB@github.com/example/widgets":                   "example/widgets",
	}
	for in, want := range okCases {
		got, ok := parseGitHubRemote(in)
		if !ok || got != want {
			t.Errorf("parseGitHubRemote(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if strings.Contains(got, "ghp_") || strings.Contains(got, "ghs_") || strings.Contains(got, "x-access-token") {
			t.Errorf("parseGitHubRemote(%q) leaked a credential: %q", in, got)
		}
	}
	rejects := []string{
		"",
		"https://gitlab.com/acme/widgets.git",
		"https://github.com/../secret",
		"https://github.com/acme",
		"git@github.com:acme",
		"https://github.example.com/acme/widgets.git",
		"https://github.com/acme/repo;rm",
		"not a url",
	}
	for _, in := range rejects {
		if _, ok := parseGitHubRemote(in); ok {
			t.Errorf("parseGitHubRemote(%q) accepted", in)
		}
	}
}

func TestGitHubHandoffPromptRedactsTokens(t *testing.T) {
	prompt := githubHandoffPrompt("example/widgets", appDetail{
		appItem: appItem{
			Title:    "token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked",
			Subtitle: "grace",
			Badge:    "open",
			URL:      "https://github.com/example/widgets/issues/10",
			Kind:     "issue",
			Number:   10,
		},
		Body: "secret ghp_CCCCCCCCCCCCCCCCCCCCCCCCCCCC",
	})
	if strings.Contains(prompt, "ghp_") {
		t.Fatalf("prompt leaked a token: %s", prompt)
	}
	if !strings.Contains(prompt, "GitHub issue #10 in example/widgets") {
		t.Fatalf("prompt missing item identity: %s", prompt)
	}
	if !strings.Contains(prompt, "[REDACTED_GITHUB_TOKEN]") {
		t.Fatalf("prompt missing redaction: %s", prompt)
	}
	if !strings.Contains(prompt, "do not comment or close") {
		t.Fatalf("prompt missing the hold-off line: %s", prompt)
	}
}

func TestAppsCatalogAndTriage(t *testing.T) {
	logPath, stdinPath := installFakeGH(t)
	dir := gitWorkspace(t, map[string]string{
		"origin": "https://github.com/example/widgets.git",
	})
	ts := appsTestServer(t, dir, nil, false)

	status, body := appsDo(t, http.MethodGet, ts.URL+"/v1/apps", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated catalog = %d %s", status, body)
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps", "", "t")
	if status != http.StatusOK {
		t.Fatalf("catalog = %d %s", status, body)
	}
	if strings.Contains(string(body), "ghp_") || strings.Contains(string(body), "LaunchDarkly") || strings.Contains(string(body), "Issue Radar") {
		t.Fatalf("catalog leaked a token or a fake app: %s", body)
	}
	var catalog struct {
		Apps []appInfo `json:"apps"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Apps) != 1 || catalog.Apps[0].ID != "github-triage" || !catalog.Apps[0].Handoff {
		t.Fatalf("catalog = %+v", catalog.Apps)
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/nope/items", "", "t")
	if status != http.StatusNotFound {
		t.Fatalf("unknown app = %d %s", status, body)
	}
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=nope", "", "t")
	if status != http.StatusBadRequest {
		t.Fatalf("bad kind = %d %s", status, body)
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=issue&state=open", "", "t")
	if status != http.StatusOK {
		t.Fatalf("issues = %d %s", status, body)
	}
	if strings.Contains(string(body), "ghp_") || strings.Contains(string(body), "ghp_BBBBBBBBBBBBBBBBBBBBBBBB") {
		t.Fatalf("issue list leaked a token: %s", body)
	}
	var list struct {
		Repo  string    `json:"repo"`
		Items []appItem `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if list.Repo != "example/widgets" || len(list.Items) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if list.Items[0].ID != "issue-10" || list.Items[0].Number != 10 {
		t.Fatalf("newest item = %+v", list.Items[0])
	}
	if !strings.Contains(list.Items[0].Title, "[REDACTED_GITHUB_TOKEN]") {
		t.Fatalf("title not redacted: %q", list.Items[0].Title)
	}
	if list.Items[1].ID != "issue-2" || list.Items[1].Subtitle != "ada" || len(list.Items[1].Labels) != 1 {
		t.Fatalf("second item = %+v", list.Items[1])
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=pr&state=open", "", "t")
	if status != http.StatusOK {
		t.Fatalf("prs = %d %s", status, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "pr-7" || list.Items[0].Kind != "pr" {
		t.Fatalf("prs = %+v", list.Items)
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=all&state=closed", "", "t")
	if status != http.StatusOK {
		t.Fatalf("closed = %d %s", status, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, it := range list.Items {
		ids[it.ID] = true
	}
	if !ids["issue-3"] || !ids["pr-4"] || ids["issue-10"] {
		t.Fatalf("closed ids = %+v", list.Items)
	}

	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items/issue-10", "", "t")
	if status != http.StatusOK {
		t.Fatalf("detail = %d %s", status, body)
	}
	if strings.Contains(string(body), "ghp_") {
		t.Fatalf("detail leaked a token: %s", body)
	}
	var detail appDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Number != 10 || !strings.Contains(detail.Body, "[REDACTED_GITHUB_TOKEN]") {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.Comments) != 1 || detail.Comments[0].Author != "ada" || detail.Comments[0].Body != "On it" {
		t.Fatalf("comments = %+v", detail.Comments)
	}
	if len(detail.Actions) != 2 || detail.Actions[0].ID != "comment" || !detail.Actions[0].NeedsBody || detail.Actions[1].ID != "close" {
		t.Fatalf("actions = %+v", detail.Actions)
	}

	for _, id := range []string{"issue-01", "issue-0", "pr-1;rm", "note-1", "../secret"} {
		status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items/"+id, "", "t")
		if status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Fatalf("id %s = %d %s", id, status, body)
		}
	}

	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-10/act", `{"action":"comment","body":"   "}`, "t")
	if status != http.StatusBadRequest {
		t.Fatalf("empty comment = %d %s", status, body)
	}
	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-10/act", `{"action":"comment","body":"hello from triage"}`, "t")
	if status != http.StatusOK {
		t.Fatalf("comment = %d %s", status, body)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "hello from triage" {
		t.Fatalf("stdin = %q", stdin)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "issue comment 10 --repo example/widgets --body-file -") {
		t.Fatalf("comment args missing: %s", log)
	}
	if strings.Contains(string(log), "hello from triage") || strings.Contains(string(log), "ghp_BBBBBBBBBBBBBBBBBBBBBBBB") {
		t.Fatalf("gh args contained the comment or the token:\n%s", log)
	}
	if !strings.Contains(string(log), "PROMPT=1") {
		t.Fatalf("gh was not told to skip prompts: %s", log)
	}

	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-10/act", `{"action":"close"}`, "t")
	if status != http.StatusOK {
		t.Fatalf("close = %d %s", status, body)
	}
	log, _ = os.ReadFile(logPath)
	if !strings.Contains(string(log), "issue close 10 --repo example/widgets") {
		t.Fatalf("close args missing: %s", log)
	}
	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-3/act", `{"action":"reopen"}`, "t")
	if status != http.StatusOK {
		t.Fatalf("reopen = %d %s", status, body)
	}
	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-10/act", `{"action":"label"}`, "t")
	if status != http.StatusBadRequest {
		t.Fatalf("unknown action = %d %s", status, body)
	}
}

func TestAppsGitHubRemoteResolution(t *testing.T) {
	logPath, _ := installFakeGH(t)
	bare := gitWorkspace(t, nil)
	ts := appsTestServer(t, bare, nil, false)
	status, body := appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items", "", "t")
	if status != http.StatusBadGateway || !strings.Contains(string(body), "no git remotes") {
		t.Fatalf("no remote = %d %s", status, body)
	}
	if _, err := os.Stat(logPath); err == nil {
		logged, _ := os.ReadFile(logPath)
		if len(logged) > 0 {
			t.Fatalf("gh ran without a remote: %s", logged)
		}
	}

	gitlab := gitWorkspace(t, map[string]string{"origin": "https://gitlab.com/acme/widgets.git"})
	ts = appsTestServer(t, gitlab, nil, false)
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items", "", "t")
	if status != http.StatusBadGateway || !strings.Contains(string(body), "no GitHub remote") {
		t.Fatalf("gitlab = %d %s", status, body)
	}

	mixed := gitWorkspace(t, map[string]string{
		"origin":   "https://gitlab.com/acme/widgets.git",
		"upstream": "git@github.com:example/widgets.git",
	})
	ts = appsTestServer(t, mixed, nil, false)
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=pr&state=open", "", "t")
	if status != http.StatusOK || !strings.Contains(string(body), `"repo":"example/widgets"`) {
		t.Fatalf("upstream github = %d %s", status, body)
	}

	sneaky := gitWorkspace(t, map[string]string{"origin": "https://github.com/../secret"})
	ts = appsTestServer(t, sneaky, nil, false)
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items", "", "t")
	if status != http.StatusBadGateway {
		t.Fatalf("bad slug = %d %s", status, body)
	}
	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "../secret") {
		t.Fatalf("gh saw a bad repo: %s", logged)
	}

	cred := gitWorkspace(t, map[string]string{
		"origin": "https://x-access-token:ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAA@github.com/example/widgets.git",
	})
	ts = appsTestServer(t, cred, nil, false)
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items?kind=issue&state=open", "", "t")
	if status != http.StatusOK || !strings.Contains(string(body), `"repo":"example/widgets"`) {
		t.Fatalf("credentialed remote = %d %s", status, body)
	}
	if strings.Contains(string(body), "ghp_") || strings.Contains(string(body), "x-access-token") {
		t.Fatalf("credentialed remote leaked: %s", body)
	}
	logged, _ = os.ReadFile(logPath)
	if strings.Contains(string(logged), "x-access-token") || strings.Contains(string(logged), "ghp_") {
		t.Fatalf("gh saw a credential: %s", logged)
	}
}

func TestAppsGHMissingAndFailing(t *testing.T) {
	dir := gitWorkspace(t, map[string]string{"origin": "https://github.com/example/widgets.git"})
	prev := ghBin
	ghBin = filepath.Join(t.TempDir(), "no-such-gh")
	t.Cleanup(func() { ghBin = prev })
	ts := appsTestServer(t, dir, nil, false)
	status, body := appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items", "", "t")
	if status != http.StatusBadGateway || !strings.Contains(string(body), "gh is not installed") {
		t.Fatalf("missing gh = %d %s", status, body)
	}
	if strings.Contains(string(body), `"items"`) {
		t.Fatalf("missing gh still returned items: %s", body)
	}

	installFakeGH(t)
	t.Setenv("GH_FAKE_FAIL", "1")
	ts = appsTestServer(t, dir, nil, false)
	status, body = appsDo(t, http.MethodGet, ts.URL+"/v1/apps/github-triage/items", "", "t")
	if status != http.StatusBadGateway {
		t.Fatalf("gh fail = %d %s", status, body)
	}
	if strings.Contains(string(body), "ghp_") {
		t.Fatalf("gh error leaked a token: %s", body)
	}
	if !strings.Contains(string(body), "[REDACTED_GITHUB_TOKEN]") {
		t.Fatalf("gh error was not redacted: %s", body)
	}
}

func TestAppsActReadOnlyAndHandoff(t *testing.T) {
	logPath, _ := installFakeGH(t)
	dir := gitWorkspace(t, map[string]string{"origin": "https://github.com/example/widgets.git"})
	ro := appsTestServer(t, dir, nil, true)
	status, body := appsDo(t, http.MethodPost, ro.URL+"/v1/apps/github-triage/items/issue-10/act", `{"action":"close"}`, "t")
	if status != http.StatusForbidden {
		t.Fatalf("read-only act = %d %s", status, body)
	}
	status, body = appsDo(t, http.MethodPost, ro.URL+"/v1/apps/github-triage/items/issue-10/handoff", "", "t")
	if status != http.StatusForbidden {
		t.Fatalf("read-only handoff = %d %s", status, body)
	}
	logged, _ := os.ReadFile(logPath)
	if strings.Contains(string(logged), "close") || strings.Contains(string(logged), "view") {
		t.Fatalf("read-only server called gh: %s", logged)
	}

	plain := appsTestServer(t, dir, nil, false)
	status, body = appsDo(t, http.MethodPost, plain.URL+"/v1/apps/github-triage/items/issue-10/handoff", "", "t")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("handoff without engine = %d %s", status, body)
	}

	ops := make(chan protocol.Op, 2)
	var hub *LiveHub
	var live *Live
	hub = NewLiveHub(func(ctx context.Context) (string, error) {
		live = NewLive("handed", dir, nil, ops)
		hub.Add("handed", live)
		return "handed", nil
	}, nil)
	t.Cleanup(func() {
		if live != nil {
			live.Close()
		}
	})
	ts := appsTestServer(t, dir, hub, false)
	status, body = appsDo(t, http.MethodPost, ts.URL+"/v1/apps/github-triage/items/issue-10/handoff", "", "t")
	if status != http.StatusCreated {
		t.Fatalf("handoff = %d %s", status, body)
	}
	var created struct {
		OK     bool   `json:"ok"`
		RootID string `json:"rootId"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.RootID != "handed" {
		t.Fatalf("created = %+v", created)
	}
	if hub.ActiveID() != "handed" {
		t.Fatalf("active = %q", hub.ActiveID())
	}
	select {
	case op := <-ops:
		ui, ok := op.(protocol.UserInput)
		if !ok {
			t.Fatalf("op = %#v", op)
		}
		if strings.Contains(ui.Text, "ghp_") {
			t.Fatalf("handoff op leaked a token: %s", ui.Text)
		}
		if !strings.Contains(ui.Text, "GitHub issue #10 in example/widgets") || !strings.Contains(ui.Text, "Please look") {
			t.Fatalf("handoff op = %s", ui.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handoff did not submit user.input")
	}
	if !strings.Contains(hub.List()[0].Title, "Issue #10") {
		t.Fatalf("session title = %+v", hub.List())
	}
}
