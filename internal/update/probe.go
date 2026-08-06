package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultProbeInterval is how long to wait between GitHub release probes.
	DefaultProbeInterval = 24 * time.Hour
	// DefaultProbeTimeout bounds a single startup network check.
	DefaultProbeTimeout = 4 * time.Second

	// Install kinds for DetectInstall.
	InstallWritable    = "writable"
	InstallNix         = "nix"
	InstallNotWritable = "not-writable"
	InstallUnsupported = "unsupported"
)

// InstallInfo describes whether the running binary can be self-replaced.
type InstallInfo struct {
	Executable string
	Kind       string
	CanReplace bool
	// Hint is a short user-facing note when CanReplace is false.
	Hint string
}

// DetectInstall classifies the running (or override) binary for self-update.
func DetectInstall(executable string) InstallInfo {
	if runtime.GOOS == "windows" {
		return InstallInfo{
			Executable: executable,
			Kind:       InstallUnsupported,
			Hint:       "Windows self-update is unsupported; re-download from GitHub Releases",
		}
	}
	exe, err := resolveExecutable(executable)
	if err != nil {
		return InstallInfo{
			Executable: executable,
			Kind:       InstallNotWritable,
			Hint:       "cannot resolve binary path for self-update",
		}
	}
	info := InstallInfo{Executable: exe}
	if isNixStorePath(exe) {
		info.Kind = InstallNix
		info.Hint = "Nix install — update the flake/lock input (not strike upgrade)"
		return info
	}
	if err := checkWritable(exe); err != nil {
		info.Kind = InstallNotWritable
		info.Hint = "binary not writable — re-run the install script or use your package manager"
		return info
	}
	info.Kind = InstallWritable
	info.CanReplace = true
	return info
}

func isNixStorePath(path string) bool {
	// Nix store paths look like /nix/store/<hash>-name[/…].
	cleaned := filepath.Clean(path)
	return strings.HasPrefix(cleaned, "/nix/store/") ||
		strings.Contains(cleaned, "/nix/store/")
}

func checkWritable(exe string) error {
	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("path is a directory")
	}
	// Probe create+remove of a sibling temp file (same as replaceBinary needs).
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".strike-update-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	// Also require the binary itself is writable (rename target).
	w, err := os.OpenFile(exe, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_ = w.Close()
	return nil
}

// ShouldProbe reports whether mode should hit the network for a release check.
// mode is off|notify|auto (empty treated as notify by callers via EffectiveAutoupdate).
func ShouldProbe(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "notify", "auto":
		return true
	default:
		return false
	}
}

// ShouldAutoInstall reports whether mode may download+replace without a prompt.
func ShouldAutoInstall(mode string) bool {
	return strings.ToLower(strings.TrimSpace(mode)) == "auto"
}

// ProbeResult is the outcome of a startup/periodic update probe.
type ProbeResult struct {
	Skipped    bool
	SkipReason string
	Current    string
	Latest     string
	Available  bool
	// Message is user-facing status chrome text (empty when silent).
	Message string
	// CanReplace is true when binary self-update is possible.
	CanReplace bool
	// AutoInstalled is true when mode=auto replaced the binary (NoExec).
	AutoInstalled bool
}

// probeCache is persisted under ~/.strike/cache/update-check.json.
type probeCache struct {
	CheckedAt          time.Time `json:"checkedAt"`
	Current            string    `json:"current,omitempty"`
	Latest             string    `json:"latest,omitempty"`
	Available          bool      `json:"available,omitempty"`
	LastNotifiedLatest string    `json:"lastNotifiedLatest,omitempty"`
}

// CachePath returns ~/.strike/cache/update-check.json (HOME-aware).
func CachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".strike", "cache", "update-check.json")
}

// ProbeOptions configures StartupProbe. Zero values use production defaults.
type ProbeOptions struct {
	Options
	// Mode is off|notify|auto.
	Mode string
	// Interval between network probes (default 24h).
	Interval time.Duration
	// CacheFile overrides CachePath (tests).
	CacheFile string
	// Now overrides time.Now (tests).
	Now func() time.Time
	// Install overrides DetectInstall (tests).
	Install *InstallInfo
	// SkipNetwork forces a cache-only / skip path (tests).
	SkipNetwork bool
}

// StartupProbe checks for a newer release without blocking the TUI unreasonably.
// Failures (offline, rate limit, parse) skip silently. When Available, Message
// always points at /upgrade or a non-replaceable install hint. mode=auto may
// replace the binary in place (NoExec) when CanReplace; never re-execs here.
func StartupProbe(ctx context.Context, popts ProbeOptions) (ProbeResult, error) {
	mode := strings.ToLower(strings.TrimSpace(popts.Mode))
	if mode == "" {
		mode = "notify"
	}
	if !ShouldProbe(mode) {
		return ProbeResult{Skipped: true, SkipReason: "autoupdate off"}, nil
	}

	opts := popts.Options.withDefaults()
	now := time.Now
	if popts.Now != nil {
		now = popts.Now
	}
	interval := popts.Interval
	if interval <= 0 {
		interval = DefaultProbeInterval
	}
	cacheFile := popts.CacheFile
	if cacheFile == "" {
		cacheFile = CachePath()
	}

	install := DetectInstall(opts.Executable)
	if popts.Install != nil {
		install = *popts.Install
	}

	cache, _ := loadProbeCache(cacheFile)
	// Re-notify only when latest tag changes; still allow first notify.
	if !cache.CheckedAt.IsZero() && now().Sub(cache.CheckedAt) < interval {
		if cache.Available && IsNewer(cache.Latest, opts.Current) {
			if cache.LastNotifiedLatest == cache.Latest {
				return ProbeResult{
					Skipped:    true,
					SkipReason: "already notified",
					Current:    opts.Current,
					Latest:     cache.Latest,
					Available:  true,
					CanReplace: install.CanReplace,
				}, nil
			}
			// Within interval but not yet notified for this tag (e.g. cache
			// written by a prior process that skipped UI) — surface message.
			return finishProbe(ctx, opts, mode, install, cache.Latest, cacheFile, now, true)
		}
		return ProbeResult{
			Skipped:    true,
			SkipReason: "probe interval",
			Current:    opts.Current,
			Latest:     cache.Latest,
			CanReplace: install.CanReplace,
		}, nil
	}

	if popts.SkipNetwork {
		return ProbeResult{Skipped: true, SkipReason: "network skipped"}, nil
	}

	rel, err := LatestRelease(ctx, opts)
	if err != nil {
		// Offline / rate-limit / API errors: silent skip.
		return ProbeResult{Skipped: true, SkipReason: "check failed: " + err.Error()}, nil
	}

	// Record check time even when up to date.
	_ = saveProbeCache(cacheFile, probeCache{
		CheckedAt:          now(),
		Current:            opts.Current,
		Latest:             rel.TagName,
		Available:          IsNewer(rel.TagName, opts.Current),
		LastNotifiedLatest: cache.LastNotifiedLatest,
	})

	if !IsNewer(rel.TagName, opts.Current) {
		return ProbeResult{
			Current:    opts.Current,
			Latest:     rel.TagName,
			CanReplace: install.CanReplace,
			Message:    "",
		}, nil
	}

	return finishProbe(ctx, opts, mode, install, rel.TagName, cacheFile, now, false)
}

func finishProbe(ctx context.Context, opts Options, mode string, install InstallInfo, latest, cacheFile string, now func() time.Time, fromCache bool) (ProbeResult, error) {
	res := ProbeResult{
		Current:    opts.Current,
		Latest:     latest,
		Available:  true,
		CanReplace: install.CanReplace,
	}

	if ShouldAutoInstall(mode) && install.CanReplace {
		upOpts := opts
		upOpts.NoExec = true
		upOpts.Stdout = opts.Stdout
		upRes, err := Upgrade(ctx, upOpts)
		if err != nil {
			// Fall back to notify messaging; do not fail startup.
			res.Message = notifyMessage(opts.Current, latest, install)
			_ = markNotified(cacheFile, opts.Current, latest, now)
			return res, nil
		}
		if upRes.Updated {
			res.AutoInstalled = true
			res.Message = fmt.Sprintf("auto-updated to %s — restart strike to use it", latest)
			_ = markNotified(cacheFile, opts.Current, latest, now)
			return res, nil
		}
	}

	res.Message = notifyMessage(opts.Current, latest, install)
	if !fromCache || res.Message != "" {
		_ = markNotified(cacheFile, opts.Current, latest, now)
	}
	return res, nil
}

func notifyMessage(current, latest string, install InstallInfo) string {
	cur := versionLabel(current)
	if !install.CanReplace {
		if install.Kind == InstallNix {
			return fmt.Sprintf("update available: %s → %s (Nix: update lock/input; not /upgrade)", cur, latest)
		}
		return fmt.Sprintf("update available: %s → %s (%s)", cur, latest, install.Hint)
	}
	return fmt.Sprintf("update available: %s → %s — /upgrade or strike upgrade", cur, latest)
}

func markNotified(cacheFile, current, latest string, now func() time.Time) error {
	prev, _ := loadProbeCache(cacheFile)
	prev.CheckedAt = now()
	prev.Current = current
	prev.Latest = latest
	prev.Available = true
	prev.LastNotifiedLatest = latest
	return saveProbeCache(cacheFile, prev)
}

func loadProbeCache(path string) (probeCache, error) {
	if path == "" {
		return probeCache{}, errors.New("empty cache path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return probeCache{}, err
	}
	var c probeCache
	if err := json.Unmarshal(data, &c); err != nil {
		return probeCache{}, err
	}
	return c, nil
}

func saveProbeCache(path string, c probeCache) error {
	if path == "" {
		return errors.New("empty cache path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
