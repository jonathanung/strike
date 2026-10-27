package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const fingerprintCap = 4000

// Fingerprint is a stable digest of dir. A git checkout uses HEAD plus
// porcelain status so unstaged edits count and the walk stays cheap.
// Anything else hashes names, sizes, and mtimes, skipping dependency
// and VCS object trees.
func Fingerprint(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("fingerprint: empty dir")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fingerprint: %s is not a directory", dir)
	}
	if gitOK(dir) {
		if sum, err := gitFingerprint(dir); err == nil {
			return sum, nil
		}
	}
	return walkFingerprint(dir)
}

func gitOK(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func gitFingerprint(dir string) (string, error) {
	head, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := gitOutput(dir, "status", "--porcelain", "-uall")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = io.WriteString(h, head)
	_, _ = io.WriteString(h, "\n")
	_, _ = io.WriteString(h, status)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	timer := time.AfterFunc(5*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func contentPrefix(path string, size int64) string {
	if size > 256*1024 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	sum := sha256.Sum256(buf[:n])
	return hex.EncodeToString(sum[:8])
}

func walkFingerprint(dir string) (string, error) {
	type row struct {
		rel  string
		size int64
		mod  int64
	}
	var rows []row
	truncated := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "node_modules", "target", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if len(rows) >= fingerprintCap {
			truncated = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		sum := contentPrefix(path, info.Size())
		rows = append(rows, row{rel: filepath.ToSlash(rel) + " " + sum, size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].rel < rows[j].rel })
	h := sha256.New()
	for _, r := range rows {
		fmt.Fprintf(h, "%s %d %d\n", r.rel, r.size, r.mod)
	}
	if truncated {
		fmt.Fprintf(h, "truncated %d\n", fingerprintCap)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
