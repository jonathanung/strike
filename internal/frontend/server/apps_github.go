package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/pkg/redact"
)

// ghBin is the gh executable. Tests point it at a fake on disk.
var ghBin = "gh"

const (
	githubCommentMax = 8 << 10
	githubBodyMax    = 20000
	githubPromptMax  = 12000
	githubNoteMax    = 2000
	githubNotesMax   = 15
)

// githubTriageApp lists and triages issues and pull requests for the
// workspace git remote. gh runs on the daemon. The page never sees a token.
type githubTriageApp struct{}

func (githubTriageApp) info() appInfo {
	return appInfo{
		ID:      "github-triage",
		Title:   "GitHub triage",
		Summary: "Issues and pull requests for this workspace's repository. gh runs on the daemon.",
		Handoff: true,
	}
}

func (githubTriageApp) list(ctx context.Context, dir, kind, state string) (string, []appItem, error) {
	repo, err := githubRepo(ctx, dir)
	if err != nil {
		return "", nil, err
	}
	var items []appItem
	if kind == "issue" || kind == "all" {
		got, err := githubList(ctx, dir, repo, "issue", state)
		if err != nil {
			return "", nil, err
		}
		items = append(items, got...)
	}
	if kind == "pr" || kind == "all" {
		got, err := githubList(ctx, dir, repo, "pr", state)
		if err != nil {
			return "", nil, err
		}
		items = append(items, got...)
		if state == "closed" {
			merged, err := githubList(ctx, dir, repo, "pr", "merged")
			if err != nil {
				return "", nil, err
			}
			items = append(items, merged...)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := parseGHTime(items[i].UpdatedAt), parseGHTime(items[j].UpdatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return items[i].Number > items[j].Number
	})
	return repo, items, nil
}

func (githubTriageApp) get(ctx context.Context, dir, itemID string) (appDetail, error) {
	repo, err := githubRepo(ctx, dir)
	if err != nil {
		return appDetail{}, err
	}
	kind, number, err := parseGitHubItemID(itemID)
	if err != nil {
		return appDetail{}, err
	}
	raw, err := runGH(ctx, dir, "", ghViewArgs(repo, kind, number)...)
	if err != nil {
		return appDetail{}, err
	}
	var row ghRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return appDetail{}, appFail("gh returned unexpected JSON")
	}
	if row.Number == 0 {
		row.Number = number
	}
	return ghDetail(kind, row), nil
}

func (githubTriageApp) act(ctx context.Context, dir, itemID, action, body string) error {
	repo, err := githubRepo(ctx, dir)
	if err != nil {
		return err
	}
	kind, number, err := parseGitHubItemID(itemID)
	if err != nil {
		return err
	}
	num := strconv.Itoa(number)
	var args []string
	var stdin string
	switch action {
	case "comment":
		body = strings.TrimSpace(body)
		if body == "" {
			return appBad("comment body is required")
		}
		if strings.ContainsRune(body, 0) || len(body) > githubCommentMax {
			return appBad("comment body is empty or longer than 8 KiB")
		}
		args = []string{kind, "comment", num, "--repo", repo, "--body-file", "-"}
		stdin = body
	case "close":
		args = []string{kind, "close", num, "--repo", repo}
	case "reopen":
		args = []string{kind, "reopen", num, "--repo", repo}
	default:
		return appBad("unknown action")
	}
	_, err = runGH(ctx, dir, stdin, args...)
	return err
}

func (githubTriageApp) handoff(ctx context.Context, dir, itemID string) (string, string, error) {
	detail, err := githubTriageApp{}.get(ctx, dir, itemID)
	if err != nil {
		return "", "", err
	}
	repo, err := githubRepo(ctx, dir)
	if err != nil {
		return "", "", err
	}
	return githubSessionTitle(detail), githubHandoffPrompt(repo, detail), nil
}

func githubList(ctx context.Context, dir, repo, kind, state string) ([]appItem, error) {
	raw, err := runGH(ctx, dir, "", ghListArgs(repo, kind, state)...)
	if err != nil {
		return nil, err
	}
	var rows []ghRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, appFail("gh returned unexpected JSON")
	}
	items := make([]appItem, 0, len(rows))
	for _, row := range rows {
		if row.Number < 1 {
			continue
		}
		items = append(items, scrubItem(ghItem(kind, row)))
	}
	return items, nil
}

type ghUser struct {
	Login string `json:"login"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghComment struct {
	Author    ghUser `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

type ghRow struct {
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	State     string      `json:"state"`
	URL       string      `json:"url"`
	UpdatedAt string      `json:"updatedAt"`
	Author    ghUser      `json:"author"`
	Labels    []ghLabel   `json:"labels"`
	Comments  []ghComment `json:"comments"`
}

func ghListArgs(repo, kind, state string) []string {
	return []string{
		kind, "list",
		"--repo", repo,
		"--state", state,
		"--limit", "40",
		"--json", "number,title,state,url,updatedAt,author,labels",
	}
}

func ghViewArgs(repo, kind string, number int) []string {
	return []string{
		kind, "view", strconv.Itoa(number),
		"--repo", repo,
		"--json", "number,title,body,state,url,updatedAt,author,labels,comments",
	}
}

func ghItem(kind string, row ghRow) appItem {
	labels := make([]string, 0, len(row.Labels))
	for _, label := range row.Labels {
		name := strings.TrimSpace(label.Name)
		if name == "" {
			continue
		}
		labels = append(labels, name)
		if len(labels) == 12 {
			break
		}
	}
	return appItem{
		ID:        kind + "-" + strconv.Itoa(row.Number),
		Title:     strings.TrimSpace(row.Title),
		Subtitle:  strings.TrimSpace(row.Author.Login),
		Badge:     ghBadge(row.State),
		UpdatedAt: strings.TrimSpace(row.UpdatedAt),
		Labels:    labels,
		URL:       strings.TrimSpace(row.URL),
		Kind:      kind,
		Number:    row.Number,
	}
}

func ghDetail(kind string, row ghRow) appDetail {
	item := scrubItem(ghItem(kind, row))
	notes := make([]appNote, 0, len(row.Comments))
	for _, c := range row.Comments {
		if len(notes) == githubNotesMax {
			break
		}
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		notes = append(notes, appNote{
			Author: redact.String(strings.TrimSpace(c.Author.Login)),
			Body:   redact.String(trimRunes(body, githubNoteMax)),
			At:     strings.TrimSpace(c.CreatedAt),
		})
	}
	return appDetail{
		appItem:  item,
		Body:     redact.String(trimRunes(row.Body, githubBodyMax)),
		Comments: notes,
		Actions:  actionsFor(item.Badge),
	}
}

func actionsFor(badge string) []appAction {
	comment := appAction{ID: "comment", Label: "Comment", NeedsBody: true}
	switch badge {
	case "open":
		return []appAction{comment, {ID: "close", Label: "Close"}}
	case "closed":
		return []appAction{comment, {ID: "reopen", Label: "Reopen"}}
	default:
		return []appAction{comment}
	}
}

func ghBadge(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return "open"
	case "merged":
		return "merged"
	case "closed":
		return "closed"
	default:
		if state == "" {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(state))
	}
}

func scrubItem(it appItem) appItem {
	it.Title = redact.String(it.Title)
	it.Subtitle = redact.String(it.Subtitle)
	it.URL = redact.String(it.URL)
	for i, label := range it.Labels {
		it.Labels[i] = redact.String(label)
	}
	return it
}

func githubSessionTitle(d appDetail) string {
	kind := "Issue"
	if d.Kind == "pr" {
		kind = "PR"
	}
	return trimRunes(redact.String(fmt.Sprintf("%s #%d %s", kind, d.Number, d.Title)), 80)
}

func githubHandoffPrompt(repo string, d appDetail) string {
	kind := "issue"
	if d.Kind == "pr" {
		kind = "pull request"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub %s #%d in %s: %s\n\n", kind, d.Number, repo, d.Title)
	b.WriteString("Triage this item. The text below is what gh returned just now. Do not invent a different item, and do not comment or close it unless asked.\n\n")
	if d.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", d.URL)
	}
	if d.Badge != "" {
		fmt.Fprintf(&b, "State: %s\n", d.Badge)
	}
	if d.Subtitle != "" {
		fmt.Fprintf(&b, "Author: %s\n", d.Subtitle)
	}
	if len(d.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(d.Labels, ", "))
	}
	b.WriteString("\nBody:\n")
	b.WriteString(trimRunes(d.Body, githubPromptMax))
	if len(d.Comments) > 0 {
		b.WriteString("\n\nRecent comments:\n")
		for _, c := range d.Comments {
			fmt.Fprintf(&b, "\n- %s: %s\n", c.Author, trimRunes(c.Body, 500))
		}
	}
	return redact.String(b.String())
}

func parseGitHubItemID(id string) (string, int, error) {
	kind, num, ok := strings.Cut(id, "-")
	if !ok || (kind != "issue" && kind != "pr") {
		return "", 0, appBad("item id must be issue-<n> or pr-<n>")
	}
	if num == "" || num[0] == '0' || len(num) > 9 {
		return "", 0, appBad("item id must be issue-<n> or pr-<n>")
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return "", 0, appBad("item id must be issue-<n> or pr-<n>")
		}
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 {
		return "", 0, appBad("item id must be issue-<n> or pr-<n>")
	}
	return kind, n, nil
}

func parseGHTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

func githubRepo(ctx context.Context, dir string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", appFail("git is not installed on the daemon host")
	}
	out, err := runGit(ctx, dir, "remote")
	if err != nil {
		return "", appFail("this workspace is not a git repository")
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return "", appFail("this workspace has no git remotes")
	}
	sort.SliceStable(names, func(i, j int) bool {
		if names[i] == "origin" {
			return true
		}
		if names[j] == "origin" {
			return false
		}
		return false
	})
	var fallback string
	sawURL := false
	for _, name := range names {
		if !safeRemoteName(name) {
			continue
		}
		// remote get-url applies url.*.insteadof and can print a credential.
		// config --get is the URL stored in this repo.
		url, err := runGit(ctx, dir, "config", "--local", "--get", "remote."+name+".url")
		if err != nil {
			continue
		}
		sawURL = true
		slug, ok := parseGitHubRemote(url)
		if !ok {
			continue
		}
		if name == "origin" {
			return slug, nil
		}
		if fallback == "" {
			fallback = slug
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	if !sawURL {
		return "", appFail("this workspace has no git remotes")
	}
	return "", appFail("this workspace has no GitHub remote")
}

func safeRemoteName(name string) bool {
	if name == "" || len(name) > 80 || name[0] == '-' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '/':
		default:
			return false
		}
	}
	return true
}

func parseGitHubRemote(raw string) (string, bool) {
	raw = stripHTTPUserinfo(strings.TrimSpace(raw))
	raw = strings.TrimSuffix(raw, "/")
	raw = strings.TrimSuffix(raw, ".git")
	raw = strings.TrimSuffix(raw, "/")
	var path string
	switch {
	case strings.HasPrefix(raw, "https://github.com/"):
		path = strings.TrimPrefix(raw, "https://github.com/")
	case strings.HasPrefix(raw, "http://github.com/"):
		path = strings.TrimPrefix(raw, "http://github.com/")
	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		path = strings.TrimPrefix(raw, "ssh://git@github.com/")
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	default:
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", false
	}
	owner, name := parts[0], parts[1]
	if !validGitHubSlug(owner) || !validGitHubSlug(name) {
		return "", false
	}
	return owner + "/" + name, true
}

// stripHTTPUserinfo drops user:password@ from http(s) remotes so a
// stored credential cannot reach the browser, a prompt, or gh args.
func stripHTTPUserinfo(raw string) string {
	schemeLen := 0
	switch {
	case len(raw) >= len("https://") && strings.EqualFold(raw[:len("https://")], "https://"):
		schemeLen = len("https://")
	case len(raw) >= len("http://") && strings.EqualFold(raw[:len("http://")], "http://"):
		schemeLen = len("http://")
	default:
		return raw
	}
	rest := raw[schemeLen:]
	hostEnd := len(rest)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostEnd = i
	}
	if i := strings.IndexByte(rest[:hostEnd], '@'); i >= 0 {
		return raw[:schemeLen] + rest[i+1:]
	}
	return raw
}

func validGitHubSlug(s string) bool {
	if s == "" || len(s) > 100 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git: %s", redact.String(trimRunes(msg, 300)))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func ghChildEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, "GH_PROMPT_DISABLED=") || strings.HasPrefix(e, "GH_NO_UPDATE_NOTIFIER=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	return out
}

func runGH(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(ghBin); err != nil {
		return nil, appFail("gh is not installed on the daemon host")
	}
	cmd := exec.CommandContext(ctx, ghBin, args...)
	cmd.Dir = dir
	cmd.Env = ghChildEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, appFail("gh is not installed on the daemon host")
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, appFail("gh: " + trimRunes(msg, 400))
	}
	if stdout.Len() > 2<<20 {
		return nil, appFail("gh output too large")
	}
	return stdout.Bytes(), nil
}
