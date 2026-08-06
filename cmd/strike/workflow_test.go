package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCLIDispatchesWorkflowHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"workflow", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "strike workflow scaffold") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunWorkflowCLIUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWorkflowScaffoldRequiresScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"scaffold", "demo"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--global or --project") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWorkflowScaffoldProjectAndNoActivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	t.Chdir(work)

	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"scaffold", "--project", "demo-flow"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	path := filepath.Join(work, ".strike", "workflows", "demo-flow.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "not activated") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schemaVersion": 1`) {
		t.Fatalf("content = %s", data)
	}
	if strings.Contains(string(data), `"active"`) {
		t.Fatal("scaffold must not mark active")
	}

	// Overwrite refused without --force.
	stdout.Reset()
	stderr.Reset()
	code = runWorkflowCLI([]string{"scaffold", "--project", "demo-flow"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}

	// --force overwrites.
	stdout.Reset()
	stderr.Reset()
	code = runWorkflowCLI([]string{"scaffold", "--project", "demo-flow", "--force"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("force code=%d stderr=%q", code, stderr.String())
	}
}

func TestWorkflowScaffoldGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"scaffold", "--global", "g-flow"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	path := filepath.Join(home, ".strike", "workflows", "g-flow.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowFormatRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	// Compact valid JSON.
	raw := `{"name":"fmt","phases":[{"name":"a","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"format", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"schemaVersion": 1`) || !strings.HasSuffix(out, "\n") {
		t.Fatalf("formatted = %q", out)
	}

	stdout.Reset()
	stderr.Reset()
	code = runWorkflowCLI([]string{"format", "--write", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("write code=%d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != out {
		t.Fatalf("write mismatch:\n%s\n---\n%s", data, out)
	}
}

func TestWorkflowValidateOKAndUnknownField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	t.Chdir(work)
	dir := filepath.Join(work, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := `{"schemaVersion":1,"name":"good","phases":[{"name":"a","agent":"build","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"validate", filepath.Join(dir, "good.json")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "ok:") || !strings.Contains(stdout.String(), "fingerprint=") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	bad := `{"name":"bad","nope":true,"phases":[{"name":"a","exit":{"type":"agent"}}]}`
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = runWorkflowCLI([]string{"validate", badPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWorkflowValidateUnknownAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	t.Chdir(work)
	path := filepath.Join(work, "wf.json")
	body := `{"name":"x","phases":[{"name":"a","agent":"no-such-agent-xyz","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"validate", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown agent") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestWorkflowValidateProjectFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	t.Chdir(work)
	dir := filepath.Join(work, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"p","phases":[{"name":"a","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "p.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"validate", "--project"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "validated 1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWorkflowValidateBothScopesRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWorkflowCLI([]string{"validate", "--global", "--project"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestMainUsageListsWorkflow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), "strike workflow") {
		t.Fatalf("usage missing workflow: %q", stdout.String())
	}
}
