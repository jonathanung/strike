package replay

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

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/secret"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// RecordingSchemaVersion is the versioned recording document schema shared
// with multi-agent run snapshots (#782). Bump minor for additive fields.
const RecordingSchemaVersion = "1.0.0"

// Nondeterministic marker kinds on a recording timeline.
const (
	MarkerClock   = "clock"   // sleep, wall-clock dependent steps
	MarkerNetwork = "network" // webfetch / external I/O
	MarkerModel   = "model"   // sampled model text/reasoning
	MarkerEnv     = "env"     // cwd/date/environment layers
	MarkerID      = "id"      // random call/session ids (informational)
)

// SettingsDigest is the effective behavioral identity of a run. Shared with
// #782 run snapshots so multi-agent and solo recordings stay compatible.
type SettingsDigest struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Autonomy       string `json:"autonomy,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Fast           *bool  `json:"fast,omitempty"`
	// ToolsDigest is a stable hash of sorted unique tool names observed.
	ToolsDigest string `json:"toolsDigest,omitempty"`
	// PromptDigest hashes EffectivePrompt layer kind/source/chars (no previews).
	PromptDigest string `json:"promptDigest,omitempty"`
	// SystemChars from the last EffectivePrompt when present.
	SystemChars int `json:"systemChars,omitempty"`
}

// RepoIdentity is optional workspace identity for #782 multi-agent snapshots.
// Solo recordings may leave it empty.
type RepoIdentity struct {
	Commit     string `json:"commit,omitempty"`
	Dirty      bool   `json:"dirty,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	ProjectKey string `json:"projectKey,omitempty"`
}

// Marker labels a nondeterministic boundary in the event stream.
type Marker struct {
	// EventIndex is the 0-based index into the source event list.
	EventIndex int `json:"eventIndex"`
	// Kind is clock|network|model|env|id.
	Kind string `json:"kind"`
	// Reason is a short human/machine label (tool name, event type, …).
	Reason string `json:"reason"`
	// ToolIndex is set when the marker attaches to a root tool call
	// (-1 when not tool-related).
	ToolIndex int `json:"toolIndex"`
}

// ProviderAttempt captures one model stream attempt for replay metadata.
type ProviderAttempt struct {
	ProviderRequestID string `json:"providerRequestId,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	TurnID            string `json:"turnId,omitempty"`
	InputTokens       *int   `json:"inputTokens,omitempty"`
	OutputTokens      *int   `json:"outputTokens,omitempty"`
	Source            string `json:"source,omitempty"`
	RetryMessage      string `json:"retryMessage,omitempty"`
}

// HandoffSnapshot is a redacted CompletionHandoff for compare (#771).
type HandoffSnapshot struct {
	SessionID             string   `json:"sessionId,omitempty"`
	ChildSessionID        string   `json:"childSessionId,omitempty"`
	Status                string   `json:"status,omitempty"`
	Summary               string   `json:"summary,omitempty"`
	FilesChanged          []string `json:"filesChanged,omitempty"`
	Verification          string   `json:"verification,omitempty"`
	Findings              []string `json:"findings,omitempty"`
	Blockers              []string `json:"blockers,omitempty"`
	RecommendedNextAction string   `json:"recommendedNextAction,omitempty"`
	Incomplete            bool     `json:"incomplete,omitempty"`
}

// VerificationSnapshot is a redacted VerificationReport for compare (#780).
type VerificationSnapshot struct {
	SessionID      string   `json:"sessionId,omitempty"`
	ChildSessionID string   `json:"childSessionId,omitempty"`
	Passed         bool     `json:"passed"`
	Claimed        bool     `json:"claimed"`
	Verified       bool     `json:"verified"`
	Summary        string   `json:"summary,omitempty"`
	CheckNames     []string `json:"checkNames,omitempty"`
	CheckPassed    []bool   `json:"checkPassed,omitempty"`
}

// ToolResultSnapshot is a redacted, size-limited tool end payload.
type ToolResultSnapshot struct {
	Name    string `json:"name"`
	IsError bool   `json:"isError,omitempty"`
	// OutputDigest is sha256 of redacted output (not the raw body).
	OutputDigest string `json:"outputDigest,omitempty"`
	// OutputPreview is a short redacted preview for debugging.
	OutputPreview string `json:"outputPreview,omitempty"`
}

// Recording is a versioned, secret-redacted capture of a solo (or child)
// session for offline echo replay, branch-from-event, and run compare.
//
// Relationship:
//   - Session JSONL remains the durable full event log.
//   - Recording is a derived, compare-friendly artifact (settings digests,
//     tool sequence, handoff/gate fields, nondeterministic markers).
//   - Schema concepts are shared with #782 multi-agent run snapshots.
type Recording struct {
	SchemaVersion string    `json:"schemaVersion"`
	SessionID     string    `json:"sessionId,omitempty"`
	RecordedAt    time.Time `json:"recordedAt"`
	Redacted      bool      `json:"redacted"`
	// SideEffectsReplayed is always false for branch-from-event forks and
	// for recordings built from logs (tools are not re-executed to build it).
	SideEffectsReplayed bool `json:"sideEffectsReplayed"`

	Settings SettingsDigest `json:"settings"`
	Repo     *RepoIdentity  `json:"repo,omitempty"`

	EventCount int        `json:"eventCount"`
	UserInputs []string   `json:"userInputs,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	// ToolResults aligns with ToolCalls by index when ends were observed.
	ToolResults []ToolResultSnapshot `json:"toolResults,omitempty"`

	// ExitStatus is the last root TurnCompleted stop reason, or last root
	// child status when the recording is a child transcript.
	ExitStatus string `json:"exitStatus,omitempty"`
	Turns      int    `json:"turns,omitempty"`

	Markers          []Marker               `json:"markers,omitempty"`
	ProviderAttempts []ProviderAttempt      `json:"providerAttempts,omitempty"`
	Handoffs         []HandoffSnapshot      `json:"handoffs,omitempty"`
	Verifications    []VerificationSnapshot `json:"verifications,omitempty"`
	FilesChanged     []string               `json:"filesChanged,omitempty"`

	// ParentSessionID / DelegationID support #782 multi-agent identity.
	ParentSessionID string `json:"parentSessionId,omitempty"`
	DelegationID    string `json:"delegationId,omitempty"`

	Note string `json:"note,omitempty"`
}

// BuildRecording derives a redacted Recording from a protocol event list.
// Events are scrubbed again so callers may pass raw or already-redacted logs.
func BuildRecording(events []protocol.Event, opts RecordingOptions) Recording {
	opts = opts.withDefaults()
	redacted := make([]protocol.Event, len(events))
	for i, ev := range events {
		redacted[i] = secret.RedactEvent(ev)
	}

	rec := Recording{
		SchemaVersion:       RecordingSchemaVersion,
		SessionID:           opts.SessionID,
		RecordedAt:          opts.Clock(),
		Redacted:            true,
		SideEffectsReplayed: false,
		EventCount:          len(redacted),
		UserInputs:          ExtractUserInputs(redacted),
		ToolCalls:           ExtractToolCalls(redacted),
		Note:                "Derived run recording for echo replay/compare. Complements session JSONL; schema shared with #782 snapshots. Secrets scrubbed via internal/secret + pkg/redact.",
		ParentSessionID:     opts.ParentSessionID,
		DelegationID:        opts.DelegationID,
		Repo:                opts.Repo,
	}
	if rec.SessionID == "" {
		rec.SessionID = firstSessionID(redacted)
	}

	settings, promptDigest, systemChars := extractSettings(redacted)
	settings.ToolsDigest = toolsDigest(rec.ToolCalls)
	settings.PromptDigest = promptDigest
	settings.SystemChars = systemChars
	rec.Settings = settings

	rec.ToolResults = extractToolResults(redacted)
	rec.Markers = labelNondeterministic(redacted)
	rec.ProviderAttempts = extractProviderAttempts(redacted)
	rec.Handoffs, rec.Verifications = extractHandoffsAndGates(redacted)
	rec.FilesChanged = extractFilesChanged(redacted)
	rec.ExitStatus, rec.Turns = extractExitAndTurns(redacted)

	return rec
}

// RecordingOptions configure BuildRecording.
type RecordingOptions struct {
	SessionID       string
	ParentSessionID string
	DelegationID    string
	Repo            *RepoIdentity
	// Clock overrides time.Now for tests.
	Clock func() time.Time
}

func (o RecordingOptions) withDefaults() RecordingOptions {
	if o.Clock == nil {
		o.Clock = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// BuildRecordingFromJSONL loads a session log and builds a Recording.
func BuildRecordingFromJSONL(path string, opts RecordingOptions) (Recording, error) {
	events, err := LoadJSONL(path)
	if err != nil {
		return Recording{}, err
	}
	if opts.SessionID == "" {
		base := filepath.Base(path)
		opts.SessionID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return BuildRecording(events, opts), nil
}

// BuildRecordingFromResult builds a Recording from an echo replay Result.
func BuildRecordingFromResult(res Result, opts RecordingOptions) Recording {
	rec := BuildRecording(res.Events, opts)
	if len(res.UserInputs) > 0 {
		rec.UserInputs = append([]string(nil), res.UserInputs...)
	}
	if res.Turns > 0 {
		rec.Turns = res.Turns
	}
	if res.Effective != nil {
		// Prefer live inspect snapshot for prompt digest.
		d, chars := promptDigestFromEffective(*res.Effective)
		rec.Settings.PromptDigest = d
		rec.Settings.SystemChars = chars
	}
	return rec
}

// WriteRecording persists a Recording as pretty JSON (atomic replace).
func WriteRecording(path string, rec Recording) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("replay: create recording dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".strike-recording-*.tmp")
	if err != nil {
		return fmt.Errorf("replay: create recording temp: %w", err)
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
		return err
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// LoadRecording reads a Recording JSON document.
func LoadRecording(path string) (Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Recording{}, err
	}
	var rec Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return Recording{}, fmt.Errorf("replay: recording %s: %w", path, err)
	}
	return rec, nil
}

func extractSettings(events []protocol.Event) (SettingsDigest, string, int) {
	var s SettingsDigest
	var promptDigest string
	var systemChars int
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ModelSelected:
			if isRootCorr(e.Correlation) {
				s.Provider = e.Provider
				s.Model = e.Model
			}
		case protocol.AgentSelected:
			if isRootCorr(e.Correlation) {
				s.Agent = e.Name
			}
		case protocol.EffortSelected:
			if isRootCorr(e.Correlation) {
				s.Effort = string(e.Level)
			}
		case protocol.AutonomySelected:
			if isRootCorr(e.Correlation) {
				s.Autonomy = string(e.Mode)
			}
		case protocol.PermissionModeSelected:
			if isRootCorr(e.Correlation) {
				s.PermissionMode = string(e.Mode)
			}
		case protocol.FastSelected:
			if isRootCorr(e.Correlation) {
				v := e.Enabled
				s.Fast = &v
			}
		case protocol.EffectivePrompt:
			if isRootCorr(e.Correlation) {
				promptDigest, systemChars = promptDigestFromEffective(e)
			}
		}
	}
	return s, promptDigest, systemChars
}

func promptDigestFromEffective(ep protocol.EffectivePrompt) (string, int) {
	type layerKey struct {
		Kind   string `json:"kind"`
		Source string `json:"source"`
		Mode   string `json:"mode"`
		Chars  int    `json:"chars"`
	}
	keys := make([]layerKey, 0, len(ep.Layers))
	for _, layer := range ep.Layers {
		// Skip environment layer (date/cwd) so digests stay portable.
		if layer.Kind == protocol.PromptLayerEnvironment {
			continue
		}
		keys = append(keys, layerKey{
			Kind:   layer.Kind,
			Source: redact.String(layer.Source),
			Mode:   layer.Mode,
			Chars:  layer.Chars,
		})
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return "", ep.SystemChars
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), ep.SystemChars
}

func toolsDigest(calls []ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	set := make(map[string]struct{}, len(calls))
	for _, c := range calls {
		set[c.Name] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return hex.EncodeToString(sum[:8])
}

func extractToolResults(events []protocol.Event) []ToolResultSnapshot {
	// Map callID → name from begins; emit results in begin order for root only.
	type pending struct {
		name   string
		callID string
	}
	var order []pending
	byCall := make(map[string]ToolResultSnapshot)
	nameByCall := make(map[string]string)

	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ToolCallBegin:
			if !isRootCorr(e.Correlation) {
				continue
			}
			nameByCall[e.CallID] = e.Name
			order = append(order, pending{name: e.Name, callID: e.CallID})
		case protocol.ToolCallEnd:
			if !isRootCorr(e.Correlation) {
				continue
			}
			name := nameByCall[e.CallID]
			if name == "" {
				name = e.Title
			}
			out := redact.ScrubToolOutput(e.Output)
			sum := sha256.Sum256([]byte(out))
			byCall[e.CallID] = ToolResultSnapshot{
				Name:          name,
				IsError:       e.IsError,
				OutputDigest:  hex.EncodeToString(sum[:8]),
				OutputPreview: clipRunes(out, 120),
			}
		}
	}
	out := make([]ToolResultSnapshot, 0, len(order))
	for _, p := range order {
		if tr, ok := byCall[p.callID]; ok {
			out = append(out, tr)
		} else {
			out = append(out, ToolResultSnapshot{Name: p.name})
		}
	}
	return out
}

func labelNondeterministic(events []protocol.Event) []Marker {
	var markers []Marker
	toolIndex := -1
	for i, ev := range events {
		switch e := ev.(type) {
		case protocol.TextDelta:
			if isRootCorr(e.Correlation) {
				markers = append(markers, Marker{EventIndex: i, Kind: MarkerModel, Reason: "text.delta", ToolIndex: -1})
			}
		case protocol.ReasoningDelta:
			if isRootCorr(e.Correlation) {
				markers = append(markers, Marker{EventIndex: i, Kind: MarkerModel, Reason: "reasoning.delta", ToolIndex: -1})
			}
		case protocol.ToolCallBegin:
			if !isRootCorr(e.Correlation) {
				continue
			}
			toolIndex++
			kind, reason := toolNondeterminism(e.Name)
			if kind != "" {
				markers = append(markers, Marker{
					EventIndex: i,
					Kind:       kind,
					Reason:     reason,
					ToolIndex:  toolIndex,
				})
			}
		case protocol.EffectivePrompt:
			if !isRootCorr(e.Correlation) {
				continue
			}
			for _, layer := range e.Layers {
				if layer.Kind == protocol.PromptLayerEnvironment {
					markers = append(markers, Marker{
						EventIndex: i,
						Kind:       MarkerEnv,
						Reason:     "prompt.environment",
						ToolIndex:  -1,
					})
					break
				}
			}
		case protocol.ProcessStarted:
			// External processes may touch network/clock; mark when argv suggests it.
			if !isRootCorr(e.Correlation) {
				continue
			}
			joined := strings.ToLower(strings.Join(e.Argv, " "))
			if strings.Contains(joined, "curl") || strings.Contains(joined, "wget") || strings.Contains(joined, "http") {
				markers = append(markers, Marker{EventIndex: i, Kind: MarkerNetwork, Reason: "process.network", ToolIndex: -1})
			}
		}
	}
	return markers
}

func toolNondeterminism(name string) (kind, reason string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "webfetch", "web_fetch", "websearch", "web_search":
		return MarkerNetwork, "tool:" + name
	case "sleep":
		return MarkerClock, "tool:" + name
	default:
		return "", ""
	}
}

func extractProviderAttempts(events []protocol.Event) []ProviderAttempt {
	byID := make(map[string]*ProviderAttempt)
	var order []string
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.UsageReported:
			if !isRootCorr(e.Correlation) {
				continue
			}
			id := e.ProviderRequestID
			if id == "" {
				id = fmt.Sprintf("turn:%s:attempt:%d", e.TurnID, e.Attempt)
			}
			pa := byID[id]
			if pa == nil {
				pa = &ProviderAttempt{
					ProviderRequestID: e.ProviderRequestID,
					Attempt:           e.Attempt,
					TurnID:            e.TurnID,
				}
				byID[id] = pa
				order = append(order, id)
			}
			pa.Source = e.Source
			if e.Input.Known {
				n := e.Input.N
				pa.InputTokens = &n
			}
			if e.Output.Known {
				n := e.Output.N
				pa.OutputTokens = &n
			}
		case protocol.ProviderRetrying:
			if !isRootCorr(e.Correlation) {
				continue
			}
			id := e.ProviderRequestID
			if id == "" {
				continue
			}
			pa := byID[id]
			if pa == nil {
				pa = &ProviderAttempt{
					ProviderRequestID: e.ProviderRequestID,
					Attempt:           e.Attempt,
					TurnID:            e.TurnID,
				}
				byID[id] = pa
				order = append(order, id)
			}
			pa.RetryMessage = redact.String(e.Message)
		}
	}
	out := make([]ProviderAttempt, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func extractHandoffsAndGates(events []protocol.Event) ([]HandoffSnapshot, []VerificationSnapshot) {
	var handoffs []HandoffSnapshot
	var verifs []VerificationSnapshot
	for _, ev := range events {
		cc, ok := ev.(protocol.ChildCompleted)
		if !ok {
			continue
		}
		h := cc.Handoff
		handoffs = append(handoffs, HandoffSnapshot{
			SessionID:             cc.ParentSessionID,
			ChildSessionID:        cc.SessionID,
			Status:                string(cc.Status),
			Summary:               redact.String(h.Summary),
			FilesChanged:          append([]string(nil), h.FilesChanged...),
			Verification:          redact.String(h.Verification),
			Findings:              redactStringSlice(h.Findings),
			Blockers:              redactStringSlice(h.Blockers),
			RecommendedNextAction: redact.String(h.RecommendedNextAction),
			Incomplete:            h.Incomplete,
		})
		if cc.Verification != nil {
			v := cc.Verification
			names := make([]string, len(v.Checks))
			passed := make([]bool, len(v.Checks))
			for i, c := range v.Checks {
				names[i] = c.Name
				if names[i] == "" {
					names[i] = c.Kind + ":" + c.Value
				}
				passed[i] = c.Passed
			}
			verifs = append(verifs, VerificationSnapshot{
				SessionID:      cc.ParentSessionID,
				ChildSessionID: cc.SessionID,
				Passed:         v.Passed,
				Claimed:        v.Claimed,
				Verified:       v.Verified,
				Summary:        redact.String(v.Summary),
				CheckNames:     names,
				CheckPassed:    passed,
			})
		}
	}
	return handoffs, verifs
}

func extractFilesChanged(events []protocol.Event) []string {
	set := make(map[string]struct{})
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.TurnCompleted:
			if !isRootCorr(e.Correlation) {
				continue
			}
			for _, f := range e.Files {
				if p := strings.TrimSpace(f.Path); p != "" {
					set[p] = struct{}{}
				}
			}
		case protocol.ChildCompleted:
			for _, p := range e.Handoff.FilesChanged {
				if p = strings.TrimSpace(p); p != "" {
					set[p] = struct{}{}
				}
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func extractExitAndTurns(events []protocol.Event) (exit string, turns int) {
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.TurnCompleted:
			if e.ParentSessionID == "" && e.Depth == 0 {
				turns++
				exit = e.StopReason
			}
		case protocol.ChildCompleted:
			// Prefer child status when this log is primarily a child completion stream.
			if exit == "" {
				exit = string(e.Status)
			}
		case protocol.EngineError:
			if e.ParentSessionID == "" && e.Depth == 0 {
				exit = "error"
			}
		}
	}
	return exit, turns
}

func firstSessionID(events []protocol.Event) string {
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.UserMessage:
			if e.SessionID != "" {
				return e.SessionID
			}
		case protocol.ModelSelected:
			if e.SessionID != "" {
				return e.SessionID
			}
		case protocol.TurnStarted:
			if e.SessionID != "" {
				return e.SessionID
			}
		}
	}
	return ""
}

func isRootCorr(c protocol.Correlation) bool {
	return c.ParentSessionID == "" && c.Depth == 0
}

func redactStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact.String(s)
	}
	return out
}

func clipRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
