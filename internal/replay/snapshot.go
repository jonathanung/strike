package replay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// RunSnapshotSchemaVersion is the multi-agent run snapshot document schema (#782).
// Shares field concepts with RecordingSchemaVersion (settings, repo, handoff/gate).
// Bump minor for additive fields.
const RunSnapshotSchemaVersion = "1.0.0"

// Snapshot phases.
const (
	SnapshotPhaseStart    = "start"
	SnapshotPhaseComplete = "complete"
)

// ConfigDigest captures behavior-relevant config for a delegated run (v1).
// Optional fields may be empty when unknown at capture time.
type ConfigDigest struct {
	LeanCode      string `json:"leanCode,omitempty"`
	MaxChildDepth int    `json:"maxChildDepth,omitempty"`
	// Budget is a stable projection of agent budget limits (zeros omitted).
	BudgetMaxWallClockS     int     `json:"budgetMaxWallClockS,omitempty"`
	BudgetMaxTokens         int     `json:"budgetMaxTokens,omitempty"`
	BudgetMaxCostUSD        float64 `json:"budgetMaxCostUSD,omitempty"`
	BudgetMaxToolCalls      int     `json:"budgetMaxToolCalls,omitempty"`
	BudgetMaxDangerousTools int     `json:"budgetMaxDangerousTools,omitempty"`
	// Digest is sha256 of the stable JSON projection (empty when all zero/empty).
	Digest string `json:"digest,omitempty"`
}

// GateSpecSnapshot is a configured verification gate at spawn (not a result).
type GateSpecSnapshot struct {
	Name  string `json:"name,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Value string `json:"value,omitempty"`
}

// RunSnapshot is a multi-agent delegated-run capture for reproducible debugging
// and offline echo replay (#782).
//
// Relationship to session JSONL:
//   - Session JSONL remains the durable full event transcript (parent + child logs).
//   - RunSnapshot is a compact, secret-redacted, exportable unit: spawn identity
//     (prompt/bundle/model/tools/permissions/repo/config) plus optional completion
//     outcome (handoff, gates, exit) and an optional derived Recording of the
//     child event stream. It complements JSONL; it does not duplicate the entire
//     transcript.
//
// Schema field concepts are shared with Recording (#791) so solo and multi-agent
// compare tooling stay compatible.
type RunSnapshot struct {
	SchemaVersion string    `json:"schemaVersion"`
	SnapshotID    string    `json:"snapshotId"`
	Phase         string    `json:"phase"` // start | complete
	CapturedAt    time.Time `json:"capturedAt"`
	// CompletedAt is set when phase is complete (may equal CapturedAt if only
	// completion was built in one shot).
	CompletedAt time.Time `json:"completedAt,omitempty"`
	Redacted    bool      `json:"redacted"`

	// Identity
	DelegationID    string `json:"delegationId,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	LeadSessionID   string `json:"leadSessionId,omitempty"`

	// Spawn inputs (redacted)
	Prompt       string `json:"prompt,omitempty"`
	PromptDigest string `json:"promptDigest,omitempty"`
	Agent        string `json:"agent,omitempty"`
	// Name is the optional teammate alias.
	Name        string         `json:"name,omitempty"`
	RouteReason string         `json:"routeReason,omitempty"`
	Settings    SettingsDigest `json:"settings"`
	// ToolAllowList is the explicit tool names available at spawn when known.
	ToolAllowList []string `json:"toolAllowList,omitempty"`
	// ContextBundle is the sealed spawn package (#779), ids/paths/digests only.
	ContextBundle *ContextBundleSnapshot `json:"contextBundle,omitempty"`
	Config        ConfigDigest           `json:"config,omitempty"`
	Repo          *RepoIdentity          `json:"repo,omitempty"`
	// VerifyGates are configured independent gates at spawn (#780).
	VerifyGates []GateSpecSnapshot `json:"verifyGates,omitempty"`
	Criteria    []string           `json:"criteria,omitempty"`

	// Completion (phase=complete)
	ExitStatus   string                `json:"exitStatus,omitempty"`
	Handoff      *HandoffSnapshot      `json:"handoff,omitempty"`
	Verification *VerificationSnapshot `json:"verification,omitempty"`
	// Recording is an optional derived child-session recording for tool-sequence
	// compare and echo divergence checks.
	Recording *Recording `json:"recording,omitempty"`

	Note string `json:"note,omitempty"`
}

// SnapshotOptions configure BuildStartSnapshot / CompleteRunSnapshot.
type SnapshotOptions struct {
	SnapshotID      string
	DelegationID    string
	ParentSessionID string
	ChildSessionID  string
	LeadSessionID   string
	Prompt          string
	Agent           string
	Name            string
	RouteReason     string
	Settings        SettingsDigest
	ToolAllowList   []string
	ContextBundle   *ContextBundleSnapshot
	// ProtocolBundle is converted when ContextBundle is nil.
	ProtocolBundle *protocol.ContextBundle
	Config         ConfigDigest
	Repo           *RepoIdentity
	VerifyGates    []GateSpecSnapshot
	Criteria       []string
	// Clock overrides time.Now for tests.
	Clock func() time.Time
}

func (o SnapshotOptions) withDefaults() SnapshotOptions {
	if o.Clock == nil {
		o.Clock = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// BuildStartSnapshot captures spawn-time identity for a delegated run.
// Secrets in prompt/bundle text are redacted. Does not include completion fields.
func BuildStartSnapshot(opts SnapshotOptions) RunSnapshot {
	opts = opts.withDefaults()
	id := strings.TrimSpace(opts.SnapshotID)
	if id == "" {
		id = newSnapshotID(opts.Clock())
	}
	bundle := opts.ContextBundle
	if bundle == nil && opts.ProtocolBundle != nil {
		b := contextBundleSnapshotFromProtocol(opts.ChildSessionID, opts.ProtocolBundle)
		bundle = &b
	}
	prompt := redact.String(opts.Prompt)
	tools := append([]string(nil), opts.ToolAllowList...)
	sort.Strings(tools)
	cfg := finalizeConfigDigest(opts.Config)
	settings := opts.Settings
	if settings.ToolsDigest == "" && len(tools) > 0 {
		settings.ToolsDigest = toolsDigestFromNames(tools)
	}
	if settings.Agent == "" {
		settings.Agent = strings.TrimSpace(opts.Agent)
	}
	return RunSnapshot{
		SchemaVersion:   RunSnapshotSchemaVersion,
		SnapshotID:      id,
		Phase:           SnapshotPhaseStart,
		CapturedAt:      opts.Clock(),
		Redacted:        true,
		DelegationID:    strings.TrimSpace(opts.DelegationID),
		ParentSessionID: strings.TrimSpace(opts.ParentSessionID),
		ChildSessionID:  strings.TrimSpace(opts.ChildSessionID),
		LeadSessionID:   strings.TrimSpace(opts.LeadSessionID),
		Prompt:          prompt,
		PromptDigest:    digestText(prompt),
		Agent:           strings.TrimSpace(opts.Agent),
		Name:            strings.TrimSpace(opts.Name),
		RouteReason:     redact.String(opts.RouteReason),
		Settings:        settings,
		ToolAllowList:   tools,
		ContextBundle:   bundle,
		Config:          cfg,
		Repo:            cloneRepo(opts.Repo),
		VerifyGates:     cloneGates(opts.VerifyGates),
		Criteria:        redactStringSlice(opts.Criteria),
		Note:            "Multi-agent run snapshot (#782). Complements session JSONL; does not replace the full event transcript. Secrets scrubbed via pkg/redact.",
	}
}

// CompleteRunSnapshot merges a start snapshot with a ChildCompleted outcome and
// optional child session events (for a derived Recording). Phase becomes complete.
func CompleteRunSnapshot(start RunSnapshot, completed protocol.ChildCompleted, childEvents []protocol.Event, opts SnapshotOptions) RunSnapshot {
	opts = opts.withDefaults()
	out := start
	if out.SnapshotID == "" {
		out.SnapshotID = newSnapshotID(opts.Clock())
	}
	out.SchemaVersion = RunSnapshotSchemaVersion
	out.Phase = SnapshotPhaseComplete
	out.Redacted = true
	out.CompletedAt = opts.Clock()
	if out.CapturedAt.IsZero() {
		out.CapturedAt = out.CompletedAt
	}
	if out.ChildSessionID == "" {
		out.ChildSessionID = completed.SessionID
	}
	if out.ParentSessionID == "" {
		out.ParentSessionID = completed.ParentSessionID
	}
	if out.DelegationID == "" {
		out.DelegationID = strings.TrimSpace(completed.DelegationID)
	}
	if out.Name == "" {
		out.Name = strings.TrimSpace(completed.Name)
	}
	out.ExitStatus = string(completed.Status)

	// Handoff + verification from completion (reuse extract helpers via synthetic list).
	hs, vs := extractHandoffsAndGates([]protocol.Event{completed})
	if len(hs) > 0 {
		h := hs[0]
		out.Handoff = &h
	}
	if len(vs) > 0 {
		v := vs[0]
		out.Verification = &v
	} else if completed.Verification != nil {
		// extract only when ChildCompleted has Verification; already handled.
	}

	if len(childEvents) > 0 {
		rec := BuildRecording(childEvents, RecordingOptions{
			SessionID:       out.ChildSessionID,
			ParentSessionID: out.ParentSessionID,
			DelegationID:    out.DelegationID,
			Repo:            out.Repo,
			Clock:           opts.Clock,
		})
		// Prefer child settings when start left them empty.
		out.Settings = mergeSettings(out.Settings, rec.Settings)
		if out.ExitStatus == "" {
			out.ExitStatus = rec.ExitStatus
		}
		out.Recording = &rec
	}

	// Fill optional opts overlays (config/repo/tools) when start lacked them.
	if out.Repo == nil && opts.Repo != nil {
		out.Repo = cloneRepo(opts.Repo)
	}
	if out.Config.Digest == "" && (opts.Config != ConfigDigest{}) {
		out.Config = finalizeConfigDigest(opts.Config)
	}
	if len(out.ToolAllowList) == 0 && len(opts.ToolAllowList) > 0 {
		tools := append([]string(nil), opts.ToolAllowList...)
		sort.Strings(tools)
		out.ToolAllowList = tools
		if out.Settings.ToolsDigest == "" {
			out.Settings.ToolsDigest = toolsDigestFromNames(tools)
		}
	}
	if out.Note == "" {
		out.Note = "Multi-agent run snapshot (#782). Complements session JSONL; does not replace the full event transcript. Secrets scrubbed via pkg/redact."
	}
	return out
}

// ExtractOptions configure ExtractRunSnapshots.
type ExtractOptions struct {
	// LeadSessionID stamps every snapshot when set.
	LeadSessionID string
	// Repo / Config apply to all extracted snapshots when set.
	Repo   *RepoIdentity
	Config ConfigDigest
	// ChildEventsBySession, when set, attaches a Recording for matching child ids.
	ChildEventsBySession map[string][]protocol.Event
	// ToolAllowListBySession optional spawn tool lists keyed by child session id.
	ToolAllowListBySession map[string][]string
	// SettingsBySession optional settings keyed by child session id.
	SettingsBySession map[string]SettingsDigest
	// DelegationIDBySession maps child session → delegation id when ChildCompleted
	// omitted it (older logs).
	DelegationIDBySession map[string]string
	Clock                 func() time.Time
}

// ExtractRunSnapshots builds one RunSnapshot per ChildStarted on the parent
// event stream, completing it when a matching ChildCompleted is present.
// Parent JSONL alone is enough for spawn + handoff/gate fields; pass child
// event maps for full Recording/settings digests.
func ExtractRunSnapshots(parentEvents []protocol.Event, opts ExtractOptions) []RunSnapshot {
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	type pending struct {
		start RunSnapshot
		order int
	}
	byChild := make(map[string]*pending)
	var order []string

	for _, ev := range parentEvents {
		switch e := ev.(type) {
		case protocol.ChildStarted:
			childID := e.SessionID
			if childID == "" {
				continue
			}
			var bundle *ContextBundleSnapshot
			if e.ContextBundle != nil {
				b := contextBundleSnapshotFromProtocol(childID, e.ContextBundle)
				bundle = &b
			}
			var tools []string
			if opts.ToolAllowListBySession != nil {
				tools = opts.ToolAllowListBySession[childID]
			}
			var settings SettingsDigest
			if opts.SettingsBySession != nil {
				settings = opts.SettingsBySession[childID]
			}
			if settings.Agent == "" {
				settings.Agent = e.Agent
			}
			start := BuildStartSnapshot(SnapshotOptions{
				DelegationID:    lookupDeleg(opts.DelegationIDBySession, childID),
				ParentSessionID: e.ParentSessionID,
				ChildSessionID:  childID,
				LeadSessionID:   opts.LeadSessionID,
				Prompt:          e.Prompt,
				Agent:           e.Agent,
				Name:            e.Name,
				RouteReason:     e.RouteReason,
				Settings:        settings,
				ToolAllowList:   tools,
				ContextBundle:   bundle,
				Config:          opts.Config,
				Repo:            opts.Repo,
				Clock:           opts.Clock,
			})
			byChild[childID] = &pending{start: start, order: len(order)}
			order = append(order, childID)
		case protocol.ChildCompleted:
			childID := e.SessionID
			p := byChild[childID]
			var childEvs []protocol.Event
			if opts.ChildEventsBySession != nil {
				childEvs = opts.ChildEventsBySession[childID]
			}
			if p == nil {
				// Completion without start: synthesize minimal start.
				start := BuildStartSnapshot(SnapshotOptions{
					DelegationID:    firstNonEmpty(e.DelegationID, lookupDeleg(opts.DelegationIDBySession, childID)),
					ParentSessionID: e.ParentSessionID,
					ChildSessionID:  childID,
					LeadSessionID:   opts.LeadSessionID,
					Name:            e.Name,
					Config:          opts.Config,
					Repo:            opts.Repo,
					Clock:           opts.Clock,
				})
				p = &pending{start: start, order: len(order)}
				byChild[childID] = p
				order = append(order, childID)
			}
			// Prefer delegation id from completion.
			if id := strings.TrimSpace(e.DelegationID); id != "" {
				p.start.DelegationID = id
			}
			p.start = CompleteRunSnapshot(p.start, e, childEvs, SnapshotOptions{
				Config: opts.Config,
				Repo:   opts.Repo,
				Clock:  opts.Clock,
			})
		}
	}

	out := make([]RunSnapshot, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, id := range order {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if p := byChild[id]; p != nil {
			out = append(out, p.start)
		}
	}
	return out
}

// WriteRunSnapshot persists a RunSnapshot as pretty JSON (atomic replace).
func WriteRunSnapshot(path string, snap RunSnapshot) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("replay: create snapshot dir: %w", err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".strike-runsnap-*.tmp")
	if err != nil {
		return fmt.Errorf("replay: create snapshot temp: %w", err)
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

// LoadRunSnapshot reads a RunSnapshot JSON document.
func LoadRunSnapshot(path string) (RunSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunSnapshot{}, err
	}
	var snap RunSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return RunSnapshot{}, fmt.Errorf("replay: run snapshot %s: %w", path, err)
	}
	return snap, nil
}

// DefaultRunsDir is ~/.strike/runs — session-scoped snapshot files live under
// <runsDir>/<parentSessionID>/*.json.
func DefaultRunsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "runs")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "runs")
}

// SnapshotPath is the default on-disk path for a snapshot under runsDir.
// Layout: <runsDir>/<parentOrLead>/<snapshotID>.json
func SnapshotPath(runsDir, parentOrLeadSessionID, snapshotID string) string {
	parent := sanitizePathPart(parentOrLeadSessionID)
	if parent == "" {
		parent = "_unknown"
	}
	id := sanitizePathPart(snapshotID)
	if id == "" {
		id = "snap"
	}
	return filepath.Join(runsDir, parent, id+".json")
}

// PersistRunSnapshot writes snap under runsDir using SnapshotPath.
// Returns the path written.
func PersistRunSnapshot(runsDir string, snap RunSnapshot) (string, error) {
	if runsDir == "" {
		runsDir = DefaultRunsDir()
	}
	parent := snap.ParentSessionID
	if parent == "" {
		parent = snap.LeadSessionID
	}
	if parent == "" {
		parent = snap.ChildSessionID
	}
	id := snap.SnapshotID
	if id == "" {
		id = newSnapshotID(time.Now().UTC())
		snap.SnapshotID = id
	}
	path := SnapshotPath(runsDir, parent, id)
	if err := WriteRunSnapshot(path, snap); err != nil {
		return "", err
	}
	return path, nil
}

// CaptureRepoIdentity best-effort fills RepoIdentity from a workspace path.
// Missing git or errors yield a partial identity (worktree path only).
func CaptureRepoIdentity(workDir, projectKey string) *RepoIdentity {
	workDir = strings.TrimSpace(workDir)
	ri := &RepoIdentity{
		Worktree:   workDir,
		ProjectKey: strings.TrimSpace(projectKey),
	}
	if workDir == "" {
		return ri
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// HEAD commit
	if out, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "HEAD").Output(); err == nil {
		ri.Commit = strings.TrimSpace(string(out))
	}
	// dirty bit
	if out, err := exec.CommandContext(ctx, "git", "-C", workDir, "status", "--porcelain").Output(); err == nil {
		ri.Dirty = len(strings.TrimSpace(string(out))) > 0
	}
	return ri
}

// ReplayRunSnapshot re-runs the snapshot prompt via the offline echo provider.
// Uses Prompt when set; otherwise falls back to Recording.UserInputs.
// Does not re-execute live side effects from the original child tools unless
// the echo script in the prompt requests them (same as replay.Run).
func ReplayRunSnapshot(ctx context.Context, snap RunSnapshot, opts Options) (Result, error) {
	inputs := snapshotReplayInputs(snap)
	if len(inputs) == 0 {
		return Result{}, fmt.Errorf("replay: run snapshot %s has no prompt or user inputs", snap.SnapshotID)
	}
	if opts.WorkDir == "" && snap.Repo != nil && snap.Repo.Worktree != "" {
		opts.WorkDir = snap.Repo.Worktree
	}
	return Run(ctx, inputs, opts)
}

func snapshotReplayInputs(snap RunSnapshot) []string {
	if p := strings.TrimSpace(snap.Prompt); p != "" {
		return []string{p}
	}
	if snap.Recording != nil && len(snap.Recording.UserInputs) > 0 {
		return append([]string(nil), snap.Recording.UserInputs...)
	}
	return nil
}

// RecordingFromSnapshot returns the embedded Recording or a minimal one built
// from completion fields for CompareRecordings.
func RecordingFromSnapshot(snap RunSnapshot) Recording {
	if snap.Recording != nil {
		rec := *snap.Recording
		if rec.ParentSessionID == "" {
			rec.ParentSessionID = snap.ParentSessionID
		}
		if rec.DelegationID == "" {
			rec.DelegationID = snap.DelegationID
		}
		if rec.Repo == nil {
			rec.Repo = snap.Repo
		}
		return rec
	}
	rec := Recording{
		SchemaVersion:   RecordingSchemaVersion,
		SessionID:       snap.ChildSessionID,
		RecordedAt:      snap.CapturedAt,
		Redacted:        true,
		Settings:        snap.Settings,
		Repo:            snap.Repo,
		ExitStatus:      snap.ExitStatus,
		ParentSessionID: snap.ParentSessionID,
		DelegationID:    snap.DelegationID,
		Note:            "Synthetic recording from RunSnapshot completion fields",
	}
	if snap.Handoff != nil {
		rec.Handoffs = []HandoffSnapshot{*snap.Handoff}
		rec.FilesChanged = append([]string(nil), snap.Handoff.FilesChanged...)
	}
	if snap.Verification != nil {
		rec.Verifications = []VerificationSnapshot{*snap.Verification}
	}
	if snap.ContextBundle != nil {
		rec.ContextBundles = []ContextBundleSnapshot{*snap.ContextBundle}
	}
	if p := strings.TrimSpace(snap.Prompt); p != "" {
		rec.UserInputs = []string{p}
	}
	return rec
}

func contextBundleSnapshotFromProtocol(childID string, b *protocol.ContextBundle) ContextBundleSnapshot {
	if b == nil {
		return ContextBundleSnapshot{}
	}
	snap := ContextBundleSnapshot{
		ChildSessionID: childID,
		Goal:           redact.String(b.Goal),
		Acceptance:     redactStringSlice(b.Acceptance),
		AllowedPaths:   append([]string(nil), b.AllowedPaths...),
		RequiredPaths:  append([]string(nil), b.RequiredPaths...),
		Constraints:    redactStringSlice(b.Constraints),
	}
	for _, a := range b.Artifacts {
		if id := strings.TrimSpace(a.ID); id != "" {
			snap.ArtifactIDs = append(snap.ArtifactIDs, id)
		}
	}
	for _, it := range b.Items {
		if id := strings.TrimSpace(it.ID); id != "" {
			snap.ItemIDs = append(snap.ItemIDs, id)
		}
	}
	for _, p := range b.FilePins {
		if path := strings.TrimSpace(p.Path); path != "" {
			snap.FilePinPaths = append(snap.FilePinPaths, path)
		}
	}
	snap.Digest = contextBundleDigest(snap)
	return snap
}

func finalizeConfigDigest(c ConfigDigest) ConfigDigest {
	type proj struct {
		LeanCode                string  `json:"leanCode,omitempty"`
		MaxChildDepth           int     `json:"maxChildDepth,omitempty"`
		BudgetMaxWallClockS     int     `json:"budgetMaxWallClockS,omitempty"`
		BudgetMaxTokens         int     `json:"budgetMaxTokens,omitempty"`
		BudgetMaxCostUSD        float64 `json:"budgetMaxCostUSD,omitempty"`
		BudgetMaxToolCalls      int     `json:"budgetMaxToolCalls,omitempty"`
		BudgetMaxDangerousTools int     `json:"budgetMaxDangerousTools,omitempty"`
	}
	p := proj{
		LeanCode:                c.LeanCode,
		MaxChildDepth:           c.MaxChildDepth,
		BudgetMaxWallClockS:     c.BudgetMaxWallClockS,
		BudgetMaxTokens:         c.BudgetMaxTokens,
		BudgetMaxCostUSD:        c.BudgetMaxCostUSD,
		BudgetMaxToolCalls:      c.BudgetMaxToolCalls,
		BudgetMaxDangerousTools: c.BudgetMaxDangerousTools,
	}
	if p == (proj{}) {
		return ConfigDigest{}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return c
	}
	sum := sha256.Sum256(raw)
	c.Digest = hex.EncodeToString(sum[:8])
	return c
}

func toolsDigestFromNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:8])
}

func digestText(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func newSnapshotID(now time.Time) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func sanitizePathPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, string(filepath.Separator), "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}

func cloneRepo(r *RepoIdentity) *RepoIdentity {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func cloneGates(in []GateSpecSnapshot) []GateSpecSnapshot {
	if len(in) == 0 {
		return nil
	}
	out := make([]GateSpecSnapshot, len(in))
	copy(out, in)
	for i := range out {
		out[i].Name = redact.String(out[i].Name)
		out[i].Value = redact.String(out[i].Value)
	}
	return out
}

func mergeSettings(base, overlay SettingsDigest) SettingsDigest {
	out := base
	if overlay.Provider != "" {
		out.Provider = overlay.Provider
	}
	if overlay.Model != "" {
		out.Model = overlay.Model
	}
	if overlay.Agent != "" {
		out.Agent = overlay.Agent
	}
	if overlay.Effort != "" {
		out.Effort = overlay.Effort
	}
	if overlay.Autonomy != "" {
		out.Autonomy = overlay.Autonomy
	}
	if overlay.PermissionMode != "" {
		out.PermissionMode = overlay.PermissionMode
	}
	if overlay.Fast != nil {
		out.Fast = overlay.Fast
	}
	if overlay.ToolsDigest != "" {
		out.ToolsDigest = overlay.ToolsDigest
	}
	if overlay.PromptDigest != "" {
		out.PromptDigest = overlay.PromptDigest
	}
	if overlay.SystemChars > 0 {
		out.SystemChars = overlay.SystemChars
	}
	return out
}

func lookupDeleg(m map[string]string, childID string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[childID])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
