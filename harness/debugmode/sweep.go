package debugmode

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
)

// maxSweepFileSize skips large files (generated bundles, binaries).
const maxSweepFileSize = 4 << 20

// Hit is one tagged line found on disk.
type Hit struct {
	Path string // slash-separated, relative to root
	Line int    // 1-based
	Text string
}

// SweepReport lists removed lines and any that remained after the sweep.
type SweepReport struct {
	Removed   []Hit
	Remaining []Hit
}

// Scan reports every tagged line under root without modifying files.
// Skips .git, symlinks, files over 4 MiB and binary files.
func Scan(root string) ([]Hit, error) {
	var hits []Hit
	err := walkText(root, func(rel string, _ string, data []byte, _ fs.FileMode) error {
		hits = append(hits, findTagged(rel, data)...)
		return nil
	})
	return hits, err
}

// Sweep removes every whole line containing Marker under root, preserving
// file modes, then rescans so callers can verify zero lines remain.
func Sweep(root string) (SweepReport, error) {
	var rep SweepReport
	err := walkText(root, func(rel, abs string, data []byte, mode fs.FileMode) error {
		hits := findTagged(rel, data)
		if len(hits) == 0 {
			return nil
		}
		if err := os.WriteFile(abs, stripTagged(data), mode.Perm()); err != nil {
			return err
		}
		rep.Removed = append(rep.Removed, hits...)
		return nil
	})
	if err != nil {
		return rep, err
	}
	rep.Remaining, err = Scan(root)
	return rep, err
}

func walkText(root string, fn func(rel, abs string, data []byte, mode fs.FileMode) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, devices, sockets
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxSweepFileSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(data) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), path, data, info.Mode())
	})
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

func findTagged(rel string, data []byte) []Hit {
	marker := []byte(Marker)
	if !bytes.Contains(data, marker) {
		return nil
	}
	var hits []Hit
	for i, line := range bytes.SplitAfter(data, []byte("\n")) {
		if bytes.Contains(line, marker) {
			hits = append(hits, Hit{Path: rel, Line: i + 1, Text: string(bytes.TrimRight(line, "\r\n"))})
		}
	}
	return hits
}

// stripTagged drops whole lines containing Marker, keeping every other
// byte (including line endings) unchanged.
func stripTagged(data []byte) []byte {
	marker := []byte(Marker)
	var out bytes.Buffer
	out.Grow(len(data))
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if !bytes.Contains(line, marker) {
			out.Write(line)
		}
	}
	return out.Bytes()
}
