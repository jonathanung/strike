package local

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
)

const (
	maxFileBytes   = 1 << 20 // 1 MiB
	maxListEntries = 2000
)

// filesService reads workspace files for host.Files frontends.
type filesService struct {
	workDir string
}

// NewFiles returns a host.Files that resolves paths relative to workDir
// (absolute paths are cleaned as-is). Path traversal outside workDir is
// allowed for user-initiated reads, matching the read tool.
func NewFiles(workDir string) host.Files {
	return filesService{workDir: workDir}
}

func (f filesService) ReadFile(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("path is empty")
	}
	resolved := absPath(f.workDir, path)
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("file exceeds 1MB limit")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// LimitReader guards TOCTOU growth between Stat and read.
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxFileBytes {
		return nil, fmt.Errorf("file exceeds 1MB limit")
	}
	return data, nil
}

func (f filesService) ListDir(path string) ([]host.DirEntry, error) {
	path = strings.TrimSpace(path)
	var resolved string
	if path == "" {
		if f.workDir == "" {
			return nil, fmt.Errorf("path is empty")
		}
		resolved = filepath.Clean(f.workDir)
	} else {
		resolved = absPath(f.workDir, path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			if path == "" {
				return nil, fmt.Errorf("directory not found: %s", resolved)
			}
			return nil, fmt.Errorf("directory not found: %s", path)
		}
		return nil, err
	}
	if !info.IsDir() {
		if path == "" {
			return nil, fmt.Errorf("not a directory: %s", resolved)
		}
		return nil, fmt.Errorf("not a directory: %s", path)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]host.DirEntry, 0, len(entries))
	for _, e := range entries {
		// Skip the synthetic . and .. entries if present.
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		out = append(out, host.DirEntry{Name: name, IsDir: e.IsDir()})
		if len(out) >= maxListEntries {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func absPath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(workDir, p)
}
