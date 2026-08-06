package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEvalCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "swebench") || !strings.Contains(out, "tbench") || !strings.Contains(out, "sweep") {
		t.Fatalf("usage: %s", out)
	}
}

func TestRunEvalSWEBenchSubsetOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"swebench", "--subset-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines", len(lines))
	}
}

func TestRunEvalSWEBenchDryRun(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{
		"swebench",
		"--dry-run",
		"--limit", "2",
		"--out", dir,
		"--run-id", "test-dry",
		"--grader", "none",
		"--provider", "echo",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "report.json") {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestRunEvalTBenchSubsetOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"tbench", "--subset-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 25 {
		t.Fatalf("got %d lines", len(lines))
	}
}

func TestRunEvalTBenchDryRun(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{
		"tbench",
		"--dry-run",
		"--limit", "2",
		"--out", dir,
		"--run-id", "test-tb-dry",
		"--grader", "none",
		"--provider", "echo",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "report.json") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Terminal-Bench") {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestRunEvalTBenchWithTasksDir(t *testing.T) {
	// Point at package testdata as a mini pack (fixture-task only).
	// Use source-relative path via test file location.
	pack := filepath.Join("..", "..", "internal", "eval", "tbench", "testdata")
	// When tests run from cmd/strike, that path works; also try module-relative.
	if _, err := os.Stat(filepath.Join(pack, "fixture-task")); err != nil {
		pack = filepath.Join("internal", "eval", "tbench", "testdata")
	}
	if _, err := os.Stat(filepath.Join(pack, "fixture-task")); err != nil {
		t.Skip("fixture pack not found from test cwd")
	}
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{
		"tbench",
		"--dry-run",
		"--tasks-dir", pack,
		"--instance", "fixture-task",
		"--out", dir,
		"--run-id", "fixture-dry",
		"--grader", "none",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
}

func TestRunEvalUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code %d", code)
	}
}

func TestRunEvalSweepListPoints(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"sweep", "--matrix", "leanCode", "--list-points"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, id := range []string{"leanCode-off", "leanCode-lite", "leanCode-full"} {
		if !strings.Contains(out, id) {
			t.Fatalf("missing %s in %s", id, out)
		}
	}
}

func TestRunEvalSweepDryRun(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{
		"sweep",
		"--benchmark", "swebench",
		"--matrix", "deferTools",
		"--dry-run",
		"--limit", "1",
		"--out", dir,
		"--run-id", "sweep-dry",
		"--grader", "none",
		"--provider", "echo",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deferTools-off", "report.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deferTools-on", "report.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "deferTools-off") {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestRunEvalSweepUnknownMatrix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"sweep", "--matrix", "nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code %d", code)
	}
}
