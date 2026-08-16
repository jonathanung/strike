package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ComputeDigest returns the canonical content digest (sha256:<hex>) for a
// plugin root per docs/plugins.md §5.2.
func ComputeDigest(root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}

	var files []string
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			base := d.Name()
			if base == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if shouldSkipDigestFile(d.Name(), relSlash) {
			return nil
		}
		// Symlinks: only include if referent stays inside root.
		if d.Type()&fs.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("symlink %s: %w", relSlash, err)
			}
			if !isUnder(rootAbs, resolved) {
				return fmt.Errorf("symlink %s escapes plugin root", relSlash)
			}
			// Digest the referent bytes under the symlink's relative path name.
		}
		files = append(files, relSlash)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)

	h := sha256.New()
	for _, rel := range files {
		path := filepath.Join(rootAbs, filepath.FromSlash(rel))
		// Follow symlink to referent for content (already confined).
		fi, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		size := fi.Size()
		fmt.Fprintf(h, "%d:%s\n", size, rel)
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return "", err
		}
		_ = f.Close()
		_, _ = h.Write([]byte{0})
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return "sha256:" + sum, nil
}

func shouldSkipDigestFile(base, relSlash string) bool {
	if base == ".DS_Store" || strings.HasSuffix(base, ".swp") {
		return true
	}
	if strings.HasPrefix(relSlash, ".git/") {
		return true
	}
	return false
}
