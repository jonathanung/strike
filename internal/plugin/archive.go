package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// maxArchiveEntries bounds files extracted from one plugin artifact.
	maxArchiveEntries = 10_000
	// maxUncompressedBytes bounds total extracted payload size (128 MiB).
	maxUncompressedBytes = 128 << 20
	// maxSingleFileBytes bounds one extracted file (64 MiB).
	maxSingleFileBytes = 64 << 20
)

// extractArchive unpacks a .tar.gz or .zip plugin artifact into destDir.
// Guards against zip-slip / tar traversal, absolute paths, and oversized payloads.
// The archive root may contain a single top-level directory wrapping the plugin;
// if destDir would lack plugin.json after extract, a sole top-level dir is flattened.
func extractArchive(data []byte, destDir string) error {
	if len(data) == 0 {
		return fmt.Errorf("empty archive")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	var err error
	switch {
	case isZip(data):
		err = extractZip(data, destDir)
	case isGzip(data):
		err = extractTarGz(data, destDir)
	default:
		// Try tar.gz anyway (some servers omit magic); then zip.
		if err = extractTarGz(data, destDir); err != nil {
			if err2 := extractZip(data, destDir); err2 == nil {
				err = nil
			} else {
				err = fmt.Errorf("unrecognized archive format (want .tar.gz or .zip)")
			}
		}
	}
	if err != nil {
		return err
	}
	return maybeFlattenPluginRoot(destDir)
}

func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K'
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func extractTarGz(data []byte, destDir string) error {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var total int64
	var entries int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive exceeds entry limit (%d)", maxArchiveEntries)
		}
		rel, err := sanitizeArchivePath(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target, err := confinedArchiveTarget(destDir, rel)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 || hdr.Size > maxSingleFileBytes {
				return fmt.Errorf("archive file %q size out of range", rel)
			}
			total += hdr.Size
			if total > maxUncompressedBytes {
				return fmt.Errorf("archive uncompressed size exceeds limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := hdr.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			// Never extract setuid bits.
			mode &^= 0o7000
			if err := writeExtractedFile(target, io.LimitReader(tr, hdr.Size+1), hdr.Size, mode); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive rejects hard/symbolic links (%s)", rel)
		default:
			// Skip other types (char devices, etc.).
			continue
		}
	}
	return nil
}

func extractZip(data []byte, destDir string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	if len(r.File) > maxArchiveEntries {
		return fmt.Errorf("archive exceeds entry limit (%d)", maxArchiveEntries)
	}
	var total int64
	for _, f := range r.File {
		rel, err := sanitizeArchivePath(f.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target, err := confinedArchiveTarget(destDir, rel)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.UncompressedSize64 > maxSingleFileBytes {
			return fmt.Errorf("archive file %q size out of range", rel)
		}
		total += int64(f.UncompressedSize64)
		if total > maxUncompressedBytes {
			return fmt.Errorf("archive uncompressed size exceeds limit")
		}
		// Reject symlink entries (zip may encode them as regular files with mode).
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive rejects symbolic links (%s)", rel)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		mode &^= 0o7000
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			_ = rc.Close()
			return err
		}
		werr := writeExtractedFile(target, io.LimitReader(rc, int64(f.UncompressedSize64)+1), int64(f.UncompressedSize64), mode)
		_ = rc.Close()
		if werr != nil {
			return fmt.Errorf("%s: %w", rel, werr)
		}
	}
	return nil
}

// sanitizeArchivePath cleans an archive entry name into a relative slash path
// under the extract root, or returns an error on traversal/absolute paths.
func sanitizeArchivePath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return "", nil
	}
	// Zip/tar on Windows may use backslashes.
	name = strings.ReplaceAll(name, `\`, "/")
	// Drop drive letters and leading slashes.
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("archive path %q is absolute", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", fmt.Errorf("archive path %q is absolute", name)
	}
	// Reject empty segments and .. after clean.
	parts := strings.Split(name, "/")
	var clean []string
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", fmt.Errorf("archive path %q contains '..'", name)
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", nil
	}
	return strings.Join(clean, "/"), nil
}

func confinedArchiveTarget(destDir, relSlash string) (string, error) {
	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(destAbs, filepath.FromSlash(relSlash))
	clean := filepath.Clean(target)
	if !isUnder(destAbs, clean) && clean != destAbs {
		return "", fmt.Errorf("archive path %q escapes extract root", relSlash)
	}
	return clean, nil
}

func writeExtractedFile(target string, r io.Reader, expectSize int64, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(target)
		return err
	}
	if expectSize >= 0 && n > expectSize {
		_ = os.Remove(target)
		return fmt.Errorf("file larger than declared size")
	}
	if n > maxSingleFileBytes {
		_ = os.Remove(target)
		return fmt.Errorf("file exceeds size limit")
	}
	return nil
}

// maybeFlattenPluginRoot if dest has no plugin.json but exactly one top-level
// directory that does, move that directory's contents up.
func maybeFlattenPluginRoot(destDir string) error {
	if _, _, err := ReadManifest(destDir); err == nil {
		return nil
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return err
	}
	var dirs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		} else {
			// Loose files without manifest — leave as-is for validation to fail.
			return nil
		}
	}
	if len(dirs) != 1 {
		return nil
	}
	inner := filepath.Join(destDir, dirs[0])
	if _, _, err := ReadManifest(inner); err != nil {
		return nil
	}
	// Move children of inner into destDir, then remove inner.
	children, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	for _, c := range children {
		src := filepath.Join(inner, c.Name())
		dst := filepath.Join(destDir, c.Name())
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return os.Remove(inner)
}
