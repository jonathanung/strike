package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectForwardedEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SEC")
	t.Setenv("OTHER", "x")

	vars, warnings := CollectForwardedEnv([]string{"AWS_*"})
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if len(vars) != 2 {
		t.Fatalf("vars: %v", vars)
	}
	joined := strings.Join(vars, "\n")
	if !strings.Contains(joined, "AWS_ACCESS_KEY_ID=AKID") || !strings.Contains(joined, "AWS_SECRET_ACCESS_KEY=SEC") {
		t.Fatalf("got %v", vars)
	}

	vars, warnings = CollectForwardedEnv([]string{"NONEXISTENT_ZXQW_*"})
	if len(vars) != 0 || len(warnings) != 1 {
		t.Fatalf("nomatch: vars=%v warn=%v", vars, warnings)
	}

	vars, warnings = CollectForwardedEnv(nil)
	if vars != nil || warnings != nil {
		t.Fatalf("empty patterns")
	}

	t.Setenv("SHARED_VAR", "v")
	vars, _ = CollectForwardedEnv([]string{"SHARED_VAR", "SHARED_*"})
	if len(vars) != 1 {
		t.Fatalf("dedupe: %v", vars)
	}
}

func TestParseEnvFileAndRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n\nFOO=bar\nBAZ=a=b\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["FOO"] != "bar" || m["BAZ"] != "a=b" {
		t.Fatalf("%v", m)
	}

	t.Setenv("HOST_ONLY", "1")
	if err := ValidateRequiredEnv([]string{"HOST_ONLY", "FOO"}, ".env", dir); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequiredEnv([]string{"MISSING_XYZ"}, "", dir); err == nil {
		t.Fatal("expected missing env error")
	}
}
