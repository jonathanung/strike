package auth

import (
	"context"
	"testing"
	"time"
)

func TestBearerSourcePrecedence(t *testing.T) {
	st := newMemStore()
	ctx := context.Background()

	// No credentials at all: actionable error.
	t.Setenv("XAI_API_KEY", "")
	if _, err := BearerSource("xai", st)(ctx); err == nil {
		t.Error("expected error with no credentials")
	}

	// Stored OAuth token, not near expiry: used as-is.
	st.Set("xai", Credential{Type: TypeOAuth, Access: "oauth-token", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour)})
	if got, err := BearerSource("xai", st)(ctx); err != nil || got != "oauth-token" {
		t.Errorf("bearer = %q, err = %v; want oauth-token", got, err)
	}

	// Stored API key wins over OAuth tokens.
	st.Set("xai", Credential{Type: TypeOAuth, Access: "oauth-token", APIKey: "stored-key", ExpiresAt: time.Now().Add(time.Hour)})
	if got, _ := BearerSource("xai", st)(ctx); got != "stored-key" {
		t.Errorf("bearer = %q, want stored-key", got)
	}

	// Env var wins over everything.
	t.Setenv("XAI_API_KEY", "env-key")
	if got, _ := BearerSource("xai", st)(ctx); got != "env-key" {
		t.Errorf("bearer = %q, want env-key", got)
	}
}

func TestAPIKey(t *testing.T) {
	st := newMemStore()
	t.Setenv("OPENAI_API_KEY", "")
	if _, ok := APIKey("openai", st); ok {
		t.Fatal("expected no key")
	}
	if err := st.Set("openai", Credential{Type: TypeAPIKey, APIKey: "stored"}); err != nil {
		t.Fatal(err)
	}
	if key, ok := APIKey("openai", st); !ok || key != "stored" {
		t.Errorf("key=%q ok=%v", key, ok)
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	if key, ok := APIKey("openai", st); !ok || key != "from-env" {
		t.Errorf("key=%q ok=%v", key, ok)
	}
}

func TestAPIKeyGoogleEnvNames(t *testing.T) {
	st := newMemStore()
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	for _, id := range []string{"google", "gemini"} {
		if _, ok := APIKey(id, st); ok {
			t.Fatalf("expected no key for %q", id)
		}
	}
	// GOOGLE_API_KEY secondary alias resolves for both google and gemini ids.
	t.Setenv("GOOGLE_API_KEY", "google-ai-studio-key")
	for _, id := range []string{"google", "gemini"} {
		if key, ok := APIKey(id, st); !ok || key != "google-ai-studio-key" {
			t.Errorf("GOOGLE_API_KEY via %q: key=%q ok=%v", id, key, ok)
		}
		if got := Describe(id, st); got != "GOOGLE_API_KEY" {
			t.Errorf("Describe(%q) = %q, want GOOGLE_API_KEY", id, got)
		}
	}
	// GEMINI_API_KEY primary wins over GOOGLE_API_KEY.
	t.Setenv("GEMINI_API_KEY", "primary-gemini-key")
	for _, id := range []string{"google", "gemini"} {
		if key, ok := APIKey(id, st); !ok || key != "primary-gemini-key" {
			t.Errorf("GEMINI_API_KEY primary via %q: key=%q ok=%v", id, key, ok)
		}
		if got := Describe(id, st); got != "GEMINI_API_KEY" {
			t.Errorf("Describe(%q) = %q, want GEMINI_API_KEY", id, got)
		}
	}
}

func TestRefreshFlowsNoGoogleOAuth(t *testing.T) {
	// Neither the canonical google id nor the gemini alias has an OAuth refresh flow.
	for _, id := range []string{"google", "gemini"} {
		if _, ok := refreshFlows[id]; ok {
			t.Fatalf("%s must not have an OAuth refresh flow", id)
		}
	}
	if _, ok := refreshFlows["openai"]; !ok {
		t.Fatal("openai refresh flow missing")
	}
	if _, ok := refreshFlows["xai"]; !ok {
		t.Fatal("xai refresh flow missing")
	}
}

func TestNewPKCE(t *testing.T) {
	a := newPKCE()
	b := newPKCE()
	if a.verifier == "" || a.challenge == "" {
		t.Fatalf("empty pkce: %+v", a)
	}
	if a.verifier == b.verifier || a.challenge == b.challenge {
		t.Fatal("expected unique pkce pairs")
	}
	if len(a.verifier) != 64 {
		t.Errorf("verifier len = %d", len(a.verifier))
	}
}
