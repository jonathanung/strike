package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// BlobRefPrefix is the scheme for content-addressed payload spill refs.
// Full form: blob:sha256:<hex>
const BlobRefPrefix = "blob:sha256:"

// DefaultMaxSpillBytes caps how much redacted payload is written to a blob
// (0 in Options means this default). Larger inputs are truncated before spill.
const DefaultMaxSpillBytes = 256 << 10 // 256 KiB

// DefaultMaxEntries is the in-memory entry cap when Options.MaxEntries is 0.
// Keeps live builders bounded under long sessions; set MaxEntries < 0 to disable.
const DefaultMaxEntries = 10_000

// Metrics reports Observe cost and storage counters for the builder.
// Observe is intentionally lock-scoped and does not fsync; blob spill uses
// plain WriteFile so the turn/UI loop is not blocked on durability flushes
// (session JSONL remains the durable transcript with its own fsync policy).
type Metrics struct {
	Observes      int64 `json:"observes"`
	ObserveNanos  int64 `json:"observeNanos"`  // cumulative wall time in Observe
	LastObserveNs int64 `json:"lastObserveNs"` // most recent Observe duration
	Entries       int   `json:"entries"`
	Spills        int64 `json:"spills"`
	Truncations   int64 `json:"truncations"`
	Pruned        int64 `json:"pruned"`
}

// AvgObserveNs returns mean Observe duration in nanoseconds (0 if none).
func (m Metrics) AvgObserveNs() int64 {
	if m.Observes == 0 {
		return 0
	}
	return m.ObserveNanos / m.Observes
}

// RetentionPolicy bounds on-disk trace/blob/recording trees by count, age,
// and/or total bytes. Zero fields are unlimited. Mirrors session retention
// axes (#803) for observability sidecars under ~/.strike/traces and runs.
type RetentionPolicy struct {
	// MaxFiles caps how many top-level entries (files or session dirs) to keep
	// (0 = unlimited). Oldest ModTime are removed first.
	MaxFiles int
	// MaxAge deletes entries whose ModTime is older than now-MaxAge (0 = off).
	MaxAge time.Duration
	// MaxBytes caps the recursive byte sum of retained entries (0 = off).
	MaxBytes int64
}

// RetentionResult summarizes ApplyRetention deletions.
type RetentionResult struct {
	Deleted []string
	Freed   int64
}

// Active reports whether any retention limit is set.
func (p RetentionPolicy) Active() bool {
	return p.MaxFiles > 0 || p.MaxAge > 0 || p.MaxBytes > 0
}

// RetentionFromConfig builds a policy from config-shaped integers.
// maxAgeDays converts to a 24h duration; non-positive values disable that axis.
func RetentionFromConfig(maxFiles, maxAgeDays int, maxBytes int64) RetentionPolicy {
	p := RetentionPolicy{MaxFiles: maxFiles, MaxBytes: maxBytes}
	if maxAgeDays > 0 {
		p.MaxAge = time.Duration(maxAgeDays) * 24 * time.Hour
	}
	if p.MaxFiles < 0 {
		p.MaxFiles = 0
	}
	if p.MaxBytes < 0 {
		p.MaxBytes = 0
	}
	return p
}

// DefaultTracesDir is ~/.strike/traces — session-scoped blob trees live under
// <tracesDir>/<sessionID>/blobs/<sha256>.
func DefaultTracesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "traces")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "traces")
}

// SessionBlobDir is the default blob directory for one session's spilled payloads.
func SessionBlobDir(tracesDir, sessionID string) string {
	if tracesDir == "" {
		tracesDir = DefaultTracesDir()
	}
	id := sanitizePathPart(sessionID)
	if id == "" {
		id = "_unknown"
	}
	return filepath.Join(tracesDir, id, "blobs")
}

// ApplyRetention deletes top-level children of dir that exceed the policy.
// Each child (file or directory) is treated as one retention unit; directory
// size is the recursive sum of file sizes. Missing dir is a no-op.
func ApplyRetention(dir string, p RetentionPolicy) (RetentionResult, error) {
	var out RetentionResult
	if !p.Active() {
		return out, nil
	}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return out, fmt.Errorf("timeline: retention dir is empty")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}

	type cand struct {
		name    string
		path    string
		modTime time.Time
		size    int64
	}
	var items []cand
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		if e.IsDir() {
			size = dirBytes(path)
		}
		items = append(items, cand{
			name:    e.Name(),
			path:    path,
			modTime: info.ModTime().UTC(),
			size:    size,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].modTime.Equal(items[j].modTime) {
			return items[i].modTime.Before(items[j].modTime)
		}
		return items[i].name < items[j].name
	})

	keep := make([]bool, len(items))
	for i := range keep {
		keep[i] = true
	}
	now := time.Now().UTC()

	if p.MaxAge > 0 {
		cutoff := now.Add(-p.MaxAge)
		for i, c := range items {
			if c.modTime.Before(cutoff) {
				keep[i] = false
			}
		}
	}

	if p.MaxFiles > 0 {
		n := 0
		for _, k := range keep {
			if k {
				n++
			}
		}
		for i := 0; i < len(items) && n > p.MaxFiles; i++ {
			if !keep[i] {
				continue
			}
			keep[i] = false
			n--
		}
	}

	if p.MaxBytes > 0 {
		var remain int64
		for i, c := range items {
			if keep[i] {
				remain += c.size
			}
		}
		for i := 0; i < len(items) && remain > p.MaxBytes; i++ {
			if !keep[i] {
				continue
			}
			keep[i] = false
			remain -= items[i].size
		}
	}

	for i, c := range items {
		if keep[i] {
			continue
		}
		if err := os.RemoveAll(c.path); err != nil {
			return out, fmt.Errorf("timeline: retention delete %q: %w", c.path, err)
		}
		out.Deleted = append(out.Deleted, c.name)
		out.Freed += c.size
	}
	return out, nil
}

// WriteBlob stores redacted payload bytes under blobDir and returns a blob ref.
// Does not fsync — observability spill must not block the turn loop; session
// JSONL remains the durability boundary.
func WriteBlob(blobDir string, payload string) (ref string, err error) {
	blobDir = filepath.Clean(strings.TrimSpace(blobDir))
	if blobDir == "" || blobDir == "." {
		return "", fmt.Errorf("timeline: blob dir is empty")
	}
	sum := sha256.Sum256([]byte(payload))
	hexSum := hex.EncodeToString(sum[:])
	ref = BlobRefPrefix + hexSum
	path := blobPath(blobDir, hexSum)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return ref, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("timeline: create blob dir: %w", err)
	}
	// Exclusive create when possible; fall back to truncate write.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return ref, nil
		}
		// Some FS may not support O_EXCL cleanly; plain write.
		if werr := os.WriteFile(path, []byte(payload), 0o644); werr != nil {
			return "", fmt.Errorf("timeline: write blob: %w", werr)
		}
		return ref, nil
	}
	_, werr := f.Write([]byte(payload))
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("timeline: write blob: %w", werr)
	}
	if cerr != nil {
		return "", fmt.Errorf("timeline: close blob: %w", cerr)
	}
	return ref, nil
}

// ReadBlob loads a previously spilled payload by ref from blobDir.
func ReadBlob(blobDir, ref string) (string, error) {
	hexSum, ok := parseBlobRef(ref)
	if !ok {
		return "", fmt.Errorf("timeline: invalid blob ref %q", ref)
	}
	path := blobPath(blobDir, hexSum)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func blobPath(blobDir, hexSum string) string {
	// Shard two hex chars to keep large blob trees manageable.
	if len(hexSum) >= 2 {
		return filepath.Join(blobDir, hexSum[:2], hexSum)
	}
	return filepath.Join(blobDir, hexSum)
}

func parseBlobRef(ref string) (hexSum string, ok bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, BlobRefPrefix) {
		return "", false
	}
	hexSum = strings.TrimPrefix(ref, BlobRefPrefix)
	if len(hexSum) != 64 {
		return "", false
	}
	for _, c := range hexSum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return hexSum, true
}

// boundPayload redacts s, clips to max runes for the inline preview, and
// optionally spills the full (size-capped) redacted body to BlobDir.
// Spill failures fall back to truncate-only so Observe never fails closed.
func boundPayload(s string, max int, blobDir string, maxSpill int) (preview, ref string, truncated bool, spilled bool) {
	if s == "" {
		return "", "", false, false
	}
	redacted := redact.String(s)
	if max <= 0 {
		return redacted, "", false, false
	}
	if utf8.RuneCountInString(redacted) <= max {
		return redacted, "", false, false
	}
	preview = clip(redacted, max)
	truncated = true

	if blobDir == "" {
		return preview, "", true, false
	}
	if maxSpill <= 0 {
		maxSpill = DefaultMaxSpillBytes
	}
	spillBody := redacted
	if len(spillBody) > maxSpill {
		// Byte cap for spill storage (not rune) — keep prefix + marker.
		spillBody = spillBody[:maxSpill] + "…[spill-capped]"
	}
	r, err := WriteBlob(blobDir, spillBody)
	if err != nil {
		return preview, "", true, false
	}
	return preview, r, true, true
}

func dirBytes(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		n += info.Size()
		return nil
	})
	return n
}

func sanitizePathPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return "_"
	}
	return out
}
