package tui

import (
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestProviderHealthTone(t *testing.T) {
	far := time.Now().Add(48 * time.Hour)
	soon := time.Now().Add(time.Hour)

	tests := []struct {
		name     string
		setup    func(*Model)
		wantTone ui.Tone
		wantOK   bool
	}{
		{
			name: "no provider",
			setup: func(m *Model) {
				m.providerName = ""
			},
			wantOK: false,
		},
		{
			name: "unauthed selected",
			setup: func(m *Model) {
				m.providerName = "openai"
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "openai", Detail: "none", OAuth: true, APIKey: true},
				}}
			},
			wantTone: ui.ToneError,
			wantOK:   true,
		},
		{
			name: "authed far expiry",
			setup: func(m *Model) {
				m.providerName = "openai"
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "openai", Detail: "oauth", Authed: true, OAuth: true, ExpiresAt: far},
				}}
			},
			wantTone: ui.ToneSuccess,
			wantOK:   true,
		},
		{
			name: "expiry in 1h",
			setup: func(m *Model) {
				m.providerName = "openai"
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "openai", Detail: "oauth", Authed: true, OAuth: true, ExpiresAt: soon},
				}}
			},
			wantTone: ui.ToneWarning,
			wantOK:   true,
		},
		{
			name: "sessionErrored overrides authed",
			setup: func(m *Model) {
				m.providerName = "openai"
				m.sessionErrored = true
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "openai", Detail: "oauth", Authed: true, OAuth: true, ExpiresAt: far},
				}}
			},
			wantTone: ui.ToneError,
			wantOK:   true,
		},
		{
			name: "echo builtin authed",
			setup: func(m *Model) {
				m.providerName = "echo"
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
				}}
			},
			wantTone: ui.ToneSuccess,
			wantOK:   true,
		},
		{
			name: "nil auth",
			setup: func(m *Model) {
				m.providerName = "openai"
				m.services.Auth = nil
			},
			wantOK: false,
		},
		{
			name: "provider absent from statuses",
			setup: func(m *Model) {
				m.providerName = "missing"
				m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
					{Name: "openai", Authed: true},
				}}
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			tt.setup(&m)
			got, ok := providerHealthTone(m)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (tone=%v)", ok, tt.wantOK, got)
			}
			if ok && got != tt.wantTone {
				t.Errorf("tone = %v, want %v", got, tt.wantTone)
			}
		})
	}
}
