package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	// grepMaxMatches caps match lines returned (narrow pattern/include if truncated).
	grepMaxMatches  = 100
	grepMaxFileSize = 1 << 20 // 1MB per file scanned
	grepMaxLineLen  = 400
)

var grepSkipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "vendor": true,
	"dist": true, "build": true, "__pycache__": true,
}

type grepTool struct{}

func NewGrep() Tool { return grepTool{} }

func (grepTool) Name() string { return "grep" }

func (grepTool) Description() string {
	return `Fast content search tool that works with any codebase size.

- Searches file contents using regular expressions
- Supports full regex syntax (e.g. "log.*Error", "function\\s+\\w+")
- Filter files by pattern with the include parameter (e.g. "*.go", "*.{ts,tsx}")
- Returns file paths and line numbers with matching lines
- Use this tool when you need to find files containing specific patterns
- Prefer this tool over shell grep/rg for ordinary codebase search`
}

func (grepTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Regular expression (Go RE2 syntax)"},
			"path": {"type": "string", "description": "Directory to search in (default: working directory)"},
			"include": {"type": "string", "description": "Glob filter on filenames, e.g. \"*.go\" or \"**/*.ts\""}
		},
		"required": ["pattern"]
	}`)
}

type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}

func (grepTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a grepArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return Result{}, fmt.Errorf("invalid regex: %w", err)
	}
	base := tc.WorkDir
	if a.Path != "" {
		base = absPath(tc.WorkDir, a.Path)
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "grep", Patterns: []string{a.Pattern}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}

	var b strings.Builder
	matches := 0
	truncated := false
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if grepSkipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != base) {
				return fs.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		if a.Include != "" {
			if ok, _ := doublestar.Match(a.Include, rel); !ok {
				if ok, _ := doublestar.Match(a.Include, d.Name()); !ok {
					return nil
				}
			}
		}
		if info, err := d.Info(); err != nil || info.Size() > grepMaxFileSize {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		head := make([]byte, 512)
		n, _ := f.Read(head)
		if bytes.IndexByte(head[:n], 0) >= 0 {
			return nil // binary
		}
		if _, err := f.Seek(0, 0); err != nil {
			return nil
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), grepMaxFileSize)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if !re.MatchString(line) {
				continue
			}
			if len(line) > grepMaxLineLen {
				line = line[:grepMaxLineLen] + "…"
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", rel, lineNo, strings.TrimSpace(line))
			matches++
			if matches >= grepMaxMatches {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return Result{}, walkErr
	}
	out := fmt.Sprintf("%d match(es)", matches)
	if matches > 0 {
		out += ":\n" + b.String()
	}
	if truncated {
		out += fmt.Sprintf("(results truncated to %d; narrow the pattern or use include)\n", grepMaxMatches)
	}
	return Result{Title: a.Pattern, Output: out}, nil
}
