package tui

import (
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// providerHealthTone returns a badge tone for the selected provider's
// credential health, or false when no indicator should be shown.
func providerHealthTone(m Model) (ui.Tone, bool) {
	if m.providerName == "" || m.services.Auth == nil {
		return 0, false
	}
	var status host.ProviderStatus
	found := false
	for _, s := range m.services.Auth.Statuses() {
		if s.Name == m.providerName {
			status = s
			found = true
			break
		}
	}
	if !found {
		return 0, false
	}
	if !status.Authed && !status.Builtin {
		return ui.ToneError, true
	}
	if m.sessionErrored {
		return ui.ToneError, true
	}
	if !status.ExpiresAt.IsZero() && time.Until(status.ExpiresAt) < authExpiryWarn {
		return ui.ToneWarning, true
	}
	if status.Authed {
		return ui.ToneSuccess, true
	}
	return 0, false
}
