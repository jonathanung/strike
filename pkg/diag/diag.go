// Package diag builds a versioned, secret-redacted diagnostic bundle for
// support and debug: instruction precedence, prompt layer map (#167), and
// effective config digests (not full secret-bearing files).
//
// Relationship to other surfaces:
//   - EffectivePrompt / InspectEffectivePrompt remain the live inspect path
//     for the context doctor modal; the bundle packages that layer map with
//     session lineage and config digests for export.
//   - Timeline export (pkg/timeline, #790) is a run-trace view; this package
//     is configuration/prompt composition, not turn/tool spans.
//   - Secret scrubbing uses pkg/redact (shared with timeline, session scrub,
//     and engine inspect previews).
package diag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// SchemaVersion is the versioned export document schema (not the Op/Event wire).
const SchemaVersion = "1.0.0"

// Default note explaining what the bundle is (and is not).
const DefaultNote = "Prompt/config diagnostic bundle: ordered system-prompt layers with provenance, effective dials, and config digests. Complements session JSONL and timeline export; not a full transcript or secret-bearing config dump."

// Precedence is the documented composition order for system-prompt layers
// (kinds may be absent when inactive). Skills are user-turn content, not
// system layers — listed here for operator clarity only.
var Precedence = []string{
	protocol.PromptLayerShared,
	protocol.PromptLayerTools,
	protocol.PromptLayerConfig,   // config systemPrompt (build) — mutually exclusive with persona/provider
	protocol.PromptLayerPersona,  // agent persona
	protocol.PromptLayerProvider, // provider overlay default
	protocol.PromptLayerPhase,
	protocol.PromptLayerPlan,
	protocol.PromptLayerLean,
	protocol.PromptLayerEnvironment,
	protocol.PromptLayerInstruction, // AGENTS.md / CLAUDE.md blocks
	protocol.PromptLayerMemory,      // project memory autoload
	protocol.PromptLayerLedger,      // active decision/assumption ledger
	// skills: rendered into UserInput on slash invoke (not a system layer)
}

// Session carries lineage so solo and child sessions are distinguishable.
type Session struct {
	SessionID       string `json:"sessionId,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	RootSessionID   string `json:"rootSessionId,omitempty"`
	Depth           int    `json:"depth"`
	IsChild         bool   `json:"isChild,omitempty"`
}

// Prompt is the ordered layer map plus sizes (previews already redacted).
type Prompt struct {
	// Precedence documents the composition order (kind labels).
	Precedence     []string                         `json:"precedence"`
	Layers         []protocol.PromptLayerInfo       `json:"layers"`
	LayerCount     int                              `json:"layerCount"`
	SystemChars    int                              `json:"systemChars"`
	MessageCount   int                              `json:"messageCount"`
	FromLastStream bool                             `json:"fromLastStream,omitempty"`
	Attribution    protocol.RequestTokenAttribution `json:"attribution"`
}

// Compaction holds effective history-compaction / prune dials.
type Compaction struct {
	Strategy           string   `json:"strategy,omitempty"`
	Model              string   `json:"model,omitempty"`
	Threshold          float64  `json:"threshold,omitempty"`
	Buffer             int      `json:"buffer,omitempty"`
	KeepUserTurns      int      `json:"keepUserTurns,omitempty"`
	PruneProtectTokens int      `json:"pruneProtectTokens,omitempty"`
	PruneMinimumTokens int      `json:"pruneMinimumTokens,omitempty"`
	PruneKeepUserTurns int      `json:"pruneKeepUserTurns,omitempty"`
	PruneProtectTools  []string `json:"pruneProtectTools,omitempty"`
}

// Scheduler holds scheduler-relevant limits (pool capacities only).
type Scheduler struct {
	Limits map[string]int `json:"limits,omitempty"`
}

// Config is the effective runtime snapshot: dial values plus digests of
// non-secret material. Never includes API keys, tokens, or raw config files.
type Config struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Autonomy       string `json:"autonomy,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	LeanCode       string `json:"leanCode,omitempty"`
	Fast           bool   `json:"fast,omitempty"`
	MaxTokens      int    `json:"maxTokens,omitempty"`
	MaxChildDepth  int    `json:"maxChildDepth,omitempty"`
	ContextWindow  int    `json:"contextWindow,omitempty"`
	// TurnTimeoutS is the effective root-turn wall-clock deadline in seconds.
	// Negative means disabled; positive is the active bound. Zero is omitted
	// (callers should resolve defaults before Build).
	TurnTimeoutS int `json:"turnTimeoutS,omitempty"`
	// WorkDir and ProjectRoot are paths only (not file contents).
	WorkDir     string `json:"workDir,omitempty"`
	ProjectRoot string `json:"projectRoot,omitempty"`

	Compaction Compaction `json:"compaction"`
	Scheduler  Scheduler  `json:"scheduler"`

	// Digests maps stable names → hex SHA-256 of canonical non-secret JSON.
	// Keys: "effective" (this config snapshot without digests), "layers"
	// (kind/source/mode/chars only), optional caller-supplied extras.
	Digests map[string]string `json:"digests,omitempty"`
}

// Bundle is the versioned machine-readable diagnostic export document.
type Bundle struct {
	SchemaVersion   string    `json:"schemaVersion"`
	ProtocolVersion string    `json:"protocolVersion,omitempty"`
	StrikeVersion   string    `json:"strikeVersion,omitempty"`
	ExportedAt      time.Time `json:"exportedAt"`
	Redacted        bool      `json:"redacted"`
	Note            string    `json:"note,omitempty"`
	Session         Session   `json:"session"`
	Prompt          Prompt    `json:"prompt"`
	Config          Config    `json:"config"`
	Warnings        []string  `json:"warnings,omitempty"`
}

// Input is everything needed to assemble a Bundle. Callers supply already-
// redacted layer previews (engine inspect path) or raw text that Build will
// scrub via pkg/redact.
type Input struct {
	Session         Session
	Layers          []protocol.PromptLayerInfo
	SystemChars     int
	MessageCount    int
	FromLastStream  bool
	Attribution     protocol.RequestTokenAttribution
	Config          Config
	ProtocolVersion string
	StrikeVersion   string
	// ExtraDigests merges into Config.Digests after built-in digests.
	ExtraDigests map[string]string
	Warnings     []string
	// Clock overrides time.Now for tests.
	Clock func() time.Time
	// SkipBuiltinDigests leaves Digests empty except ExtraDigests (tests).
	SkipBuiltinDigests bool
}

// Build assembles a redacted, versioned Bundle from Input.
func Build(in Input) Bundle {
	clock := in.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	layers := redactLayers(in.Layers)
	cfg := redactConfig(in.Config)
	if !in.SkipBuiltinDigests {
		cfg.Digests = mergeDigests(builtinDigests(cfg, layers), in.ExtraDigests)
	} else if len(in.ExtraDigests) > 0 {
		cfg.Digests = mergeDigests(nil, in.ExtraDigests)
	}

	sess := in.Session
	if sess.ParentSessionID != "" || sess.Depth > 0 {
		sess.IsChild = true
	}
	// Paths may theoretically embed credential-shaped tokens; scrub.
	sess.SessionID = redact.String(sess.SessionID)
	sess.ParentSessionID = redact.String(sess.ParentSessionID)
	sess.RootSessionID = redact.String(sess.RootSessionID)

	return Bundle{
		SchemaVersion:   SchemaVersion,
		ProtocolVersion: strings.TrimSpace(in.ProtocolVersion),
		StrikeVersion:   redact.String(strings.TrimSpace(in.StrikeVersion)),
		ExportedAt:      clock().UTC(),
		Redacted:        true,
		Note:            DefaultNote,
		Session:         sess,
		Prompt: Prompt{
			Precedence:     append([]string(nil), Precedence...),
			Layers:         layers,
			LayerCount:     len(layers),
			SystemChars:    in.SystemChars,
			MessageCount:   in.MessageCount,
			FromLastStream: in.FromLastStream,
			Attribution:    in.Attribution,
		},
		Config:   cfg,
		Warnings: append([]string(nil), in.Warnings...),
	}
}

// ExportJSON writes a redacted Bundle as pretty JSON to path (atomic replace).
func ExportJSON(path string, b Bundle) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create diagnostic export directory: %w", err)
	}
	// Field-level redaction already ran in Build; do not scrub raw JSON bytes.
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".strike-diag-*.tmp")
	if err != nil {
		return fmt.Errorf("create diagnostic temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write diagnostic: %w", err)
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write diagnostic newline: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod diagnostic temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close diagnostic temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace diagnostic file: %w", err)
	}
	cleanup = false
	return nil
}

func redactLayers(in []protocol.PromptLayerInfo) []protocol.PromptLayerInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.PromptLayerInfo, len(in))
	for i, layer := range in {
		out[i] = protocol.PromptLayerInfo{
			Kind:    layer.Kind,
			Source:  redact.String(layer.Source),
			Mode:    layer.Mode,
			Chars:   layer.Chars,
			Preview: redact.String(layer.Preview),
		}
	}
	return out
}

func redactConfig(c Config) Config {
	c.Provider = redact.String(c.Provider)
	c.Model = redact.String(c.Model)
	c.Agent = redact.String(c.Agent)
	c.Effort = redact.String(c.Effort)
	c.Autonomy = redact.String(c.Autonomy)
	c.PermissionMode = redact.String(c.PermissionMode)
	c.Sandbox = redact.String(c.Sandbox)
	c.LeanCode = redact.String(c.LeanCode)
	c.WorkDir = redact.String(c.WorkDir)
	c.ProjectRoot = redact.String(c.ProjectRoot)
	c.Compaction.Strategy = redact.String(c.Compaction.Strategy)
	c.Compaction.Model = redact.String(c.Compaction.Model)
	if len(c.Compaction.PruneProtectTools) > 0 {
		tools := make([]string, len(c.Compaction.PruneProtectTools))
		for i, t := range c.Compaction.PruneProtectTools {
			tools[i] = redact.String(t)
		}
		c.Compaction.PruneProtectTools = tools
	}
	if len(c.Scheduler.Limits) > 0 {
		lim := make(map[string]int, len(c.Scheduler.Limits))
		for k, v := range c.Scheduler.Limits {
			lim[redact.String(k)] = v
		}
		c.Scheduler.Limits = lim
	}
	// Digests recomputed by Build; drop caller digests that may be stale.
	c.Digests = nil
	return c
}

func builtinDigests(cfg Config, layers []protocol.PromptLayerInfo) map[string]string {
	out := make(map[string]string, 2)
	// effective: config snapshot without digests field.
	if raw, err := json.Marshal(cfg); err == nil {
		out["effective"] = sha256Hex(raw)
	}
	// layers: kind/source/mode/chars only (no previews).
	type layerDigest struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Mode   string `json:"mode"`
		Chars  int    `json:"chars"`
	}
	ld := make([]layerDigest, 0, len(layers))
	for _, l := range layers {
		ld = append(ld, layerDigest{Kind: l.Kind, Source: l.Source, Mode: l.Mode, Chars: l.Chars})
	}
	if raw, err := json.Marshal(ld); err == nil {
		out["layers"] = sha256Hex(raw)
	}
	return out
}

func mergeDigests(base, extra map[string]string) map[string]string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		if k = strings.TrimSpace(k); k != "" && v != "" {
			out[k] = v
		}
	}
	for k, v := range extra {
		if k = strings.TrimSpace(k); k != "" && v != "" {
			out[redact.String(k)] = redact.String(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DigestJSON returns the hex SHA-256 of canonical JSON for v (for callers
// that want an extra digest of non-secret material).
func DigestJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

// SortedLimitKeys returns scheduler limit keys in stable order (tests/docs).
func SortedLimitKeys(limits map[string]int) []string {
	if len(limits) == 0 {
		return nil
	}
	keys := make([]string, 0, len(limits))
	for k := range limits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ToProtocol maps a Bundle onto the wire DiagnosticBundle event payload
// (Correlation must be set by the caller).
func ToProtocol(b Bundle) protocol.DiagnosticBundle {
	return protocol.DiagnosticBundle{
		SchemaVersion:   b.SchemaVersion,
		ProtocolVersion: b.ProtocolVersion,
		StrikeVersion:   b.StrikeVersion,
		ExportedAt:      b.ExportedAt,
		Redacted:        b.Redacted,
		Note:            b.Note,
		Session: protocol.DiagnosticSession{
			SessionID:       b.Session.SessionID,
			ParentSessionID: b.Session.ParentSessionID,
			RootSessionID:   b.Session.RootSessionID,
			Depth:           b.Session.Depth,
			IsChild:         b.Session.IsChild,
		},
		Prompt: protocol.DiagnosticPrompt{
			Precedence:     append([]string(nil), b.Prompt.Precedence...),
			Layers:         append([]protocol.PromptLayerInfo(nil), b.Prompt.Layers...),
			LayerCount:     b.Prompt.LayerCount,
			SystemChars:    b.Prompt.SystemChars,
			MessageCount:   b.Prompt.MessageCount,
			FromLastStream: b.Prompt.FromLastStream,
			Attribution:    b.Prompt.Attribution,
		},
		Config:   configToProtocol(b.Config),
		Warnings: append([]string(nil), b.Warnings...),
	}
}

// FromProtocol rebuilds a Bundle from a wire DiagnosticBundle event.
// Always re-runs Build so export paths scrub secrets even if a caller
// constructed a non-redacted event (defense in depth).
func FromProtocol(ev protocol.DiagnosticBundle) Bundle {
	cfg := configFromProtocol(ev.Config)
	// Preserve digests from the wire event as extras (Build recomputes builtins).
	extra := cfg.Digests
	cfg.Digests = nil
	b := Build(Input{
		Session: Session{
			SessionID:       firstNonEmpty(ev.Session.SessionID, ev.SessionID),
			ParentSessionID: firstNonEmpty(ev.Session.ParentSessionID, ev.ParentSessionID),
			RootSessionID:   ev.Session.RootSessionID,
			Depth:           firstDepth(ev.Session.Depth, ev.Depth),
			IsChild:         ev.Session.IsChild,
		},
		Layers:          append([]protocol.PromptLayerInfo(nil), ev.Prompt.Layers...),
		SystemChars:     ev.Prompt.SystemChars,
		MessageCount:    ev.Prompt.MessageCount,
		FromLastStream:  ev.Prompt.FromLastStream,
		Attribution:     ev.Prompt.Attribution,
		Config:          cfg,
		ProtocolVersion: ev.ProtocolVersion,
		StrikeVersion:   ev.StrikeVersion,
		ExtraDigests:    extra,
		Warnings:        append([]string(nil), ev.Warnings...),
		Clock: func() time.Time {
			if ev.ExportedAt.IsZero() {
				return time.Now().UTC()
			}
			return ev.ExportedAt.UTC()
		},
	})
	// Prefer wire schema/note when present (forward-compat).
	if sv := strings.TrimSpace(ev.SchemaVersion); sv != "" {
		b.SchemaVersion = sv
	}
	if n := strings.TrimSpace(ev.Note); n != "" {
		b.Note = n
	}
	return b
}

func configToProtocol(c Config) protocol.DiagnosticConfig {
	var lim map[string]int
	if len(c.Scheduler.Limits) > 0 {
		lim = make(map[string]int, len(c.Scheduler.Limits))
		for k, v := range c.Scheduler.Limits {
			lim[k] = v
		}
	}
	var dig map[string]string
	if len(c.Digests) > 0 {
		dig = make(map[string]string, len(c.Digests))
		for k, v := range c.Digests {
			dig[k] = v
		}
	}
	return protocol.DiagnosticConfig{
		Provider:       c.Provider,
		Model:          c.Model,
		Agent:          c.Agent,
		Effort:         c.Effort,
		Autonomy:       c.Autonomy,
		PermissionMode: c.PermissionMode,
		Sandbox:        c.Sandbox,
		LeanCode:       c.LeanCode,
		Fast:           c.Fast,
		MaxTokens:      c.MaxTokens,
		MaxChildDepth:  c.MaxChildDepth,
		ContextWindow:  c.ContextWindow,
		TurnTimeoutS:   c.TurnTimeoutS,
		WorkDir:        c.WorkDir,
		ProjectRoot:    c.ProjectRoot,
		Compaction: protocol.DiagnosticCompaction{
			Strategy:           c.Compaction.Strategy,
			Model:              c.Compaction.Model,
			Threshold:          c.Compaction.Threshold,
			Buffer:             c.Compaction.Buffer,
			KeepUserTurns:      c.Compaction.KeepUserTurns,
			PruneProtectTokens: c.Compaction.PruneProtectTokens,
			PruneMinimumTokens: c.Compaction.PruneMinimumTokens,
			PruneKeepUserTurns: c.Compaction.PruneKeepUserTurns,
			PruneProtectTools:  append([]string(nil), c.Compaction.PruneProtectTools...),
		},
		Scheduler: protocol.DiagnosticScheduler{Limits: lim},
		Digests:   dig,
	}
}

func configFromProtocol(c protocol.DiagnosticConfig) Config {
	var lim map[string]int
	if len(c.Scheduler.Limits) > 0 {
		lim = make(map[string]int, len(c.Scheduler.Limits))
		for k, v := range c.Scheduler.Limits {
			lim[k] = v
		}
	}
	var dig map[string]string
	if len(c.Digests) > 0 {
		dig = make(map[string]string, len(c.Digests))
		for k, v := range c.Digests {
			dig[k] = v
		}
	}
	return Config{
		Provider:       c.Provider,
		Model:          c.Model,
		Agent:          c.Agent,
		Effort:         c.Effort,
		Autonomy:       c.Autonomy,
		PermissionMode: c.PermissionMode,
		Sandbox:        c.Sandbox,
		LeanCode:       c.LeanCode,
		Fast:           c.Fast,
		MaxTokens:      c.MaxTokens,
		MaxChildDepth:  c.MaxChildDepth,
		ContextWindow:  c.ContextWindow,
		TurnTimeoutS:   c.TurnTimeoutS,
		WorkDir:        c.WorkDir,
		ProjectRoot:    c.ProjectRoot,
		Compaction: Compaction{
			Strategy:           c.Compaction.Strategy,
			Model:              c.Compaction.Model,
			Threshold:          c.Compaction.Threshold,
			Buffer:             c.Compaction.Buffer,
			KeepUserTurns:      c.Compaction.KeepUserTurns,
			PruneProtectTokens: c.Compaction.PruneProtectTokens,
			PruneMinimumTokens: c.Compaction.PruneMinimumTokens,
			PruneKeepUserTurns: c.Compaction.PruneKeepUserTurns,
			PruneProtectTools:  append([]string(nil), c.Compaction.PruneProtectTools...),
		},
		Scheduler: Scheduler{Limits: lim},
		Digests:   dig,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstDepth(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
