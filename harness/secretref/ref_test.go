package secretref_test

import (
	"strings"
	"testing"

	secret "github.com/jonathanung/strike-cli/harness/secretref"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		in       string
		wantOK   bool
		wantName string
	}{
		{"secret://env/OPENAI_API_KEY", true, "OPENAI_API_KEY"},
		{"{secret:env:STRIKE_TEST_KEY}", true, "STRIKE_TEST_KEY"},
		{"  secret://env/FOO  ", true, "FOO"},
		{"{env:FOO}", false, ""},
		{"$FOO", false, ""},
		{"secret://vault/x", false, ""},
		{"secret://env/", false, ""},
		{"secret://env/bad-name", false, ""},
		{"not-a-ref", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		r, ok := secret.ParseRef(tt.in)
		if ok != tt.wantOK {
			t.Errorf("ParseRef(%q) ok=%v want %v", tt.in, ok, tt.wantOK)
			continue
		}
		if ok && (r.Kind != secret.KindEnv || r.Name != tt.wantName) {
			t.Errorf("ParseRef(%q) = %+v want kind=env name=%s", tt.in, r, tt.wantName)
		}
		if ok && !secret.IsRef(tt.in) {
			t.Errorf("IsRef(%q) = false", tt.in)
		}
	}
}

func TestRefString(t *testing.T) {
	r := secret.Ref{Kind: secret.KindEnv, Name: "FOO"}
	if got := r.String(); got != "secret://env/FOO" {
		t.Fatalf("String = %q", got)
	}
}

func TestResolveAndEnvPairs(t *testing.T) {
	t.Setenv("STRIKE_SECRET_TEST_VAL", "s3cr3t-value-xyz")
	r, ok := secret.ParseRef("secret://env/STRIKE_SECRET_TEST_VAL")
	if !ok {
		t.Fatal("parse")
	}
	got, err := secret.Resolve(r)
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t-value-xyz" {
		t.Fatalf("Resolve = %q", got)
	}

	pairs, err := secret.EnvPairs(map[string]secret.Ref{
		"INJECTED": r,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0] != "INJECTED=s3cr3t-value-xyz" {
		t.Fatalf("EnvPairs = %#v", pairs)
	}

	merged, err := secret.MergeEnv([]string{"PATH=/bin", "INJECTED=old"}, map[string]secret.Ref{
		"INJECTED": r,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, kv := range merged {
		if strings.HasPrefix(kv, "INJECTED=") {
			found = kv
		}
	}
	if found != "INJECTED=s3cr3t-value-xyz" {
		t.Fatalf("MergeEnv INJECTED = %q, env=%v", found, merged)
	}
}

func TestResolveMissing(t *testing.T) {
	_, err := secret.Resolve(secret.Ref{Kind: secret.KindEnv, Name: "STRIKE_SECRET_MISSING_ZZZ"})
	if err == nil {
		t.Fatal("expected error for missing env")
	}
}
