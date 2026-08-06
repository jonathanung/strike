package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateAgentsMDEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	body, err := GenerateAgentsMD(dir)
	if err != nil {
		t.Fatalf("GenerateAgentsMD: %v", err)
	}
	name := filepath.Base(dir)
	if !strings.Contains(body, "# "+name) {
		t.Fatalf("missing title:\n%s", body)
	}
	if !strings.Contains(body, "Project instructions for coding agents") {
		t.Fatalf("missing default blurb:\n%s", body)
	}
	if !strings.Contains(body, "## Notes for agents") {
		t.Fatalf("missing notes:\n%s", body)
	}
	if strings.Contains(body, "## Layout") {
		t.Fatalf("empty repo should not invent layout:\n%s", body)
	}
}

func TestGenerateAgentsMDDetectsGoStackAndMakefile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("go.mod", "module github.com/example/demo\n\ngo 1.26\n")
	write("Makefile", "test:\n\tgo test ./...\n\nvet:\n\tgo vet ./...\n\nbuild:\n\tgo build ./...\n")
	write("README.md", "# Demo\n\nA tiny demo service.\n")
	write(".env", "SECRET=should-not-appear\n")
	write(".gitignore", "node_modules\n.env\n")

	body, err := GenerateAgentsMD(dir)
	if err != nil {
		t.Fatalf("GenerateAgentsMD: %v", err)
	}
	for _, want := range []string{
		"A tiny demo service.",
		"Go module `github.com/example/demo`",
		"Make",
		"`cmd/`",
		"`internal/`",
		"make test",
		"make vet",
		"make build",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// Layout section must not list ignored/secret paths (notes may mention .env).
	layout := body
	if i := strings.Index(body, "## Layout"); i >= 0 {
		layout = body[i:]
		if j := strings.Index(layout[2:], "## "); j >= 0 {
			layout = layout[:j+2]
		}
	}
	for _, bad := range []string{"node_modules", ".git", ".env", "SECRET="} {
		if strings.Contains(layout, bad) {
			t.Errorf("layout leaked %q in:\n%s", bad, layout)
		}
	}
	if strings.Contains(body, "SECRET=") {
		t.Errorf("secret value leaked:\n%s", body)
	}
}

func TestWriteAgentsMDCreateAndConfirm(t *testing.T) {
	dir := t.TempDir()
	path, created, err := WriteAgentsMD(dir, false)
	if err != nil {
		t.Fatalf("WriteAgentsMD create: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if path != AgentsMDPath(dir) {
		t.Fatalf("path = %q, want %q", path, AgentsMDPath(dir))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## Notes for agents") {
		t.Fatalf("written body incomplete:\n%s", data)
	}

	_, _, err = WriteAgentsMD(dir, false)
	if !errors.Is(err, ErrAgentsExists) {
		t.Fatalf("second write without force err = %v, want ErrAgentsExists", err)
	}
	// Unchanged after refused overwrite.
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(data) {
		t.Fatal("file changed after refused overwrite")
	}

	// Force replace.
	if err := os.WriteFile(path, []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path2, created2, err := WriteAgentsMD(dir, true)
	if err != nil {
		t.Fatalf("force write: %v", err)
	}
	if created2 {
		t.Fatal("created = true on replace, want false")
	}
	if path2 != path {
		t.Fatalf("path = %q, want %q", path2, path)
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(final), "# old") {
		t.Fatal("force write did not replace")
	}
	if !strings.Contains(string(final), "## Notes for agents") {
		t.Fatalf("force body incomplete:\n%s", final)
	}
}

func TestAgentsMDExistsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(AgentsMDPath(dir), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Size > 0 even if whitespace — treat as exists so we confirm before clobber.
	ok, err := AgentsMDExists(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("whitespace file should count as exists")
	}
}

func TestScrubSecretSpans(t *testing.T) {
	// ghp_ needs ≥20 body chars; password assignment needs ≥6 value chars.
	in := "token sk-ant-api03-SUPERSECRETKEYVALUE and ghp_ABCDEFGHIJKLMNOPQRSTUV password=hunter2"
	out := scrubSecretSpans(in)
	if strings.Contains(out, "SUPERSECRETKEYVALUE") || strings.Contains(out, "ABCDEFGHIJKLMNOPQRSTUV") || strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Fatalf("expected redaction placeholder: %q", out)
	}
}

func TestGenerateAgentsMDSkipsSecretNamedEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"k":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := GenerateAgentsMD(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "credentials.json") {
		t.Fatalf("credentials file listed:\n%s", body)
	}
	if !strings.Contains(body, "`src/`") {
		t.Fatalf("src missing:\n%s", body)
	}
}
