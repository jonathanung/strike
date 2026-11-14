package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/update"
)

// UpdateProbeResult is one release-check outcome plus the cache's check time.
type UpdateProbeResult struct {
	update.ProbeResult
	// CheckedAt is when the release metadata was last fetched (zero = never).
	CheckedAt time.Time
}

// UpdateProbe runs a release check. It is only called when the autoupdate
// dial is not off. Implementations must never install (#1583).
type UpdateProbe func(ctx context.Context) (UpdateProbeResult, error)

// WebUpdateCacheFile is the daemon's probe cache, a sibling of the TUI cache
// so a web check never consumes the TUI's once-per-release notice.
func WebUpdateCacheFile() string {
	p := update.CachePath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "update-check-web.json")
}

// NewUpdateProbe wraps update.StartupProbe (the TUI's startup check) for the
// daemon. Mode is always forced to notify: the web surface reports and never
// self-replaces the binary, even when the dial is auto. base.CacheFile ""
// uses WebUpdateCacheFile.
func NewUpdateProbe(base update.ProbeOptions) UpdateProbe {
	return func(ctx context.Context) (UpdateProbeResult, error) {
		opts := base
		opts.Mode = config.AutoupdateNotify
		if opts.CacheFile == "" {
			opts.CacheFile = WebUpdateCacheFile()
		}
		ctx, cancel := context.WithTimeout(ctx, update.DefaultProbeTimeout)
		defer cancel()
		res, err := update.StartupProbe(ctx, opts)
		if err != nil {
			return UpdateProbeResult{}, err
		}
		return UpdateProbeResult{ProbeResult: res, CheckedAt: update.ProbeCheckedAt(opts.CacheFile)}, nil
	}
}

// Update-check states on the wire.
const (
	updateStateOff       = "off"
	updateStateAvailable = "available"
	updateStateCurrent   = "current"
	updateStateFailed    = "failed"
	updateStateUnknown   = "unknown"
)

// updateCheckResponse is GET /v1/update and settings.updateCheck.
type updateCheckResponse struct {
	// Mode is the effective autoupdate dial (off|notify|auto).
	Mode string `json:"mode"`
	// State is off|available|current|failed|unknown. "current" is only
	// reported after a successful check; failures never read as up to date.
	State     string `json:"state"`
	Current   string `json:"current,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
	// CanReplace reports whether the host binary could self-update (host-side).
	CanReplace bool `json:"canReplace"`
}

const updateCheckFailedPrefix = "check failed: "

func updateCheckFromProbe(mode string, res UpdateProbeResult, err error) updateCheckResponse {
	out := updateCheckResponse{Mode: mode, Current: res.Current, Latest: res.Latest, CanReplace: res.CanReplace}
	if !res.CheckedAt.IsZero() {
		out.CheckedAt = res.CheckedAt.UTC().Format(time.RFC3339)
	}
	switch {
	case err != nil:
		out.State, out.Error = updateStateFailed, err.Error()
	case strings.HasPrefix(res.SkipReason, updateCheckFailedPrefix):
		out.State, out.Error = updateStateFailed, strings.TrimPrefix(res.SkipReason, updateCheckFailedPrefix)
	case res.Available:
		out.State, out.Available, out.Message = updateStateAvailable, true, res.Message
		if out.Message == "" {
			out.Message = fmt.Sprintf("update available: %s → %s", res.Current, res.Latest)
		}
	case res.Latest != "" && (!res.Skipped || res.SkipReason == "probe interval"):
		out.State = updateStateCurrent
	default:
		out.State = updateStateUnknown
	}
	return out
}

func (s *Server) updateCheckEnabled() bool {
	return s.opts.UpdateProbe != nil && s.opts.Services != nil && s.opts.Services.Settings != nil
}

func (s *Server) updateMode() string {
	return config.EffectiveAutoupdate(s.opts.Services.Settings.Defaults().Autoupdate)
}

// lastUpdateCheck returns the most recent GET /v1/update result (nil = none).
func (s *Server) lastUpdateCheck() *updateCheckResponse {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if s.updateLast == nil {
		return nil
	}
	v := *s.updateLast
	return &v
}

// handleUpdateCheck runs the release probe for the current dial. Off never
// touches the network. Probes are serialised; the probe's own cache interval
// bounds how often GitHub is actually contacted.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.updateCheckEnabled() {
		capabilityUnavailable(w, "update check")
		return
	}
	mode := s.updateMode()
	if !update.ShouldProbe(mode) {
		out := updateCheckResponse{Mode: mode, State: updateStateOff}
		s.updateMu.Lock()
		s.updateLast = &out
		s.updateMu.Unlock()
		writeJSON(w, http.StatusOK, out)
		return
	}
	s.updateMu.Lock()
	res, err := s.opts.UpdateProbe(r.Context())
	out := updateCheckFromProbe(mode, res, err)
	s.updateLast = &out
	s.updateMu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
