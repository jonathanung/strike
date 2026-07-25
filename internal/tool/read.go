package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	readDefaultLimit = 2000
	readMaxLineLen   = 2000
)

type readTool struct{}

func NewRead() Tool { return readTool{} }

func (readTool) Name() string { return "read" }

func (readTool) Description() string {
	return `Read a file from the local filesystem. If the path does not exist, an error is returned.

Usage:
- filePath may be absolute or relative to the working directory.
- By default, this tool returns up to 2000 lines from the start of the file.
- offset is the 1-indexed line to start from; use a larger offset to continue.
- Use the grep tool to find specific content in large files or files with long lines.
- If you are unsure of the correct file path, use the glob tool to look up filenames by pattern.
- Contents are returned with each line prefixed by its line number. Any line longer than 2000 characters is truncated.
- Call this tool in parallel when you know there are multiple files you want to read.
- Avoid tiny repeated slices (30 line chunks). If you need more context, read a larger window.`
}

func (readTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"offset": {"type": "integer", "description": "1-indexed line to start reading from"},
			"limit": {"type": "integer", "description": "Maximum number of lines to read (default 2000)"}
		},
		"required": ["filePath"]
	}`)
}

type readArgs struct {
	FilePath string `json:"filePath"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func (readTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	path := absPath(tc.WorkDir, a.FilePath)
	if err := tc.Ask(ctx, AskRequest{Permission: "read", Patterns: []string{relPath(tc.WorkDir, path)}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		tc.Files.Record(path, info)
	}
	lines := strings.Split(string(data), "\n")
	total := len(lines)
	offset := max(a.Offset, 1)
	limit := a.Limit
	if limit <= 0 {
		limit = readDefaultLimit
	}
	if offset > total {
		return Result{}, fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, total)
	}
	end := min(offset-1+limit, total)
	var b strings.Builder
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > readMaxLineLen {
			line = line[:readMaxLineLen] + "…"
		}
		fmt.Fprintf(&b, "%d\t%s\n", i+1, line)
	}
	if end < total {
		fmt.Fprintf(&b, "(%d more lines not shown; continue with offset=%d)\n", total-end, end+1)
	}
	meta, _ := json.Marshal(map[string]any{"lineStart": offset, "lineEnd": end, "totalLines": total})
	return Result{
		Title:    relPath(tc.WorkDir, path),
		Output:   b.String(),
		Metadata: meta,
	}, nil
}

func absPath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(workDir, p)
}

func relPath(workDir, p string) string {
	if rel, err := filepath.Rel(workDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}
