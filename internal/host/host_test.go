package host

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// ctxKey is a private context key used to prove a caller's ctx reaches an
// injected closure unchanged.
type ctxKey struct{}

func TestNewSkillRender(t *testing.T) {
	// A nil renderer yields "" regardless of args, and the display fields
	// round-trip through NewSkill.
	nilRender := NewSkill("greet", "say hi", false, nil)
	if nilRender.Name != "greet" || nilRender.Description != "say hi" || nilRender.HasArgs {
		t.Errorf("NewSkill fields = %+v", nilRender)
	}
	if got := nilRender.Render("anything"); got != "" {
		t.Errorf("nil renderer Render = %q, want empty", got)
	}

	// A real renderer receives args and its result is returned verbatim.
	withRender := NewSkill("echo", "echoes", true, func(args string) string {
		return "prompt:" + args
	})
	if !withRender.HasArgs {
		t.Error("HasArgs should be true")
	}
	if got := withRender.Render("xyz"); got != "prompt:xyz" {
		t.Errorf("Render = %q, want prompt:xyz", got)
	}
}

func TestOAuthLoginWait(t *testing.T) {
	// A nil wait closure is an error, and URL round-trips.
	noWait := NewOAuthLogin("https://issuer/authorize", nil)
	if noWait.URL != "https://issuer/authorize" {
		t.Errorf("URL = %q", noWait.URL)
	}
	if _, err := noWait.Wait(context.Background()); err == nil {
		t.Error("Wait with nil closure: expected error")
	}

	// The outcome message and error pass through, and the caller ctx reaches
	// the closure unchanged.
	sentinel := errors.New("callback failed")
	var seen context.Context
	login := NewOAuthLogin("u", func(ctx context.Context) (string, error) {
		seen = ctx
		return "logged in", sentinel
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	msg, err := login.Wait(ctx)
	if msg != "logged in" || !errors.Is(err, sentinel) {
		t.Errorf("Wait = %q, %v; want logged in, callback failed", msg, err)
	}
	if seen == nil || seen.Value(ctxKey{}) != "v" {
		t.Error("ctx did not reach the wait closure")
	}
}

func TestOAuthLoginWaitCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	login := NewOAuthLogin("u", func(ctx context.Context) (string, error) {
		return "", ctx.Err()
	})
	if _, err := login.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait err = %v, want context.Canceled", err)
	}
}

func TestDeviceLoginPoll(t *testing.T) {
	// A nil poll closure is an error, and the code/URI round-trip.
	noPoll := NewDeviceLogin("WXYZ", "https://verify.example", nil)
	if noPoll.UserCode != "WXYZ" || noPoll.VerificationURI != "https://verify.example" {
		t.Errorf("device fields = %+v", noPoll)
	}
	if _, err := noPoll.Poll(context.Background()); err == nil {
		t.Error("Poll with nil closure: expected error")
	}

	// The outcome message passes through and the caller ctx reaches poll.
	var seen context.Context
	login := NewDeviceLogin("C", "V", func(ctx context.Context) (string, error) {
		seen = ctx
		return "device authorized", nil
	})
	ctx := context.WithValue(context.Background(), ctxKey{}, "z")
	msg, err := login.Poll(ctx)
	if msg != "device authorized" || err != nil {
		t.Errorf("Poll = %q, %v", msg, err)
	}
	if seen == nil || seen.Value(ctxKey{}) != "z" {
		t.Error("ctx did not reach the poll closure")
	}
}

func TestDeviceLoginPollCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	login := NewDeviceLogin("C", "V", func(ctx context.Context) (string, error) {
		return "", ctx.Err()
	})
	if _, err := login.Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Poll err = %v, want context.Canceled", err)
	}
}

// TestContractImportsStdlibOnly guards the core invariant of this package:
// the host contract must import nothing outside the standard library, so
// frontends can build and test against it without pulling in backend or
// third-party code. Stdlib import paths have no dot in their first segment.
func TestContractImportsStdlibOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %q; the host contract must import stdlib only", name, path)
			}
		}
	}
}
