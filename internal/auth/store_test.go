package auth

import (
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
