package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunEvalCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "swebench") {
		t.Fatalf("usage: %s", stdout.String())
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

func TestRunEvalUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runEvalCLI([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code %d", code)
	}
}
