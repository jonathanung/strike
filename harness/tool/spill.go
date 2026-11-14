package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike/pkg/redact"
)

// Oversized tool output spill (#1199): when bash / grep / webfetch exceed
// their produce-time cap, the model-facing Result.Output keeps the capped
// preview and the redacted remainder lands in session scratch so the model
// can page it with read offset/limit instead of losing it.
const (
	// spillSubdir is the directory under Context.SessionTempDir holding spills.
	spillSubdir = "tool-output"
	// spillMaxBytes caps one spill file (redacted body is truncated past this).
	spillMaxBytes = 4 << 20
	// spillDirMaxBytes caps all spill files in one session; past it spilling
	// degrades to today's truncate-and-discard.
	spillDirMaxBytes = 64 << 20
	// SpillHintPrefix starts the reopen hint appended to a truncated preview.
	// The engine parses it (SpillPathFromOutput) to keep the path after prune.
	SpillHintPrefix = "(full output saved to "
	spillHintSuffix = "; use read with offset/limit to page through it)"
)

// spillOverflow writes the redacted, size-capped full output for toolName to
// session scratch and returns the reopen hint line to append to the preview.
// Returns "" when there is nothing to spill or scratch is unavailable — the
// caller keeps its existing truncate-and-discard behavior (non-fatal).
func spillOverflow(tc *Context, toolName, full string) string {
	if tc == nil || strings.TrimSpace(tc.SessionTempDir) == "" || full == "" {
		return ""
	}
	path, err := writeSpill(tc.SessionTempDir, toolName, full)
	if err != nil || path == "" {
		return ""
	}
	return SpillHintPrefix + path + spillHintSuffix
}

func writeSpill(tempDir, toolName, full string) (string, error) {
	body := redact.ScrubToolOutput(full)
	if len(body) > spillMaxBytes {
		cut := spillMaxBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + fmt.Sprintf("\n… (spill truncated at %d bytes)\n", cut)
	}
	dir := filepath.Join(tempDir, spillSubdir)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("spill dir %q is not a directory", dir)
	}
	sum := sha256.Sum256([]byte(body))
	name := sanitizeSessionTempID(toolName)
	if name == "" {
		name = "tool"
	}
	path := filepath.Join(dir, name+"-"+hex.EncodeToString(sum[:8])+".txt")
	// Content-addressed: an identical spill already on disk is reused.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().IsRegular() && fi.Size() == int64(len(body)) {
			return path, nil
		}
		return "", fmt.Errorf("spill path %q exists with unexpected type or size", path)
	}
	if spillDirUsage(dir)+int64(len(body)) > spillDirMaxBytes {
		return "", fmt.Errorf("session spill budget exhausted")
	}
	// O_EXCL refuses pre-planted files and symlinks at the leaf.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func spillDirUsage(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, ent := range entries {
		if info, err := ent.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// SpillPathFromOutput returns the spill file path named by the last reopen
// hint in a tool output, or "" when the output carries no hint.
func SpillPathFromOutput(output string) string {
	i := strings.LastIndex(output, SpillHintPrefix)
	if i < 0 {
		return ""
	}
	rest := output[i+len(SpillHintPrefix):]
	j := strings.Index(rest, spillHintSuffix)
	if j <= 0 {
		return ""
	}
	return rest[:j]
}

// IsSpillPath reports whether path is a spill file directly under
// sessionTempDir's spill subdirectory (guards against hint text forged by
// command output naming arbitrary paths).
func IsSpillPath(sessionTempDir, path string) bool {
	if strings.TrimSpace(sessionTempDir) == "" || path == "" {
		return false
	}
	return filepath.Dir(filepath.Clean(path)) == filepath.Join(sessionTempDir, spillSubdir)
}
