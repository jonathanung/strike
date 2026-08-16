package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseSeverityName(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", SeverityError, false},
		{"error", SeverityError, false},
		{"WARNING", SeverityWarning, false},
		{"info", SeverityInformation, false},
		{"hint", SeverityHint, false},
		{"nope", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseSeverityName(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("%q: want err", tt.in)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("%q: got %d %v, want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestFormatDiagLineAndSeverityFilter(t *testing.T) {
	d := Diagnostic{
		Range:    Range{Start: Position{Line: 2, Character: 4}},
		Severity: SeverityWarning,
		Source:   "gopls",
		Message:  "unused",
		Code:     "U1000",
	}
	line := formatDiagLine("a.go", d)
	if !strings.Contains(line, "a.go:3:5: warning: unused [gopls] (U1000)") {
		t.Fatalf("line = %q", line)
	}
	if includeSeverity(SeverityWarning, SeverityError) {
		t.Fatal("warning should be filtered when min=error")
	}
	if !includeSeverity(SeverityError, SeverityWarning) {
		t.Fatal("error should pass when min=warning")
	}
	if !includeSeverity(0, SeverityError) {
		t.Fatal("omitted severity treated as error")
	}
}

func TestFormatDiagnosticBlockCapAndFilter(t *testing.T) {
	entries := []pathDiags{
		{path: "a.go", diags: []Diagnostic{
			{Severity: SeverityError, Message: "err-a", Range: Range{Start: Position{Line: 0, Character: 0}}},
			{Severity: SeverityWarning, Message: "warn-a", Range: Range{Start: Position{Line: 1, Character: 0}}},
		}},
		{path: "b.go", diags: []Diagnostic{
			{Severity: SeverityError, Message: "err-b", Range: Range{Start: Position{Line: 0, Character: 0}}},
		}},
	}
	block := formatDiagnosticBlock(entries, InjectOptions{
		MinSeverity: SeverityError, MaxChars: 4000,
	}.Normalize())
	if !strings.HasPrefix(block, "--- diagnostics ---") {
		t.Fatalf("block = %q", block)
	}
	if strings.Count(block, "--- diagnostics ---") != 1 {
		t.Fatalf("want one header: %q", block)
	}
	if !strings.Contains(block, "err-a") || !strings.Contains(block, "err-b") {
		t.Fatalf("missing errors: %q", block)
	}
	if strings.Contains(block, "warn-a") {
		t.Fatalf("warning leaked at error filter: %q", block)
	}

	withWarn := formatDiagnosticBlock(entries, InjectOptions{
		MinSeverity: SeverityWarning, MaxChars: 4000,
	}.Normalize())
	if !strings.Contains(withWarn, "warn-a") {
		t.Fatalf("warning missing when opted in: %q", withWarn)
	}

	// Header is ~20 runes; one line ~30 — budget 45 keeps header+one line, truncates rest.
	tiny := formatDiagnosticBlock(entries, InjectOptions{
		MinSeverity: SeverityError, MaxChars: 55,
	}.Normalize())
	if utf8.RuneCountInString(tiny) > 55 {
		t.Fatalf("over budget: %d %q", utf8.RuneCountInString(tiny), tiny)
	}
	if !strings.Contains(tiny, "truncated") {
		t.Fatalf("want truncation: %q", tiny)
	}
}

func TestCollectForPathsSingleBlock(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()
	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env, RootDir: dir, Extensions: []string{".go"},
	}})

	p1 := filepath.Join(dir, "a.go")
	p2 := filepath.Join(dir, "b.go")
	m.NotifyFile(ctx, p1, "package a\nERR\n", false)
	m.NotifyFile(ctx, p2, "package b\nERR\n", false)

	block := m.CollectForPaths(ctx, []string{p1, p2}, InjectOptions{
		WorkDir:     dir,
		MinSeverity: SeverityError,
		MaxChars:    4000,
		Wait:        2 * time.Second,
	})
	if block == "" {
		t.Fatal("expected diagnostics block")
	}
	if strings.Count(block, "--- diagnostics ---") != 1 {
		t.Fatalf("want one header, got %q", block)
	}
	if !strings.Contains(block, "a.go:") || !strings.Contains(block, "b.go:") {
		t.Fatalf("missing paths: %q", block)
	}
	if !strings.Contains(block, "error: found ERR") {
		t.Fatalf("missing message: %q", block)
	}
}

func TestCollectForPathsNilAndEmpty(t *testing.T) {
	var m *Manager
	if m.CollectForPaths(context.Background(), []string{"/x"}, InjectOptions{}) != "" {
		t.Fatal("nil manager")
	}
	m = NewManager(t.TempDir())
	if m.CollectForPaths(context.Background(), nil, InjectOptions{Wait: -1}) != "" {
		t.Fatal("empty paths")
	}
}

func TestCollectForPathsNoStaleAfterNotify(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	m := NewManager(dir)
	defer m.Close()
	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env, RootDir: dir, Extensions: []string{".go"},
	}})

	path := filepath.Join(dir, "main.go")
	m.NotifyFile(ctx, path, "ERR\n", false)
	block := m.CollectForPaths(ctx, []string{path}, InjectOptions{WorkDir: dir, Wait: 2 * time.Second})
	if !strings.Contains(block, "found ERR") {
		t.Fatalf("first = %q", block)
	}

	// Fix content: should not keep stale ERR after wait.
	m.NotifyFile(ctx, path, "package main\n", false)
	block = m.CollectForPaths(ctx, []string{path}, InjectOptions{WorkDir: dir, Wait: 2 * time.Second})
	if strings.Contains(block, "found ERR") {
		t.Fatalf("stale diagnostics after fix: %q", block)
	}
}

func TestInjectOptionsNormalize(t *testing.T) {
	o := InjectOptions{}.Normalize()
	if o.MinSeverity != DefaultInjectMinSeverity || o.MaxChars != DefaultInjectMaxChars || o.Wait != DefaultInjectWait {
		t.Fatalf("%#v", o)
	}
}
