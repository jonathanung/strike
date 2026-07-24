package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{Type: TypeOAuth, Access: "acc", Refresh: "ref", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := st.Set("xai", cred); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json permissions = %o, want 600", perm)
	}

	st2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st2.Get("xai")
	if !ok || got.Access != "acc" || got.Refresh != "ref" {
		t.Errorf("reloaded credential = %+v, ok=%v", got, ok)
	}
}

func TestBearerSourcePrecedence(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
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
