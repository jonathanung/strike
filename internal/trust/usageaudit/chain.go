package usageaudit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/pkg/redact"
)

// SchemaVersion is the on-disk hash-chain line schema.
const SchemaVersion = "1"

// Actions other streams (#1266 approvals, #1279 terminal) may append.
const (
	ActionApproval   = "approval"
	ActionPermission = "permission"
	ActionShell      = "shell"
	ActionTerminal   = "terminal"
	ActionConfig     = "config"
	ActionToken      = "token"
)

// GenesisPrev is the prev hash of the first entry.
const GenesisPrev = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry is one stored audit line. Summary and Detail are already redacted.
type Entry struct {
	SchemaVersion string          `json:"schemaVersion"`
	Seq           int64           `json:"seq"`
	Time          string          `json:"time"`
	Action        string          `json:"action"`
	SessionID     string          `json:"sessionId,omitempty"`
	Summary       string          `json:"summary"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	Prev          string          `json:"prev"`
	Hash          string          `json:"hash"`
}

// Input is an append request. Strings are redacted before they are stored.
type Input struct {
	Action    string
	SessionID string
	Summary   string
	Detail    any
	// Time overrides the clock. Empty uses Log's clock.
	Time string
}

type canon struct {
	SchemaVersion string          `json:"schemaVersion"`
	Seq           int64           `json:"seq"`
	Time          string          `json:"time"`
	Action        string          `json:"action"`
	SessionID     string          `json:"sessionId,omitempty"`
	Summary       string          `json:"summary"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	Prev          string          `json:"prev"`
}

// VerifyError is a broken chain. Seq is 1-based; 0 means the file or tip.
type VerifyError struct {
	Seq    int64
	Reason string
}

func (e *VerifyError) Error() string {
	if e == nil {
		return "usage audit: verify failed"
	}
	if e.Seq > 0 {
		return fmt.Sprintf("usage audit: seq %d: %s", e.Seq, e.Reason)
	}
	return "usage audit: " + e.Reason
}

// Log is an append-only hash-chained audit file plus the daily budget.
type Log struct {
	dir  string
	path string
	now  func() time.Time

	mu     sync.Mutex
	closed bool
}

var (
	openMu   sync.Mutex
	openLogs = map[string]*Log{}
)

// DefaultDir is ~/.strike/usage-audit.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "usage-audit")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "usage-audit")
}

// Open opens (or creates) the chain in dir. Repeated opens of the same
// directory in this process return the same Log.
func Open(dir string) (*Log, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("usage audit: directory required")
	}
	dir = filepath.Clean(dir)
	openMu.Lock()
	defer openMu.Unlock()
	if existing, ok := openLogs[dir]; ok && !existing.closed {
		return existing, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "chain.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	l := &Log{
		dir:  dir,
		path: path,
		now:  time.Now,
	}
	openLogs[dir] = l
	return l, nil
}

// SetClock overrides the timestamp source. Tests use it.
func (l *Log) SetClock(now func() time.Time) {
	if l == nil || now == nil {
		return
	}
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Dir returns the log directory.
func (l *Log) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// Close unregisters the log. The file stays on disk.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	openMu.Lock()
	if cur, ok := openLogs[l.dir]; ok && cur == l {
		delete(openLogs, l.dir)
	}
	openMu.Unlock()
	return nil
}

// Append redacts input, links it to the previous hash, and fsyncs the line
// and the tip file.
func (l *Log) Append(in Input) (Entry, error) {
	if l == nil {
		return Entry{}, errors.New("usage audit: nil log")
	}
	action := strings.TrimSpace(in.Action)
	if !knownAction(action) {
		return Entry{}, fmt.Errorf("usage audit: unknown action %q", in.Action)
	}
	summary := truncate(redact.String(strings.TrimSpace(in.Summary)), 500)
	if summary == "" {
		return Entry{}, errors.New("usage audit: summary required")
	}
	detail, err := redactDetail(in.Detail)
	if err != nil {
		return Entry{}, err
	}
	when := strings.TrimSpace(in.Time)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Entry{}, errors.New("usage audit: log closed")
	}
	if when == "" {
		when = l.now().UTC().Format(time.RFC3339Nano)
	}
	f, err := os.OpenFile(l.path, os.O_RDWR, 0o600)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	if err := lockFile(f, true); err != nil {
		return Entry{}, err
	}
	defer unlockFile(f)

	prevSeq, prevHash, err := lastLocked(f)
	if err != nil {
		return Entry{}, err
	}
	if prevHash == "" {
		prevHash = GenesisPrev
	}
	entry := Entry{
		SchemaVersion: SchemaVersion,
		Seq:           prevSeq + 1,
		Time:          when,
		Action:        action,
		SessionID:     strings.TrimSpace(in.SessionID),
		Summary:       summary,
		Detail:        detail,
		Prev:          prevHash,
	}
	sum, err := hashEntry(entry)
	if err != nil {
		return Entry{}, err
	}
	entry.Hash = sum
	line, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, err
	}
	line = append(line, '\n')
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return Entry{}, err
	}
	if _, err := f.Write(line); err != nil {
		return Entry{}, err
	}
	if err := f.Sync(); err != nil {
		return Entry{}, err
	}
	if err := writeTip(l.dir, entry.Seq, entry.Hash); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Entries reads the chain. It does not verify.
func (l *Log) Entries() ([]Entry, error) {
	if l == nil {
		return nil, errors.New("usage audit: nil log")
	}
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	if err := lockFile(f, false); err != nil {
		return nil, err
	}
	defer unlockFile(f)
	return readAll(f)
}

// Verify checks hash links, sequence, and that tip.json matches the last
// line. Editing a line without recomputing the rest, or deleting a tail
// line while leaving the tip, fails.
func (l *Log) Verify() error {
	if l == nil {
		return errors.New("usage audit: nil log")
	}
	f, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := lockFile(f, false); err != nil {
		return err
	}
	defer unlockFile(f)
	entries, err := readAll(f)
	if err != nil {
		return err
	}
	prev := GenesisPrev
	for i, e := range entries {
		wantSeq := int64(i + 1)
		if e.Seq != wantSeq {
			return &VerifyError{Seq: e.Seq, Reason: fmt.Sprintf("seq gap, want %d", wantSeq)}
		}
		if e.Prev != prev {
			return &VerifyError{Seq: e.Seq, Reason: "prev hash does not match the previous entry"}
		}
		sum, err := hashEntry(e)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, e.Hash) {
			return &VerifyError{Seq: e.Seq, Reason: "hash does not match contents"}
		}
		prev = e.Hash
	}
	tipSeq, tipHash, err := readTip(l.dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if tipHash != "" {
			return &VerifyError{Reason: "tip present but chain is empty"}
		}
		return nil
	}
	last := entries[len(entries)-1]
	if tipHash == "" {
		return &VerifyError{Seq: last.Seq, Reason: "tip file missing"}
	}
	if tipSeq != last.Seq || !strings.EqualFold(tipHash, last.Hash) {
		return &VerifyError{Seq: last.Seq, Reason: "tip does not match the last entry"}
	}
	return nil
}

// Export is the downloadable bundle. Verified is false when the chain is
// damaged; entries are still included so the damage can be inspected.
type Export struct {
	Redacted bool    `json:"redacted"`
	Verified bool    `json:"verified"`
	Note     string  `json:"note"`
	Error    string  `json:"error,omitempty"`
	Entries  []Entry `json:"entries"`
}

// Export reads the chain and reports whether Verify passed.
func (l *Log) Export() (Export, error) {
	out := Export{
		Redacted: true,
		Note:     "Hash-chained audit of approvals, permission changes, shell, terminal, config edits, and token rotation. Payloads are redacted. This is not a session transcript.",
	}
	if l == nil {
		return out, errors.New("usage audit: nil log")
	}
	entries, err := l.Entries()
	if err != nil {
		return out, err
	}
	out.Entries = entries
	if err := l.Verify(); err != nil {
		out.Verified = false
		out.Error = err.Error()
		return out, nil
	}
	out.Verified = true
	return out, nil
}

type tipFile struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

func writeTip(dir string, seq int64, hash string) error {
	body, err := json.Marshal(tipFile{Seq: seq, Hash: hash})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := filepath.Join(dir, "tip.json.tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "tip.json"))
}

func readTip(dir string) (int64, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "tip.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, "", nil
		}
		return 0, "", err
	}
	var tip tipFile
	if err := json.Unmarshal(data, &tip); err != nil {
		return 0, "", fmt.Errorf("usage audit: tip: %w", err)
	}
	return tip.Seq, strings.TrimSpace(tip.Hash), nil
}

func lastLocked(f *os.File) (int64, string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	entries, err := readAll(f)
	if err != nil {
		return 0, "", err
	}
	if len(entries) == 0 {
		return 0, "", nil
	}
	last := entries[len(entries)-1]
	return last.Seq, last.Hash, nil
}

func readAll(f *os.File) ([]Entry, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var out []Entry
	lineNo := 0
	for sc.Scan() {
		lineNo++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return nil, &VerifyError{Seq: int64(lineNo), Reason: "corrupt line: " + err.Error()}
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func hashEntry(e Entry) (string, error) {
	body, err := json.Marshal(canon{
		SchemaVersion: e.SchemaVersion,
		Seq:           e.Seq,
		Time:          e.Time,
		Action:        e.Action,
		SessionID:     e.SessionID,
		Summary:       e.Summary,
		Detail:        e.Detail,
		Prev:          e.Prev,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func knownAction(action string) bool {
	switch action {
	case ActionApproval, ActionPermission, ActionShell, ActionTerminal, ActionConfig, ActionToken:
		return true
	default:
		return false
	}
}

func redactDetail(detail any) (json.RawMessage, error) {
	if detail == nil {
		return nil, nil
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > 8192 {
		return nil, errors.New("usage audit: detail exceeds 8KB")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	redacted := redactValue(v)
	out, err := json.Marshal(redacted)
	if err != nil {
		return nil, err
	}
	if len(out) > 8192 {
		return nil, errors.New("usage audit: detail exceeds 8KB")
	}
	return out, nil
}

func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return truncate(redact.String(t), 500)
	case map[string]any:
		for k, val := range t {
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	default:
		return v
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// Budget is the operator-set daily spend ceiling in USD.
// Zero means no ceiling. The number is never inferred from usage.
type Budget struct {
	DailyUSD float64 `json:"dailyUsd"`
}

// DailyBudget reads the ceiling. set is false when no ceiling is stored.
func (l *Log) DailyBudget() (usd float64, set bool, err error) {
	if l == nil {
		return 0, false, errors.New("usage audit: nil log")
	}
	data, err := os.ReadFile(filepath.Join(l.dir, "budget.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	var b Budget
	if err := json.Unmarshal(data, &b); err != nil {
		return 0, false, err
	}
	if b.DailyUSD <= 0 {
		return 0, false, nil
	}
	return b.DailyUSD, true, nil
}

// SetDailyBudget stores the ceiling. Zero clears it.
func (l *Log) SetDailyBudget(usd float64) error {
	if l == nil {
		return errors.New("usage audit: nil log")
	}
	if usd < 0 {
		return errors.New("usage audit: daily budget must be zero or positive")
	}
	body, err := json.Marshal(Budget{DailyUSD: usd})
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := filepath.Join(l.dir, "budget.json.tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(l.dir, "budget.json"))
}
