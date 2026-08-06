package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

var globalMu sync.Mutex

// SetGlobalDefaults persists non-empty fields into ~/.strike/config,
// creating it if needed. Fields passed as "" are left unchanged, and
// unrelated config (permissions, systemPrompt) is preserved.
// mode is a permissionMode string (default|plan|soft-approve|accept-edits|yolo);
// empty leaves the stored default unchanged.
func SetGlobalDefaults(provider, model, agent string, effort protocol.Effort, mode string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if provider != "" {
		cfg.Provider = CanonicalProviderID(provider)
	}
	if model != "" {
		cfg.Model = model
	}
	if agent != "" {
		cfg.DefaultAgent = agent
	}
	if effort != "" {
		parsed, ok := protocol.ParseEffort(string(effort))
		if !ok {
			unlock()
			return fmt.Errorf("unknown effort %q", effort)
		}
		cfg.Effort = parsed
	}
	if mode != "" {
		parsed, ok := protocol.ParsePermissionMode(mode)
		if !ok {
			unlock()
			return fmt.Errorf("unknown permission mode %q", mode)
		}
		cfg.PermissionMode = parsed
	}
	return writeGlobal(cfg, unlock)
}

// SetGlobalTheme persists the preferred TUI theme id into ~/.strike/config.
func SetGlobalTheme(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("theme id is required")
	}
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	cfg.Theme = id
	return writeGlobal(cfg, unlock)
}

// SetGlobalKeybinds persists overrides into ~/.strike/keybinds.jsonc, merging
// with any existing binds in that file. Pass nil to delete the file entirely
// (reset to defaults). Unknown ids from the new binds are silently dropped;
// unknown ids already in the file survive round-trip.
func SetGlobalKeybinds(binds map[string]KeybindChords) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	path := keybindsWritePath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}

	if len(binds) == 0 {
		return deleteKeybindsFile(path)
	}

	existing, err := readKeybindsRelaxed(path)
	if err != nil {
		return err
	}
	if existing == nil {
		existing = make(map[string]KeybindChords)
	}
	// Overwrite only known ids; unknown ids in the file survive.
	for id, chords := range binds {
		if _, ok := KnownKeybindIDs[id]; ok {
			existing[id] = append(KeybindChords(nil), chords...)
		}
	}
	return writeKeybindsFile(path, existing)
}

func keybindsWritePath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "keybinds.jsonc")
}

// readKeybindsRelaxed reads the file and returns all keybind entries, including
// any with unknown ids (they survive round-trip). Missing file → nil, nil.
func readKeybindsRelaxed(path string) (map[string]KeybindChords, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read keybinds file: %w", err)
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return nil, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 || stripped[0] != '{' {
		return nil, fmt.Errorf("keybinds file must be a JSON object")
	}
	// Accept wrapped {"keybinds":{...}} or flat.
	var wrapped struct {
		Keybinds map[string]KeybindChords `json:"keybinds"`
	}
	if err := json.Unmarshal(stripped, &wrapped); err != nil {
		return nil, err
	}
	if wrapped.Keybinds != nil {
		return wrapped.Keybinds, nil
	}
	var flat map[string]KeybindChords
	if err := json.Unmarshal(stripped, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}

func deleteKeybindsFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func writeKeybindsFile(path string, binds map[string]KeybindChords) error {
	out, err := json.MarshalIndent(binds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal keybinds: %w", err)
	}
	payload := append(out, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keybinds-")
	if err != nil {
		return fmt.Errorf("create temp keybinds: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp keybinds: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp keybinds: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp keybinds: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp keybinds: %w", err)
	}
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}
	return nil
}

// SetGlobalPresentation persists non-empty editor/reader presentation modes
// into ~/.strike/config. Empty fields are left unchanged. Values match config
// keys vimMode/nanoMode/mdReadMode (pane|embedded|overlay|modal|takeover).
func SetGlobalPresentation(vimMode, nanoMode, mdReadMode string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if vimMode != "" {
		if !validEditorMode(vimMode) {
			unlock()
			return fmt.Errorf("unknown vimMode %q (want pane|embedded|overlay|modal|takeover)", vimMode)
		}
		cfg.VimMode = normalizeEditorMode(vimMode)
	}
	if nanoMode != "" {
		if !validEditorMode(nanoMode) {
			unlock()
			return fmt.Errorf("unknown nanoMode %q (want pane|embedded|overlay|modal|takeover)", nanoMode)
		}
		cfg.NanoMode = normalizeEditorMode(nanoMode)
	}
	if mdReadMode != "" {
		if !validMdReadMode(mdReadMode) {
			unlock()
			return fmt.Errorf("unknown mdReadMode %q (want embedded|pane|modal|overlay)", mdReadMode)
		}
		cfg.MdReadMode = normalizeMdReadMode(mdReadMode)
	}
	return writeGlobal(cfg, unlock)
}

// SetGlobalConfigDials persists non-empty peer-ported behavior dials into
// ~/.strike/config. Empty fields are left unchanged. Values:
//
//	sandbox          — off|read-only|workspace-write (OS isolation; distinct from permissionMode)
//	notify           — on|off|unfocused-only
//	leanCode         — off|lite|full
//	deferTools       — on|off
//	sessionWorktree  — off|auto|always (session.worktree)
func SetGlobalConfigDials(sandboxMode, notify, leanCode, deferTools, sessionWorktree string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if sandboxMode != "" {
		mode, ok := sandbox.ParseMode(sandboxMode)
		if !ok {
			unlock()
			return fmt.Errorf("unknown sandbox %q (want %s)", sandboxMode, sandbox.ModeNames())
		}
		// Store canonical token even when input was empty-alias; callers pass
		// non-empty only, so DefaultMode from "" is not used here.
		cfg.Sandbox = mode.String()
	}
	if notify != "" {
		n := NormalizeNotify(notify)
		if n == "" {
			unlock()
			return fmt.Errorf("unknown notify %q (want on|off|unfocused-only)", notify)
		}
		cfg.Notify = n
	}
	if leanCode != "" {
		lc := NormalizeLeanCode(leanCode)
		if lc == "" {
			unlock()
			return fmt.Errorf("unknown leanCode %q (want off|lite|full)", leanCode)
		}
		cfg.LeanCode = lc
	}
	if deferTools != "" {
		dt := NormalizeDeferTools(deferTools)
		if dt == "" {
			unlock()
			return fmt.Errorf("unknown deferTools %q (want on|off)", deferTools)
		}
		cfg.DeferTools = dt
	}
	if sessionWorktree != "" {
		wt, ok := parseSessionWorktree(sessionWorktree)
		if !ok {
			unlock()
			return fmt.Errorf("unknown session.worktree %q (want off|auto|always)", sessionWorktree)
		}
		cfg.Session.Worktree = wt
	}
	return writeGlobal(cfg, unlock)
}

// SetGlobalAutoApproveDials persists permission auto-approve countdown/exclude
// and maxChildDepth into ~/.strike/config. Empty scalar strings leave the
// corresponding field unchanged. exclude nil leaves the list unchanged; a
// non-nil pointer (including to an empty slice) replaces the stored list.
//
//	seconds        — "off"|"0" disables; "1"–"60" (clamped); aliases off/false/no/disabled
//	maxChildDepth  — "0"|"default" → engine default; "1"–"8" (clamped to MaxChildDepthCeiling)
func SetGlobalAutoApproveDials(seconds string, exclude *[]string, maxChildDepth string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if seconds != "" {
		n, ok := parseAutoApproveSeconds(seconds)
		if !ok {
			unlock()
			return fmt.Errorf("unknown permissionAutoApproveSeconds %q (want off|0|1-60)", seconds)
		}
		cfg.PermissionAutoApproveSeconds = n
	}
	if exclude != nil {
		cfg.PermissionAutoApproveExclude = normalizePermissionAutoApproveExclude(*exclude)
	}
	if maxChildDepth != "" {
		n, ok := parseMaxChildDepth(maxChildDepth)
		if !ok {
			unlock()
			return fmt.Errorf("unknown maxChildDepth %q (want default|0|1-%d)", maxChildDepth, MaxChildDepthCeiling)
		}
		cfg.MaxChildDepth = n
	}
	return writeGlobal(cfg, unlock)
}

func parseAutoApproveSeconds(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "off", "0", "false", "no", "disabled", "none":
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return 0, false
	}
	return ClampPermissionAutoApproveSeconds(n), true
}

func parseMaxChildDepth(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "default", "0", "off", "unset":
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return 0, false
	}
	return ClampMaxChildDepth(n), true
}

// parseSessionWorktree accepts only canonical off|auto|always (strict; no
// silent fallback to off for unknown tokens).
func parseSessionWorktree(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "0", "no", "never", "none":
		return "off", true
	case "auto":
		return "auto", true
	case "always", "on", "true", "1", "yes":
		return "always", true
	default:
		return "", false
	}
}

// CompactionDials is a partial update for history compaction / prune knobs
// written to ~/.strike/config. Empty strings leave the stored value unchanged.
// See host.CompactionDials for the shared vocabulary (duplicated here so
// config stays free of a host import).
type CompactionDials struct {
	Strategy           string
	Model              string
	Threshold          string
	Buffer             string
	KeepUserTurns      string
	PruneProtectTokens string
	PruneMinimumTokens string
	PruneKeepUserTurns string
	PruneProtectTools  string
}

// SetGlobalCompactionDials persists non-empty compaction/prune dials into
// ~/.strike/config. Empty fields are left unchanged. Rejects unknown strategy
// tokens and unparseable numbers without writing.
//
//	Strategy           — trim|summarize
//	Model              — model id; "-" clears (session model)
//	Threshold          — float string; "default"/"0" → engine default (0);
//	                     values >=1 disable threshold compaction
//	Buffer / Keep* / Prune*Tokens — non-negative int strings; "default"/"0" → 0
//	PruneProtectTools  — comma-separated names; "-" clears extras
func SetGlobalCompactionDials(d CompactionDials) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}

	if d.Strategy != "" {
		s := NormalizeCompactionStrategy(d.Strategy)
		if s == "" {
			unlock()
			return fmt.Errorf("unknown compactionStrategy %q (want trim|summarize)", d.Strategy)
		}
		cfg.CompactionStrategy = s
	}
	if d.Model != "" {
		if isClearToken(d.Model) {
			cfg.CompactionModel = ""
		} else {
			cfg.CompactionModel = strings.TrimSpace(d.Model)
		}
	}
	if d.Threshold != "" {
		v, err := parseCompactionFloat(d.Threshold, "compactionThreshold")
		if err != nil {
			unlock()
			return err
		}
		cfg.CompactionThreshold = ClampCompactionThreshold(v)
	}
	if d.Buffer != "" {
		n, err := parseCompactionInt(d.Buffer, "compactionBuffer")
		if err != nil {
			unlock()
			return err
		}
		cfg.CompactionBuffer = ClampCompactionBuffer(n)
	}
	if d.KeepUserTurns != "" {
		n, err := parseCompactionInt(d.KeepUserTurns, "keepUserTurns")
		if err != nil {
			unlock()
			return err
		}
		cfg.KeepUserTurns = ClampKeepUserTurns(n)
	}
	if d.PruneProtectTokens != "" {
		n, err := parseCompactionInt(d.PruneProtectTokens, "pruneProtectTokens")
		if err != nil {
			unlock()
			return err
		}
		cfg.PruneProtectTokens = ClampPruneProtectTokens(n)
	}
	if d.PruneMinimumTokens != "" {
		n, err := parseCompactionInt(d.PruneMinimumTokens, "pruneMinimumTokens")
		if err != nil {
			unlock()
			return err
		}
		cfg.PruneMinimumTokens = ClampPruneMinimumTokens(n)
	}
	if d.PruneKeepUserTurns != "" {
		n, err := parseCompactionInt(d.PruneKeepUserTurns, "pruneKeepUserTurns")
		if err != nil {
			unlock()
			return err
		}
		cfg.PruneKeepUserTurns = ClampPruneKeepUserTurns(n)
	}
	if d.PruneProtectTools != "" {
		if isClearToken(d.PruneProtectTools) {
			cfg.PruneProtectTools = nil
		} else {
			parts := strings.Split(d.PruneProtectTools, ",")
			cfg.PruneProtectTools = NormalizePruneProtectTools(parts)
		}
	}
	return writeGlobal(cfg, unlock)
}

func isClearToken(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "-", "clear", "none", "default", "session", "unset":
		return true
	default:
		return false
	}
}

func parseCompactionFloat(raw, field string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "default" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	if v < 0 {
		return 0, fmt.Errorf("invalid %s %q (want >= 0)", field, raw)
	}
	return v, nil
}

func parseCompactionInt(raw, field string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "default" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", field, raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid %s %q (want >= 0)", field, raw)
	}
	return n, nil
}

// SetGlobalSchedulerPresets validates and persists the global scheduler
// presets list into ~/.strike/config. Custom scheduler limits and command
// rules are preserved. Unknown or duplicate ids are rejected without writing.
// An empty slice clears global presets only. IDs are stored in catalog order
// among the selection for stable re-writes.
func SetGlobalSchedulerPresets(ids []string) error {
	normalized, err := normalizeSchedulerPresetIDs(ids)
	if err != nil {
		return err
	}

	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	cfg.Scheduler.Presets = normalized
	return writeGlobal(cfg, unlock)
}

// normalizeSchedulerPresetIDs trims, validates, and reorders ids into shipped
// catalog order. Empty input yields nil (clears presets).
func normalizeSchedulerPresetIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	trimmed := make([]string, len(ids))
	for i, id := range ids {
		trimmed[i] = strings.TrimSpace(id)
	}
	src := "scheduler.presets"
	if path := GlobalPath(); path != "" {
		src = path + ": scheduler.presets"
	}
	if err := scheduler.ValidatePresetIDs(trimmed, src); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(trimmed))
	for _, id := range trimmed {
		seen[id] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for _, p := range scheduler.Catalog() {
		if _, ok := seen[p.ID]; ok {
			out = append(out, p.ID)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ReadGlobalDefaults returns the global config file contents used as user
// defaults. Missing file yields a zero Config and nil error. Accepts JSONC
// (comments) and ignores unknown keys including "$schema".
func ReadGlobalDefaults() (Config, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	path := GlobalPath()
	if path == "" {
		return Config{}, fmt.Errorf("cannot locate home directory")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Config{}, nil
	case err != nil:
		return Config{}, err
	case len(data) == 0:
		return Config{}, nil
	default:
		cfg, err := unmarshalConfigJSONC(data)
		if err != nil {
			return Config{}, fmt.Errorf("%s is not valid JSON/JSONC: %w", path, err)
		}
		return cfg, nil
	}
}

// unmarshalConfigJSONC strips JSONC comments then decodes into Config.
// Unknown keys (including "$schema") are ignored.
func unmarshalConfigJSONC(data []byte) (Config, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(stripped, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validEditorMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pane", "embedded", "overlay", "modal", "takeover":
		return true
	default:
		return false
	}
}

// normalizeEditorMode stores canonical pane|overlay|takeover (aliases collapse).
func normalizeEditorMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "embedded", "pane":
		return "pane"
	case "modal", "overlay":
		return "overlay"
	case "takeover":
		return "takeover"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func validMdReadMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "embedded", "pane", "modal", "overlay":
		return true
	default:
		return false
	}
}

func normalizeMdReadMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pane", "embedded":
		return "embedded"
	case "overlay", "modal":
		return "modal"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func readGlobalForWrite() (Config, func() error, error) {
	path := GlobalPath()
	if path == "" {
		return Config{}, nil, fmt.Errorf("cannot locate home directory")
	}
	unlock, err := lockGlobalFile(path)
	if err != nil {
		return Config{}, nil, err
	}
	var readErr error
	defer func() {
		if readErr != nil {
			unlock()
		}
	}()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Config{}, unlock, nil
	case err != nil:
		readErr = err
		return Config{}, nil, err
	case len(data) == 0:
		// Lock created the file; treat empty as not existing.
		return Config{}, unlock, nil
	default:
		// JSONC load; writeGlobal rewrites pure JSON (comments / $schema dropped).
		cfg, err := unmarshalConfigJSONC(data)
		if err != nil {
			readErr = fmt.Errorf("existing %s is not valid JSON/JSONC (%v) — fix it before saving defaults", path, err)
			return Config{}, nil, readErr
		}
		return cfg, unlock, nil
	}
}

func writeGlobal(cfg Config, unlock func() error) error {
	defer unlock()
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}
	// If config itself is a file symlink (stow/dotfiles), write the referent
	// so atomic rename does not replace the symlink node with a plain file.
	path, err := resolveWritePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload := append(out, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp config: %w", err)
	}
	// Fsync the directory so the rename is durable.
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}
	return nil
}

// resolveWritePath returns path suitable for atomic rename. File symlinks are
// resolved to their referent; missing paths and regular files are unchanged.
func resolveWritePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve config symlink: %w", err)
	}
	return real, nil
}

// ProjectPath is the project config file, <workDir>/.strike/config (JSON or JSONC).
func ProjectPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(projectRoot(workDir), "config")
}

// AppendProjectPermission appends an allow rule to the project config at
// <workDir>/.strike/config, creating the file (and .strike/) if needed.
// Unrelated fields are preserved. Empty permission names are rejected.
func AppendProjectPermission(workDir string, rule permission.Rule) error {
	if workDir == "" {
		return fmt.Errorf("empty work directory")
	}
	if rule.Permission == "" {
		return fmt.Errorf("empty permission name")
	}
	if rule.Action == "" {
		rule.Action = permission.Allow
	}
	if rule.Pattern == "" {
		rule.Pattern = "*"
	}
	path := ProjectPath(workDir)
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// fresh project config
	case err != nil:
		return err
	default:
		// JSONC load; WriteFile below rewrites pure JSON (comments / $schema dropped).
		parsed, err := unmarshalConfigJSONC(data)
		if err != nil {
			return fmt.Errorf("existing %s is not valid JSON/JSONC (%v) — fix it before saving permissions", path, err)
		}
		cfg = parsed
	}
	cfg.Permissions = append(cfg.Permissions, rule)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
