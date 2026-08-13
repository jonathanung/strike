package tool

import (
	"strings"
	"testing"
)

func TestBashEnvMinimalNoHostLeak(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("STRIKE_SECRET_SHOULD_NOT_LEAK", "super-secret-value")
	t.Setenv("OPENAI_API_KEY", "sk-test-should-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-should-not-leak")

	env, err := bashEnv(&Context{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, leak := range []string{
		"STRIKE_SECRET_SHOULD_NOT_LEAK",
		"super-secret-value",
		"OPENAI_API_KEY",
		"sk-test-should-not-leak",
		"AWS_SECRET_ACCESS_KEY",
		"aws-should-not-leak",
	} {
		if strings.Contains(joined, leak) {
			t.Fatalf("env leaked %q:\n%s", leak, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("expected PATH in minimal env: %v", env)
	}
	if !strings.Contains(joined, "HOME=/home/test") {
		t.Fatalf("expected HOME in minimal env: %v", env)
	}
}

func TestBashEnvForwardsEvalContainer(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("STRIKE_EVAL_CONTAINER", "cid123")
	env, err := bashEnv(&Context{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "STRIKE_EVAL_CONTAINER=cid123") {
		t.Fatalf("expected eval container in bash env: %v", env)
	}
}

func TestBashEnvSecretRefs(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("MY_TOKEN", "token-value-xyz")
	env, err := bashEnv(&Context{
		BashSecrets: map[string]string{
			"GITHUB_TOKEN": "secret://env/MY_TOKEN",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, kv := range env {
		if kv == "GITHUB_TOKEN=token-value-xyz" {
			found = true
		}
		// Ref wire form must not appear as a value placeholder incorrectly.
		if strings.Contains(kv, "secret://") {
			t.Fatalf("unresolved ref in env: %s", kv)
		}
	}
	if !found {
		t.Fatalf("GITHUB_TOKEN not injected: %v", env)
	}
}

func TestBashEnvBadSecretRef(t *testing.T) {
	_, err := bashEnv(&Context{
		BashSecrets: map[string]string{
			"X": "not-a-ref",
		},
	})
	if err == nil {
		t.Fatal("expected error for bad ref")
	}
}

func TestBashMinimalEnvKeysDocumented(t *testing.T) {
	keys := BashMinimalEnvKeys()
	if len(keys) < 5 {
		t.Fatalf("too few keys: %v", keys)
	}
	// Sanity: PATH and HOME always listed.
	want := map[string]bool{"PATH": false, "HOME": false}
	for _, k := range keys {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing %s in BashMinimalEnvKeys", k)
		}
	}
}
