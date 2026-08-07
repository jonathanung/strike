package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
	"github.com/jonathanung/strike-cli/pkg/telemetry"
)

// Retention bounds durable audit storage. Zero fields are unlimited.
type Retention struct {
	MaxEvents int           // 0 = unlimited
	MaxAge    time.Duration // 0 = off
}

// Options configures a Sink.
type Options struct {
	// Dir is the audit root (default DefaultDir).
	Dir string
	// Retention is applied on Close and explicitly via Prune.
	Retention Retention
	// Clock overrides time.Now for tests.
	Clock func() time.Time
	// SegmentMaxBytes rotates the active JSONL segment (default 8 MiB).
	SegmentMaxBytes int64
}

// Sink is an append-only security audit log.
type Sink struct {
	mu      sync.Mutex
	dir     string
	ret     Retention
	clock   func() time.Time
	segMax  int64
	f       *os.File
	segPath string
	segSize int64
	closed  bool
}

// DefaultDir is ~/.strike/audit.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "audit")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "audit")
}

// Open creates or resumes a Sink under opts.Dir.
func Open(opts Options) (*Sink, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = DefaultDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	segMax := opts.SegmentMaxBytes
	if segMax <= 0 {
		segMax = 8 << 20
	}
	s := &Sink{dir: dir, ret: opts.Retention, clock: clock, segMax: segMax}
	if err := s.openSegment(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Sink) openSegment() error {
	name := s.clock().Format("20060102T150405.000000000Z") + ".jsonl"
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, _ := f.Stat()
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	if s.f != nil {
		_ = s.f.Close()
	}
	s.f = f
	s.segPath = path
	s.segSize = size
	return nil
}

// Record appends a redacted security event. payload should be a struct or map.
func (s *Sink) Record(family string, sessionID, turnID, toolCallID, chainID string, payload any) error {
	if s == nil {
		return nil
	}
	family = strings.TrimSpace(family)
	if family == "" {
		return fmt.Errorf("audit: family required")
	}
	raw, err := redactPayload(family, payload)
	if err != nil {
		return err
	}
	rec := Record{
		SchemaVersion: SchemaVersion,
		Family:        family,
		Time:          s.clock().UTC(),
		SessionID:     sessionID,
		TurnID:        turnID,
		ToolCallID:    toolCallID,
		ChainID:       chainID,
		Payload:       raw,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("audit: sink closed")
	}
	if s.f == nil {
		if err := s.openSegment(); err != nil {
			return err
		}
	}
	if s.segSize > 0 && s.segSize+int64(len(line)) > s.segMax {
		if err := s.openSegment(); err != nil {
			return err
		}
	}
	n, err := s.f.Write(line)
	if err != nil {
		return err
	}
	s.segSize += int64(n)
	return s.f.Sync()
}

// Observe maps selected protocol events into audit records (best-effort).
// Unknown / non-security events are ignored. Never stores full transcripts.
// Covers every documented family with a production path (#1032):
//
//	permission       ← PermissionDecided
//	toolchain_match  ← PermissionDecided with ChainRule
//	sandbox          ← ToolCallEnd sandbox_denied (+ degraded metadata)
//	egress           ← ToolCallEnd network_denied
//	content_guard    ← ToolCallEnd content_guard_denied
//	admission        ← Scheduler* + AdmissionDecided
//	hook             ← HookMatched (shell_* and declarative block)
//	secret_ref_use   ← direct Record (see RecordSecretRefUse)
func (s *Sink) Observe(ev protocol.Event) error {
	if s == nil || ev == nil {
		return nil
	}
	switch e := ev.(type) {
	case protocol.PermissionDecided:
		pats := make([]string, len(e.Patterns))
		copy(pats, e.Patterns)
		payload := telemetry.PermissionEvent{
			RequestID:      e.RequestID,
			Permission:     e.Permission,
			Patterns:       pats,
			Action:         e.Action,
			Decision:       string(e.Decision),
			Layer:          e.Layer,
			RulePermission: e.RulePermission,
			RulePattern:    e.RulePattern,
			RuleAction:     e.RuleAction,
			SessionID:      e.SessionID,
			TurnID:         e.TurnID,
		}
		if err := s.Record(FamilyPermission, e.SessionID, e.TurnID, "", e.ChainID, payload); err != nil {
			return err
		}
		// Tool-chain correlation match (#891) → dedicated family.
		if e.ChainRule != "" {
			tc := ToolchainMatchPayload{
				Tool:    e.Permission,
				Matched: redact.String(e.ChainSummary),
				Action:  e.Action,
				Source:  e.ChainRule,
			}
			if err := s.Record(FamilyToolchainMatch, e.SessionID, e.TurnID, "", e.ChainID, tc); err != nil {
				return err
			}
		}
		// content_guard permission asks/denies.
		if e.Permission == "content_guard" {
			cg := ContentGuardPayload{
				Action: e.Action,
				Reason: redact.String(e.RulePattern),
				Tool:   e.Permission,
				RuleID: e.RulePermission,
			}
			return s.Record(FamilyContentGuard, e.SessionID, e.TurnID, "", e.ChainID, cg)
		}
		return nil
	case protocol.ToolCallEnd:
		if !e.IsError {
			// Successful sandbox apply is not audited (noise); denials only.
			return nil
		}
		switch e.ErrorCode {
		case "sandbox_denied":
			payload := telemetry.SandboxEvent{
				Reason:     redact.String(e.Output),
				ErrorCode:  e.ErrorCode,
				SessionID:  e.SessionID,
				TurnID:     e.TurnID,
				ToolCallID: e.CallID,
			}
			return s.Record(FamilySandbox, e.SessionID, e.TurnID, e.CallID, "", payload)
		case "network_denied":
			payload := telemetry.EgressEvent{
				Destination:      "", // host may appear in redacted output only
				DestinationClass: "denied",
				Tool:             e.Title,
				Action:           "deny",
				Reason:           redact.String(e.Output),
				SessionID:        e.SessionID,
				TurnID:           e.TurnID,
				ToolCallID:       e.CallID,
			}
			return s.Record(FamilyEgress, e.SessionID, e.TurnID, e.CallID, "", payload)
		case "content_guard_denied":
			payload := ContentGuardPayload{
				Action: "deny",
				Reason: redact.String(e.Output),
				Tool:   e.Title,
			}
			return s.Record(FamilyContentGuard, e.SessionID, e.TurnID, e.CallID, "", payload)
		case "blocked":
			// Hook / phase policy block — also covered by HookMatched when present.
			payload := HookPayload{
				Event:    "tool_blocked",
				Action:   "block",
				Tool:     e.Title,
				Reason:   redact.String(e.Output),
				CallID:   e.CallID,
				Decision: "block",
			}
			return s.Record(FamilyHook, e.SessionID, e.TurnID, e.CallID, "", payload)
		default:
			return nil
		}
	case protocol.HookMatched:
		action := e.Action
		decision := "allow"
		if strings.Contains(action, "block") || strings.Contains(action, "fail_closed") {
			decision = "block"
		}
		payload := HookPayload{
			Event:    e.Event,
			Action:   action,
			Tool:     e.Tool,
			Reason:   redact.String(e.Message),
			CallID:   e.CallID,
			Decision: decision,
		}
		return s.Record(FamilyHook, e.SessionID, e.TurnID, e.CallID, "", payload)
	case protocol.AdmissionDecided:
		payload := telemetry.AdmissionEvent{
			Pool:      e.Surface + ":" + e.Target,
			State:     e.Action,
			Reason:    redact.String(e.Reason),
			SessionID: e.SessionID,
			TurnID:    e.TurnID,
		}
		return s.Record(FamilyAdmission, e.SessionID, e.TurnID, "", "", payload)
	case protocol.SchedulerQueued:
		payload := telemetry.AdmissionEvent{
			Pool:      firstPool(e.Pools),
			State:     "queued",
			SessionID: e.SessionID,
			TurnID:    e.TurnID,
		}
		return s.Record(FamilyAdmission, e.SessionID, e.TurnID, "", "", payload)
	case protocol.SchedulerAdmitted:
		var wait *int64
		if e.WaitMs > 0 {
			w := e.WaitMs
			wait = &w
		}
		payload := telemetry.AdmissionEvent{
			Pool:      firstPool(e.Pools),
			State:     "admitted",
			WaitMs:    wait,
			SessionID: e.SessionID,
			TurnID:    e.TurnID,
		}
		return s.Record(FamilyAdmission, e.SessionID, e.TurnID, "", "", payload)
	case protocol.SchedulerCanceled:
		payload := telemetry.AdmissionEvent{
			Pool:      firstPool(e.Pools),
			State:     "canceled",
			Reason:    redact.String(e.Reason),
			SessionID: e.SessionID,
			TurnID:    e.TurnID,
		}
		return s.Record(FamilyAdmission, e.SessionID, e.TurnID, "", "", payload)
	default:
		return nil
	}
}

// RecordSecretRefUse appends a secret_ref_use record (class/hash only — never
// the resolved value). Call at resolve/inject time from production paths.
func (s *Sink) RecordSecretRefUse(sessionID, turnID, toolCallID, refClass, refHash, action, toolName string) error {
	return s.Record(FamilySecretRefUse, sessionID, turnID, toolCallID, "", SecretRefUsePayload{
		RefClass: refClass,
		RefHash:  refHash,
		Action:   action,
		Tool:     toolName,
	})
}

func firstPool(pools []string) string {
	if len(pools) == 0 {
		return ""
	}
	return pools[0]
}

// Close flushes, applies retention, and releases the file handle.
func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.f != nil {
		err = s.f.Close()
		s.f = nil
	}
	if perr := s.pruneLocked(); perr != nil && err == nil {
		err = perr
	}
	return err
}

// Prune applies retention now.
func (s *Sink) Prune() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked()
}

func (s *Sink) pruneLocked() error {
	if s.ret.MaxEvents <= 0 && s.ret.MaxAge <= 0 {
		return nil
	}
	recs, paths, err := s.readAllLocked()
	if err != nil {
		return err
	}
	keep := make([]bool, len(recs))
	for i := range keep {
		keep[i] = true
	}
	now := s.clock().UTC()
	if s.ret.MaxAge > 0 {
		cutoff := now.Add(-s.ret.MaxAge)
		for i, r := range recs {
			if r.Time.Before(cutoff) {
				keep[i] = false
			}
		}
	}
	if s.ret.MaxEvents > 0 {
		// Drop oldest first until at most MaxEvents remain.
		n := 0
		for _, k := range keep {
			if k {
				n++
			}
		}
		for i := 0; i < len(recs) && n > s.ret.MaxEvents; i++ {
			if keep[i] {
				keep[i] = false
				n--
			}
		}
	}
	// Rewrite: group by original path order — simpler: write single compacted segment.
	var kept []Record
	for i, r := range recs {
		if keep[i] {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(recs) {
		return nil
	}
	// Close active file before rewrite.
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	// Remove old segments.
	for _, p := range unique(paths) {
		_ = os.Remove(p)
	}
	// Write compacted segment.
	if err := s.openSegment(); err != nil {
		return err
	}
	for _, r := range kept {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		n, err := s.f.Write(line)
		if err != nil {
			return err
		}
		s.segSize += int64(n)
	}
	return s.f.Sync()
}

func unique(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (s *Sink) readAllLocked() ([]Record, []string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(s.dir, e.Name()))
	}
	sort.Strings(files)
	var recs []Record
	var paths []string
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		sc := bufio.NewScanner(f)
		// 1 MiB lines max
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(bytesTrimSpace(line)) == 0 {
				continue
			}
			var r Record
			if err := json.Unmarshal(line, &r); err != nil {
				_ = f.Close()
				return nil, nil, fmt.Errorf("audit: corrupt line in %s: %w", path, err)
			}
			recs = append(recs, r)
			paths = append(paths, path)
		}
		err = sc.Err()
		_ = f.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return recs, paths, nil
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\n' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

// ExportBundle writes a machine-readable redacted JSON bundle to path.
func (s *Sink) ExportBundle(path string) error {
	if s == nil {
		return fmt.Errorf("audit: nil sink")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Flush active segment.
	if s.f != nil {
		_ = s.f.Sync()
	}
	recs, _, err := s.readAllLocked()
	if err != nil {
		return err
	}
	bundle := struct {
		SchemaVersion string   `json:"schemaVersion"`
		ExportedAt    string   `json:"exportedAt"`
		Redacted      bool     `json:"redacted"`
		Note          string   `json:"note"`
		Count         int      `json:"count"`
		Records       []Record `json:"records"`
	}{
		SchemaVersion: SchemaVersion,
		ExportedAt:    s.clock().UTC().Format(time.RFC3339Nano),
		Redacted:      true,
		Note:          "Security audit export (permission/sandbox/secret_ref/content_guard/admission/egress/toolchain). Not a session transcript.",
		Count:         len(recs),
		Records:       recs,
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Dir returns the audit root directory.
func (s *Sink) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}
