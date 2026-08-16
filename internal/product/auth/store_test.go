package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoreDeleteAndProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Providers(); len(got) != 0 {
		t.Fatalf("Providers empty store = %v", got)
	}
	if err := st.Set("openai", Credential{Type: TypeAPIKey, APIKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("xai", Credential{Type: TypeAPIKey, APIKey: "k2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("anthropic", Credential{Type: TypeAPIKey, APIKey: "k3"}); err != nil {
		t.Fatal(err)
	}
	if got := st.Providers(); !reflect.DeepEqual(got, []string{"anthropic", "openai", "xai"}) {
		t.Errorf("Providers = %v, want sorted anthropic/openai/xai", got)
	}

	if err := st.Delete("openai"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("openai"); ok {
		t.Error("openai still present after Delete")
	}
	if got := st.Providers(); !reflect.DeepEqual(got, []string{"anthropic", "xai"}) {
		t.Errorf("Providers after delete = %v", got)
	}

	// Delete is idempotent for missing providers.
	if err := st.Delete("openai"); err != nil {
		t.Fatal(err)
	}

	st2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st2.Providers(); !reflect.DeepEqual(got, []string{"anthropic", "xai"}) {
		t.Errorf("reloaded Providers = %v", got)
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// UserHomeDir reads HOME on Unix.
	got := DefaultPath()
	want := filepath.Join(home, ".strike", "auth.json")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join(".strike", "auth.json")) {
		t.Errorf("DefaultPath suffix unexpected: %q", got)
	}
}

func TestDefaultPathResolvesStrikeDirSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := DefaultPath()
	want := filepath.Join(target, "auth.json")
	if got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

func TestOpenStoreCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("expected corrupt JSON error")
	}
}

func TestOpenStoreMissingIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Providers()) != 0 {
		t.Fatalf("Providers = %v", st.Providers())
	}
}

// TestGoogleStoreCanonicalAndLegacy covers gemini→google storage semantics:
// legacy gemini keys are readable via either id, canonical google wins when
// both exist, Set writes only google (and drops gemini), Delete either removes
// both, and Providers de-dupes to google.
func TestGoogleStoreCanonicalAndLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	// Seed a legacy-only file (pre-migration auth.json).
	if err := os.WriteFile(path, []byte(`{"gemini":{"type":"api","apiKey":"legacy-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy gemini is readable through both google and gemini ids.
	for _, id := range []string{"google", "gemini"} {
		cred, ok := st.Get(id)
		if !ok || cred.APIKey != "legacy-key" {
			t.Fatalf("Get(%q) = %+v ok=%v, want legacy-key", id, cred, ok)
		}
	}
	if got := st.Providers(); !reflect.DeepEqual(got, []string{"google"}) {
		t.Errorf("Providers with legacy gemini = %v, want [google]", got)
	}

	// Canonical google takes precedence when both keys exist on disk.
	if err := os.WriteFile(path, []byte(`{
		"gemini":{"type":"api","apiKey":"legacy-key"},
		"google":{"type":"api","apiKey":"canonical-key"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"google", "gemini"} {
		cred, ok := st.Get(id)
		if !ok || cred.APIKey != "canonical-key" {
			t.Fatalf("Get(%q) with both = %+v ok=%v, want canonical-key", id, cred, ok)
		}
	}
	if got := st.Providers(); !reflect.DeepEqual(got, []string{"google"}) {
		t.Errorf("Providers with both keys = %v, want [google]", got)
	}

	// Set via either id writes only google and removes gemini.
	if err := st.Set("gemini", Credential{Type: TypeAPIKey, APIKey: "from-alias"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]Credential
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := disk["gemini"]; ok {
		t.Errorf("Set(gemini) left gemini on disk: %s", raw)
	}
	if c, ok := disk["google"]; !ok || c.APIKey != "from-alias" {
		t.Errorf("Set(gemini) disk google = %+v ok=%v", c, ok)
	}
	cred, ok := st.Get("google")
	if !ok || cred.APIKey != "from-alias" {
		t.Fatalf("after Set(gemini): Get(google) = %+v ok=%v", cred, ok)
	}

	// Re-seed both, then Delete via either id removes both.
	if err := os.WriteFile(path, []byte(`{
		"gemini":{"type":"api","apiKey":"legacy-key"},
		"google":{"type":"api","apiKey":"canonical-key"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("gemini"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("google"); ok {
		t.Error("Get(google) still present after Delete(gemini)")
	}
	if _, ok := st.Get("gemini"); ok {
		t.Error("Get(gemini) still present after Delete(gemini)")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var afterDel map[string]Credential
	if err := json.Unmarshal(raw, &afterDel); err != nil {
		t.Fatal(err)
	}
	if _, ok := afterDel["gemini"]; ok {
		t.Errorf("disk still has gemini: %s", raw)
	}
	if _, ok := afterDel["google"]; ok {
		t.Errorf("disk still has google: %s", raw)
	}

	// Delete("google") also clears a legacy-only store.
	if err := os.WriteFile(path, []byte(`{"gemini":{"type":"api","apiKey":"legacy-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("google"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("gemini"); ok {
		t.Error("legacy gemini survived Delete(google)")
	}
}
