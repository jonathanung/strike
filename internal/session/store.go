// Package session persists sessions as JSONL logs of protocol events and
// coordinates concurrent open logs via Manager. The event stream is the
// transcript, so resume/replay is re-reading the log. cmd/strike is the only
// importer: it tees engine events through a store (or Manager) on their way to
// the frontend. internal/tui never imports this package directly.
//
// Durability (#803): each Append writes a complete JSON line then fsyncs so a
// crash cannot leave a half-record that poisons the log. Replay skips a
// trailing incomplete line (crash residue) and fails closed on interior
// corruption or an unsupported newer log schema version. See also export/
// import packages, Fork lineage, and retention hooks in this package.
// Trace/run sidecar retention (#810) coordinates with session.retention* via
// ApplyTraceRetention / ApplyRetentionWithSidecars. Durable checkpoint stacks
// live under ~/.strike/checkpoints/<session-id>/ (#573) and are removed with
// Destroy/retention. Human-readable markdown transcript export is #221
// (/export) — complementary, not replacements.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/harness/fault"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/secret"
)

// LogSchemaVersion is the on-disk session JSONL format version written in the
// optional first-line session.header. Bump on breaking log layout changes.
// Unknown newer versions fail open/replay with an upgrade message.
const LogSchemaVersion = 1

// headerType is the JSON "type" of the optional first-line schema header.
const headerType = "session.header"

// Store is an open append-only session JSONL writer.
type Store struct {
	mu sync.Mutex
	f  *os.File
	// appendDisabled is set after a short/partial write or fsync failure so
	// callers cannot compound corruption; Recover (or reopen) is required.
	appendDisabled bool
	// recoverTo is the known-good file size before a failed write/fsync.
	// Recover truncates back to this boundary before reopening. -1 means
	// "compute durable prefix via Replay" (e.g. after external damage).
	recoverTo int64
}

// PersistenceError is returned when session durability fails and the runtime
// must not continue side effects as if the log were healthy.
type PersistenceError struct {
	Op      string // append | recover | sync
	Path    string
	Message string
	Err     error
	// Fatal is true when recovery failed or the store is latched closed;
	// active turns must cancel and must not treat the transcript as complete.
	Fatal bool
}

func (e *PersistenceError) Error() string {
	if e == nil {
		return "session: persistence error"
	}
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg == "" {
		msg = "persistence failure"
	}
	op := e.Op
	if op == "" {
		op = "append"
	}
	if e.Path != "" {
		return fmt.Sprintf("session %s %q: %s", op, e.Path, msg)
	}
	return fmt.Sprintf("session %s: %s", op, msg)
}

func (e *PersistenceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsFatalPersistence reports whether err requires stopping runtime side effects.
func IsFatalPersistence(err error) bool {
	var pe *PersistenceError
	if errors.As(err, &pe) {
		return pe != nil && pe.Fatal
	}
	return false
}

// logHeader is the optional first line of a session JSONL log.
type logHeader struct {
	Type          string    `json:"type"`
	SchemaVersion int       `json:"schemaVersion"`
	Time          time.Time `json:"time"`
}

// DefaultDir is ~/.strike/sessions — ~/.strike is strike's home for all
// user-level state. Existing ~/.strike directory symlinks are resolved.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "sessions")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "sessions")
}

// newIDLast is advanced under newIDMu so rapid NewID calls stay strictly
// increasing and therefore lexically sortable despite random suffixes.
var (
	newIDMu   sync.Mutex
	newIDLast time.Time
)

// NewID returns a UTC timestamp-first, filename-safe, collision-resistant
// session identifier. Lexical order matches creation order.
func NewID() string {
	newIDMu.Lock()
	defer newIDMu.Unlock()
	now := time.Now().UTC()
	if !now.After(newIDLast) {
		now = newIDLast.Add(time.Nanosecond)
	}
	newIDLast = now
	// Fixed-width fractional seconds so equal-length prefixes sort by time.
	return now.Format("20060102T150405.000000000Z") + "-" + rand.Text()
}

// LogPath is the JSONL event-log path for a session id under dir.
func LogPath(dir, id string) string {
	return filepath.Join(dir, id+".jsonl")
}

// Open creates or opens a session log for append. New empty files receive a
// schema header line before any events.
func Open(dir, id string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := LogPath(dir, id)
	st, statErr := os.Stat(path)
	needHeader := false
	switch {
	case statErr == nil && st.Size() == 0:
		needHeader = true
	case statErr != nil && errors.Is(statErr, os.ErrNotExist):
		needHeader = true
	case statErr != nil:
		return nil, statErr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Store{f: f, recoverTo: -1}
	if needHeader {
		if err := s.writeHeaderLocked(); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) writeHeaderLocked() error {
	h := logHeader{
		Type:          headerType,
		SchemaVersion: LogSchemaVersion,
		Time:          time.Now().UTC(),
	}
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.f.Write(line); err != nil {
		return fmt.Errorf("write session header: %w", err)
	}
	if err := s.fsyncLocked(); err != nil {
		return fmt.Errorf("sync session header: %w", err)
	}
	return nil
}

// fsyncLocked durability-flushes the open log. fault.SessionSync may inject
// failures for chaos tests (see docs/chaos.md).
func (s *Store) fsyncLocked() error {
	if err := fault.Check(fault.SessionSync); err != nil {
		return err
	}
	return s.f.Sync()
}

func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pathLocked()
}

func (s *Store) Append(ev protocol.Event) error {
	// Scrub credentials before JSONL persist so session logs, timeline export
	// consumers, and diagnostic bundles never retain raw secrets.
	env, err := protocol.Wrap(secret.RedactEvent(ev))
	if err != nil {
		return err
	}
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendDisabled {
		return &PersistenceError{
			Op:      "append",
			Path:    s.pathLocked(),
			Message: "cannot append after a partial write or fsync failure; recover the log",
			Fatal:   true,
		}
	}
	if s.f == nil {
		return &PersistenceError{
			Op:      "append",
			Message: "store is closed",
			Fatal:   true,
		}
	}
	startSize, err := s.sizeLocked()
	if err != nil {
		return &PersistenceError{Op: "append", Path: s.pathLocked(), Err: err, Fatal: true}
	}
	if err := fault.Check(fault.SessionWrite); err != nil {
		// Injected write failure before any bytes — retryable without latch.
		return &PersistenceError{Op: "append", Path: s.pathLocked(), Err: err, Fatal: false}
	}
	n, err := s.f.Write(line)
	if err != nil {
		if n != 0 {
			s.latchLocked(startSize)
		}
		return &PersistenceError{
			Op:      "append",
			Path:    s.pathLocked(),
			Message: "write failed",
			Err:     err,
			Fatal:   n != 0,
		}
	}
	if n != len(line) {
		s.latchLocked(startSize)
		return &PersistenceError{
			Op:      "append",
			Path:    s.pathLocked(),
			Message: "short write",
			Err:     io.ErrShortWrite,
			Fatal:   true,
		}
	}
	// fsync so a crash cannot leave a torn last record on stable storage.
	// On failure, roll back to startSize — the line is not confirmed durable.
	if err := s.fsyncLocked(); err != nil {
		s.latchLocked(startSize)
		return &PersistenceError{
			Op:      "sync",
			Path:    s.pathLocked(),
			Message: "fsync failed",
			Err:     err,
			Fatal:   true,
		}
	}
	return nil
}

func (s *Store) pathLocked() string {
	if s.f == nil {
		return ""
	}
	return s.f.Name()
}

func (s *Store) sizeLocked() (int64, error) {
	st, err := s.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func (s *Store) latchLocked(recoverTo int64) {
	s.appendDisabled = true
	s.recoverTo = recoverTo
}

// NeedsRecover reports whether Append is latched after a durability failure.
func (s *Store) NeedsRecover() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendDisabled
}

// Recover reopens the log after a partial write or fsync failure.
// It truncates back to the known-good size (or the validated durable prefix),
// verifies the prefix via Replay, and clears the append latch. It never
// appends new records. Returns a fatal PersistenceError when recovery is
// impossible (interior corruption, truncate/open failure).
func (s *Store) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoverLocked()
}

func (s *Store) recoverLocked() error {
	path := s.pathLocked()
	if path == "" && s.f == nil {
		return &PersistenceError{Op: "recover", Message: "store is closed", Fatal: true}
	}
	if s.f != nil {
		path = s.f.Name()
		_ = s.f.Close()
		s.f = nil
	}
	target := s.recoverTo
	if target < 0 {
		end, err := durablePrefixEnd(path)
		if err != nil {
			s.appendDisabled = true
			return &PersistenceError{Op: "recover", Path: path, Message: "unrecoverable log", Err: err, Fatal: true}
		}
		target = end
	}
	if err := os.Truncate(path, target); err != nil {
		s.appendDisabled = true
		return &PersistenceError{Op: "recover", Path: path, Message: "truncate failed", Err: err, Fatal: true}
	}
	// Validate durable prefix before accepting new appends.
	if _, err := Replay(path); err != nil {
		s.appendDisabled = true
		return &PersistenceError{Op: "recover", Path: path, Message: "prefix validation failed", Err: err, Fatal: true}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		s.appendDisabled = true
		return &PersistenceError{Op: "recover", Path: path, Message: "reopen failed", Err: err, Fatal: true}
	}
	s.f = f
	s.appendDisabled = false
	s.recoverTo = -1
	return nil
}

// durablePrefixEnd returns the byte size of the longest valid complete-line
// prefix of path. Trailing partial lines are excluded. Interior corruption
// returns an error (unrecoverable without external repair).
func durablePrefixEnd(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 {
		return 0, nil
	}
	// Walk complete newline-terminated lines; stop before a trailing partial.
	var end int
	for end < len(raw) {
		nl := indexByte(raw[end:], '\n')
		if nl < 0 {
			// Trailing partial — durable prefix ends at end.
			break
		}
		lineEnd := end + nl + 1
		chunk := raw[:lineEnd]
		// Validate by decoding what we have so far (endsWithNL=true).
		if _, err := decodeLogLines(path, splitCompleteLines(chunk), true); err != nil {
			return 0, err
		}
		end = lineEnd
	}
	return int64(end), nil
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func splitCompleteLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		line := bytesTrimSpace(raw[start:i])
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
		start = i + 1
	}
	return lines
}

// Sync flushes the underlying JSONL file so concurrent readers see all
// appended events (e.g. Fork copying an open session).
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	if err := s.fsyncLocked(); err != nil {
		// Do not latch on Sync-only failure of already-appended durable lines;
		// Append already fsynced each record. Surface the error to the caller.
		return &PersistenceError{Op: "sync", Path: s.pathLocked(), Err: err, Fatal: false}
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// idCreatedAt parses the UTC timestamp prefix of a NewID value when present.
func idCreatedAt(id string) (time.Time, bool) {
	// NewID: 20060102T150405.000000000Z-<suffix>
	i := strings.Index(id, "Z-")
	if i <= 0 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405.999999999Z", id[:i+1])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// TimedEvent pairs a decoded protocol event with its JSONL envelope timestamp.
// Used by timeline export so durations are derived from durable log times.
type TimedEvent struct {
	Time  time.Time
	Event protocol.Event
}

// CorruptError describes an unreadable interior session log record.
type CorruptError struct {
	Path    string
	Line    int
	Message string
	Err     error
}

func (e *CorruptError) Error() string {
	if e == nil {
		return "session: corrupt log"
	}
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if e.Path == "" {
		return fmt.Sprintf("session log line %d: %s (repair: restore from a session package export, or fork a known-good prefix)", e.Line, msg)
	}
	return fmt.Sprintf("session log %q line %d: %s (repair: restore from a session package export, or fork a known-good prefix)", e.Path, e.Line, msg)
}

func (e *CorruptError) Unwrap() error { return e.Err }

// SchemaVersionError is returned when a log header declares a newer schema
// than this binary supports.
type SchemaVersionError struct {
	Path    string
	Found   int
	Support int
}

func (e *SchemaVersionError) Error() string {
	if e == nil {
		return "session: unsupported schema version"
	}
	p := e.Path
	if p == "" {
		p = "session log"
	}
	return fmt.Sprintf("%s: schema version %d is newer than supported %d; upgrade strike to read this session", p, e.Found, e.Support)
}

// Replay reads all events back from a session log.
func Replay(path string) ([]protocol.Event, error) {
	timed, err := ReplayTimed(path)
	if err != nil {
		return nil, err
	}
	events := make([]protocol.Event, len(timed))
	for i, te := range timed {
		events[i] = te.Event
	}
	return events, nil
}

// ReplayTimed reads all events with envelope timestamps from a session log.
// A trailing incomplete line without a terminating newline (crash mid-append)
// is skipped. Interior corrupt lines, complete-but-invalid final lines, and
// unsupported newer schema versions return actionable errors.
func ReplayTimed(path string) ([]TimedEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var endsWithNL bool
	if st.Size() > 0 {
		if _, err := f.Seek(-1, io.SeekEnd); err == nil {
			var b [1]byte
			if _, err := f.Read(b[:]); err == nil {
				endsWithNL = b[0] == '\n'
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	} else {
		endsWithNL = true
	}

	lines, err := readLogLines(f)
	if err != nil {
		return nil, fmt.Errorf("session log %q: %w", path, err)
	}
	return decodeLogLines(path, lines, endsWithNL)
}

// InspectSchemaVersion reads the log header (or infers legacy v1) without a
// full event decode. Returns LogSchemaVersion for empty/legacy logs.
func InspectSchemaVersion(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	lines, err := readLogLines(f)
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return LogSchemaVersion, nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(lines[0], &probe); err != nil {
		return 0, &CorruptError{Path: path, Line: 1, Message: "invalid JSON", Err: err}
	}
	typ, _ := jsonString(probe["type"])
	if typ != headerType {
		return LogSchemaVersion, nil // legacy log without header
	}
	ver, err := jsonInt(probe["schemaVersion"])
	if err != nil {
		return 0, &CorruptError{Path: path, Line: 1, Message: "session.header missing schemaVersion", Err: err}
	}
	if ver > LogSchemaVersion {
		return ver, &SchemaVersionError{Path: path, Found: ver, Support: LogSchemaVersion}
	}
	if ver < 1 {
		return 0, &CorruptError{Path: path, Line: 1, Message: fmt.Sprintf("invalid schemaVersion %d", ver)}
	}
	return ver, nil
}

func readLogLines(r io.Reader) ([][]byte, error) {
	scanner := bufio.NewScanner(r)
	// Multimodal user.message lines can carry multi-MiB base64 images.
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	var lines [][]byte
	for scanner.Scan() {
		raw := bytesTrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		// Scanner reuses its buffer; copy each line.
		lines = append(lines, append([]byte(nil), raw...))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func decodeLogLines(path string, lines [][]byte, endsWithNL bool) ([]TimedEvent, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	start := 0
	// Optional schema header on line 1.
	if ver, ok, err := parseHeaderLine(lines[0]); err != nil {
		// Torn first line (no trailing newline) → empty readable log.
		if len(lines) == 1 && !endsWithNL {
			return nil, nil
		}
		return nil, &CorruptError{Path: path, Line: 1, Message: "invalid JSON", Err: err}
	} else if ok {
		if ver > LogSchemaVersion {
			return nil, &SchemaVersionError{Path: path, Found: ver, Support: LogSchemaVersion}
		}
		if ver < 1 {
			return nil, &CorruptError{Path: path, Line: 1, Message: fmt.Sprintf("invalid schemaVersion %d", ver)}
		}
		start = 1
	}

	var events []TimedEvent
	for i := start; i < len(lines); i++ {
		lineNo := i + 1
		last := i == len(lines)-1
		// Crash mid-append leaves a partial final record without '\n'.
		softTail := last && !endsWithNL
		var env protocol.Envelope
		if err := json.Unmarshal(lines[i], &env); err != nil {
			if softTail {
				break
			}
			return nil, &CorruptError{Path: path, Line: lineNo, Message: "invalid JSON", Err: err}
		}
		if env.Type == headerType {
			return nil, &CorruptError{Path: path, Line: lineNo, Message: "unexpected session.header mid-log"}
		}
		if err := checkEnvelopeVersion(env); err != nil {
			if softTail {
				break
			}
			return nil, &CorruptError{Path: path, Line: lineNo, Message: err.Error(), Err: err}
		}
		ev, err := env.Decode()
		if err != nil {
			if softTail {
				break
			}
			return nil, &CorruptError{Path: path, Line: lineNo, Message: "decode event", Err: err}
		}
		events = append(events, TimedEvent{Time: env.Time.UTC(), Event: ev})
	}
	return events, nil
}

func parseHeaderLine(raw []byte) (version int, isHeader bool, err error) {
	var probe struct {
		Type          string `json:"type"`
		SchemaVersion int    `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, false, err
	}
	if probe.Type != headerType {
		return 0, false, nil
	}
	return probe.SchemaVersion, true, nil
}

func checkEnvelopeVersion(env protocol.Envelope) error {
	v := env.SchemaVersion()
	if v == "" {
		return nil
	}
	foundMajor, err := semverMajor(v)
	if err != nil {
		// Non-semver tags: ignore (legacy / test fixtures).
		return nil
	}
	supportMajor, err := semverMajor(protocol.Version)
	if err != nil {
		return nil
	}
	if foundMajor > supportMajor {
		return fmt.Errorf("envelope schema version %q is newer than supported %s; upgrade strike", v, protocol.Version)
	}
	return nil
}

func semverMajor(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty")
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		v = v[:i]
	}
	return strconv.Atoi(v)
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func jsonInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("missing")
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, err
	}
	return n, nil
}

func bytesTrimSpace(b []byte) []byte {
	// Avoid strings.TrimSpace alloc for hot path; match unicode.IsSpace lightly
	// for ASCII whitespace used in JSONL.
	start, end := 0, len(b)
	for start < end {
		c := b[start]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		start++
	}
	for end > start {
		c := b[end-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		end--
	}
	return b[start:end]
}

// ReplaySlice returns a bounded ordered slice of events from a session log.
// offset is 0-based; limit caps the number of events returned (must be > 0).
// total is the full event count. Never loads more than needed into the result
// slice, though the file is still scanned to compute total and reach offset.
func ReplaySlice(path string, offset, limit int) (events []protocol.Event, total int, err error) {
	if offset < 0 {
		return nil, 0, fmt.Errorf("offset must be >= 0")
	}
	if limit <= 0 {
		return nil, 0, fmt.Errorf("limit must be > 0")
	}
	all, err := Replay(path)
	if err != nil {
		return nil, 0, err
	}
	total = len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

// ReplayLast returns up to n trailing events from a session log (bounded).
// n must be > 0. Order is chronological (oldest of the tail first).
func ReplayLast(path string, n int) (events []protocol.Event, total int, err error) {
	if n <= 0 {
		return nil, 0, fmt.Errorf("n must be > 0")
	}
	all, err := Replay(path)
	if err != nil {
		return nil, 0, err
	}
	total = len(all)
	if total == 0 {
		return nil, 0, nil
	}
	if n >= total {
		return all, total, nil
	}
	return all[total-n:], total, nil
}
