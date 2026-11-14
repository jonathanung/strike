package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// TestSoloVerifyTriageAcrossTurns: a failing go test gate is triaged with a
// focused rerun, the engine keeps the history across turns, and the next
// claim runs the focused target first (skipping the full gate while it
// still fails). The fake `go` on PATH keeps the test offline and fast.
func TestSoloVerifyTriageAcrossTurns(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho \"fake go $*\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	gate := `printf '%s\n' '--- FAIL: TestBroken (0.00s)' '    a_test.go:7: boom' 'FAIL	example.com/mod/pkg/a	0.01s'; exit 1`
	prov := newScriptedProvider(completedStep("done one"), completedStep("done two"))
	eng := engine.New(engine.Options{
		SessionID:       "solo-vg-triage",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         work,
		Rules:           []permission.Ruleset{permission.Defaults()},
		Verify:          []tool.VerifyGate{{Kind: "cmd", Value: gate, Description: "unit"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	turn := func(text string) protocol.VerificationCheck {
		t.Helper()
		eng.Ops() <- protocol.UserInput{Text: text}
		var tc *protocol.TurnCompleted
		collectEventsUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
			if done, ok := evs[len(evs)-1].(protocol.TurnCompleted); ok {
				tc = &done
				return true
			}
			return false
		})
		if tc.Verification == nil || tc.Verification.Passed || len(tc.Verification.Checks) != 1 {
			t.Fatalf("verification = %#v", tc.Verification)
		}
		return tc.Verification.Checks[0]
	}

	const focused = "go test './pkg/a' -run '^(TestBroken)$' -count=1"
	c := turn("first")
	tr := c.Triage
	if tr == nil || tr.FocusedCommand != focused || len(tr.Attempts) != 1 || tr.FullGateSkipped {
		t.Fatalf("turn1 triage = %#v", tr)
	}
	if c.Value != gate || !strings.Contains(c.Output, "a_test.go:7: boom") {
		t.Fatalf("turn1 original command/output lost: %#v", c)
	}
	if len(tr.Failures) != 1 || tr.Failures[0].Package != "example.com/mod/pkg/a" || tr.Failures[0].Tests[0] != "TestBroken" {
		t.Fatalf("turn1 failures = %#v", tr.Failures)
	}
	if !strings.Contains(tr.Attempts[0].Output, "fake go test ./pkg/a -run ^(TestBroken)$ -count=1") {
		t.Fatalf("turn1 attempt = %#v", tr.Attempts[0])
	}

	c = turn("second")
	tr = c.Triage
	if tr == nil || !tr.FullGateSkipped || len(tr.Attempts) != 2 || !strings.Contains(tr.OriginalOutput, "a_test.go:7: boom") {
		t.Fatalf("turn2 triage = %#v", tr)
	}
	if !strings.Contains(c.Output, "fake go") || !strings.Contains(c.Error, "full gate skipped") {
		t.Fatalf("turn2 check = %#v", c)
	}
}
