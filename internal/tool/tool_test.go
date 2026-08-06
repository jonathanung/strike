package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func allowAll(workDir string) *Context {
	return &Context{
		WorkDir: workDir,
		Ask:     func(context.Context, AskRequest) error { return nil },
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRegistry(t *testing.T) {
	r := NewRegistry(NewRead(), NewBash())
	if _, ok := r.Get("read"); !ok {
		t.Fatal("missing read")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("expected missing")
	}
	schemas := r.Schemas()
	if len(schemas) != 2 || schemas[0].Name != "read" || schemas[1].Name != "bash" {
		t.Fatalf("schemas = %+v", schemas)
	}
	// Re-register replaces but keeps order.
	r.Register(NewRead())
	if len(r.Schemas()) != 2 {
		t.Fatalf("len after re-register = %d", len(r.Schemas()))
	}
}

func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	content := "line1\nline2\n" + strings.Repeat("x", 2500) + "\nline4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	res, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.txt",
		"offset":   2,
		"limit":    2,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2\tline2") {
		t.Errorf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "…") {
		t.Errorf("expected long line truncation, got %q", res.Output)
	}

	_, err = NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.txt",
		"offset":   99,
	}), tc)
	if err == nil {
		t.Fatal("expected offset error")
	}

	_, err = NewRead().Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("expected invalid args")
	}
}

func TestReadPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := &Context{
		WorkDir: dir,
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{"filePath": "a.txt"}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestWriteAndEdit(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)

	res, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "sub/b.txt",
		"content":  "hello world",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Created") {
		t.Errorf("output = %q", res.Output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub", "b.txt"))
	if err != nil || string(data) != "hello world" {
		t.Fatalf("file = %q err=%v", data, err)
	}

	res, err = NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "sub/b.txt",
		"content":  "hello world\nhello again",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Overwrote") {
		t.Errorf("output = %q", res.Output)
	}

	_, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "sub/b.txt",
		"oldString": "hello",
		"newString": "hello",
	}), tc)
	if err == nil {
		t.Fatal("expected identical strings error")
	}

	_, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "sub/b.txt",
		"oldString": "hello",
		"newString": "hi",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "matches") {
		t.Fatalf("expected non-unique error, got %v", err)
	}

	res, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":   "sub/b.txt",
		"oldString":  "hello",
		"newString":  "hi",
		"replaceAll": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2 replacement") {
		t.Errorf("output = %q", res.Output)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "sub", "b.txt"))
	if string(data) != "hi world\nhi again" {
		t.Errorf("content = %q", data)
	}

	_, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "sub/b.txt",
		"oldString": "missing",
		"newString": "x",
	}), tc)
	if err == nil {
		t.Fatal("expected not found")
	}
}

func TestFileSyncCallback(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	type syncEv struct {
		path    string
		content string
		deleted bool
	}
	var got []syncEv
	tc.FileSync = func(absPath, content string, deleted bool) {
		got = append(got, syncEv{path: absPath, content: content, deleted: deleted})
	}

	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"content":  "package a\n",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].deleted || got[0].content != "package a\n" {
		t.Fatalf("write sync = %#v", got)
	}
	if got[0].path != filepath.Join(dir, "a.go") {
		t.Fatalf("path = %q", got[0].path)
	}

	_, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.go",
		"oldString": "package a\n",
		"newString": "package b\n",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].content != "package b\n" {
		t.Fatalf("edit sync = %#v", got)
	}

	// Panic in callback must not fail the tool.
	tc.FileSync = func(string, string, bool) { panic("boom") }
	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "c.go",
		"content":  "x",
	}), tc); err != nil {
		t.Fatalf("panic should be recovered: %v", err)
	}
}

func TestGlobAndGrep(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("hello Foo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)

	res, err := NewGlob().Execute(context.Background(), mustJSON(t, map[string]any{
		"pattern": "**/*",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "a.go") || !strings.Contains(res.Output, "b.txt") {
		t.Errorf("glob output = %q", res.Output)
	}

	res, err = NewGrep().Execute(context.Background(), mustJSON(t, map[string]any{
		"pattern": "Foo",
		"include": "*.go",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "a.go") {
		t.Errorf("grep output = %q", res.Output)
	}
	if strings.Contains(res.Output, "b.txt") {
		t.Errorf("include filter failed: %q", res.Output)
	}

	_, err = NewGrep().Execute(context.Background(), mustJSON(t, map[string]any{
		"pattern": "(",
	}), tc)
	if err == nil {
		t.Fatal("expected invalid regex")
	}
}

func TestBashTool(t *testing.T) {
	dir := t.TempDir()
	var sawAlways []string
	tc := &Context{
		WorkDir: dir,
		Ask: func(_ context.Context, req AskRequest) error {
			sawAlways = req.Always
			return nil
		},
	}
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo hello-tool && true",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "hello-tool") {
		t.Errorf("output = %q", res.Output)
	}
	if len(sawAlways) != 1 || sawAlways[0] != "echo *" {
		t.Errorf("always = %#v, want echo *", sawAlways)
	}

	res, err = NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "exit 7",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "exit code 7") {
		t.Errorf("output = %q", res.Output)
	}

	_, err = NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "   ",
	}), tc)
	if err == nil {
		t.Fatal("expected empty command error")
	}

	// timeout path
	res, err = NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command":   "sleep 2",
		"timeoutMs": 50,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "timed out") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestBashToolStreamsReportOutput(t *testing.T) {
	dir := t.TempDir()
	var (
		mu     sync.Mutex
		chunks []string
	)
	tc := &Context{
		WorkDir: dir,
		Ask:     func(context.Context, AskRequest) error { return nil },
		ReportOutput: func(data string) {
			mu.Lock()
			chunks = append(chunks, data)
			mu.Unlock()
		},
	}
	// Line-buffered stdio with delays so ReportOutput fires before Execute returns.
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "printf 'line1\\n'; sleep 0.05; printf 'line2\\n'; sleep 0.05; printf 'line3\\n'",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "line1") || !strings.Contains(res.Output, "line3") {
		t.Fatalf("final output = %q", res.Output)
	}
	mu.Lock()
	joined := strings.Join(chunks, "")
	mu.Unlock()
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") || !strings.Contains(joined, "line3") {
		t.Fatalf("streamed chunks = %#v (joined %q)", chunks, joined)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one ReportOutput chunk")
	}
}

func TestToolNames(t *testing.T) {
	reg := NewRegistry()
	ts := NewToolSearch(reg)
	store := NewTodoStore()
	mem := openMemory(t)
	iss := openIssue(t)
	want := map[string]Tool{
		"read":            NewRead(),
		"write":           NewWrite(),
		"edit":            NewEdit(),
		"move":            NewMove(),
		"delete":          NewDelete(),
		"glob":            NewGlob(),
		"grep":            NewGrep(),
		"bash":            NewBash(),
		"webfetch":        NewWebFetch(),
		"websearch":       NewWebSearch(),
		"todowrite":       NewTodoWrite(store),
		"todoread":        NewTodoRead(store),
		"memory_write":    NewMemoryWrite(mem),
		"memory_read":     NewMemoryRead(mem),
		"issue_write":     NewIssueWrite(iss),
		"issue_read":      NewIssueRead(iss),
		"plan_write":      NewPlanWrite(openPlan(t)),
		"plan_read":       NewPlanRead(openPlan(t)),
		"plan_delegate":   NewPlanDelegate(openPlan(t)),
		"notebook_edit":   NewNotebookEdit(),
		"sleep":           NewSleep(),
		"skill":           NewSkill(nil),
		"toolsearch":      ts,
		"question":        NewQuestion(),
		"apply_patch":     NewApplyPatch(),
		"enter_plan_mode": NewEnterPlanMode(),
		"exit_plan_mode":  NewExitPlanMode(),
		"phase_done":      NewPhaseDone(),
		"task_status":     NewTaskStatus(),
		"task_read":       NewTaskRead(),
		"task_message":    NewTaskMessage(),
		"task_interrupt":  NewTaskInterrupt(),
		"agent_roster":    NewAgentRoster(),
		"agent_ownership": NewAgentOwnership(),
		"agent_message":   NewAgentMessage(),
		"agent_broadcast": NewAgentBroadcast(),
		"agent_thread":    NewAgentThread(),
		"team_task":       NewTeamTask(),
		"patch_collab":    NewPatchCollab(),
		"delegate":        NewDelegate(),
		"wait":            NewWait(),
	}
	if len(want) != 41 {
		t.Fatalf("expected 41 tools, got %d", len(want))
	}
	for name, tool := range want {
		if tool.Name() != name {
			t.Errorf("Name() = %q, want %q", tool.Name(), name)
		}
		if len(tool.Schema()) == 0 || tool.Description() == "" {
			t.Errorf("%s missing schema/description", name)
		}
	}
}

func TestAppendDiagnostics(t *testing.T) {
	tc := allowAll(t.TempDir())
	res := Result{Output: "Edited a.go (1 replacement(s))"}
	// nil CollectDiagnostics is a no-op
	got := tc.AppendDiagnostics(context.Background(), res, "/tmp/a.go")
	if got.Output != res.Output {
		t.Fatalf("nil collect changed output: %q", got.Output)
	}

	var saw []string
	tc.CollectDiagnostics = func(ctx context.Context, absPaths []string) string {
		saw = append([]string(nil), absPaths...)
		return "--- diagnostics ---\na.go:1:1: error: boom"
	}
	got = tc.AppendDiagnostics(context.Background(), res, "/abs/a.go", "", "/abs/b.go")
	if len(saw) != 2 || saw[0] != "/abs/a.go" || saw[1] != "/abs/b.go" {
		t.Fatalf("paths = %#v", saw)
	}
	if !strings.Contains(got.Output, "Edited a.go") || !strings.Contains(got.Output, "--- diagnostics ---") {
		t.Fatalf("output = %q", got.Output)
	}
	if !strings.Contains(got.Output, "\n\n--- diagnostics ---") {
		t.Fatalf("want blank line before block: %q", got.Output)
	}

	// Panic must not fail the tool path.
	tc.CollectDiagnostics = func(context.Context, []string) string { panic("boom") }
	got = tc.AppendDiagnostics(context.Background(), res, "/x")
	if got.Output != res.Output {
		t.Fatalf("panic changed output: %q", got.Output)
	}
}

func TestWriteEditInjectDiagnostics(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	var collectCalls int
	var collectPaths [][]string
	tc.CollectDiagnostics = func(ctx context.Context, absPaths []string) string {
		collectCalls++
		collectPaths = append(collectPaths, append([]string(nil), absPaths...))
		return "--- diagnostics ---\nx:1:1: error: e"
	}
	tc.FileSync = func(string, string, bool) {} // present but no-op

	res, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"content":  "package a\n",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if collectCalls != 1 {
		t.Fatalf("collectCalls=%d", collectCalls)
	}
	if !strings.Contains(res.Output, "Created a.go") || !strings.Contains(res.Output, "--- diagnostics ---") {
		t.Fatalf("write output = %q", res.Output)
	}

	res, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.go",
		"oldString": "package a\n",
		"newString": "package b\n",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if collectCalls != 2 {
		t.Fatalf("collectCalls=%d after edit", collectCalls)
	}
	if !strings.Contains(res.Output, "Edited a.go") || !strings.Contains(res.Output, "error: e") {
		t.Fatalf("edit output = %q", res.Output)
	}
}

func TestApplyPatchDiagnosticsSingleCollect(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	var collectCalls int
	var nPaths int
	tc.CollectDiagnostics = func(ctx context.Context, absPaths []string) string {
		collectCalls++
		nPaths = len(absPaths)
		return "--- diagnostics ---\nmulti"
	}
	tc.FileSync = func(string, string, bool) {}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: a.go",
		"+package a",
		"*** Add File: b.go",
		"+package b",
		"*** End Patch",
	}, "\n")
	res, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if collectCalls != 1 {
		t.Fatalf("collectCalls=%d, want 1 (debounced)", collectCalls)
	}
	if nPaths != 2 {
		t.Fatalf("nPaths=%d, want 2", nPaths)
	}
	if strings.Count(res.Output, "--- diagnostics ---") != 1 {
		t.Fatalf("output = %q", res.Output)
	}
}
