package server

import (
	"net/http"
)

// onboardingResponse is GET /v1/onboarding (#1552). It reports only the
// daemon-side acknowledgement bit from ~/.strike/onboarding.json — the
// same record the TUI wizard writes — so a new browser against an
// already-onboarded daemon does not show the first-run card again. The
// record is daemon-scoped ("onboarding was acknowledged for this
// daemon"), not a per-user identity, and carries no other data.
type onboardingResponse struct {
	Acknowledged bool `json:"acknowledged"`
}

func (s *Server) onboardingService(w http.ResponseWriter) bool {
	if s.opts.Services == nil || s.opts.Services.Onboarding == nil {
		capabilityUnavailable(w, "onboarding")
		return false
	}
	return true
}

func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingService(w) {
		return
	}
	// Acknowledged is a pure read: unlike the TUI's ShouldAutoOpen it never
	// persists the established-install migration on a page load.
	writeJSON(w, http.StatusOK, onboardingResponse{Acknowledged: s.opts.Services.Onboarding.Acknowledged()})
}

func (s *Server) handleOnboardingAcknowledge(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingService(w) {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	if err := s.opts.Services.Onboarding.Acknowledge(); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, onboardingResponse{Acknowledged: true})
}
