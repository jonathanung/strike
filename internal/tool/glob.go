package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
)

const globMaxResults = 200

type globTool struct{}

func NewGlob() Tool { return globTool{} }

func (globTool) Name() string { return "glob" }

func (globTool) Description() string {
	return "Find files by name pattern. Supports ** for recursive matching, e.g. \"**/*.go\" or \"internal/**/*_test.go\"."
}

func (globTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob pattern to match file paths against"},
			"path": {"type": "string", "description": "Directory to search in (default: working directory)"}
		},
		"required": ["pattern"]
	}`)
}

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (globTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a globArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	base := tc.WorkDir
	if a.Path != "" {
		base = absPath(tc.WorkDir, a.Path)
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "glob", Patterns: []string{a.Pattern}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	matches, err := doublestar.Glob(os.DirFS(base), a.Pattern)
	if err != nil {
		return Result{}, fmt.Errorf("invalid glob pattern: %w", err)
	}
	var files []string
	for _, m := range matches {
		full := filepath.Join(base, m)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			files = append(files, m)
		}
	}
	sort.Strings(files)
	truncated := false
	if len(files) > globMaxResults {
		files = files[:globMaxResults]
		truncated = true
	}
	out := fmt.Sprintf("%d file(s) matched", len(files))
	if len(files) > 0 {
		out += ":\n"
		for _, f := range files {
			out += f + "\n"
		}
	}
	if truncated {
		out += fmt.Sprintf("(results truncated to %d)\n", globMaxResults)
	}
	return Result{Title: a.Pattern, Output: out}, nil
}
