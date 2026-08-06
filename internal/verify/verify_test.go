package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseGate(t *testing.T) {
	t.Parallel()
	g, err := ParseGate("CMD", " make test ", " unit ")
	if err != nil {
		t.Fatal(err)
	}
	if g.Kind != KindCmd || g.Value != "make test" || g.Description != "unit" {
		t.Fatalf("%+v", g)
	}
	g, err = ParseGate("schema", "Handoff", "")
	if err != nil || g.Value != "handoff" {
		t.Fatalf("schema: %+v err=%v", g, err)
	}
	if _, err := ParseGate("judge", "x", ""); err == nil {
		t.Fatal("want unknown kind error")
	}
	if _, err := ParseGate("cmd", "", ""); err == nil {
		t.Fatal("want empty value error")
	}
	if _, err := ParseGate("schema", "other", ""); err == nil {
		t.Fatal("want unknown schema error")
	}
}

func TestParseGatesMax(t *testing.T) {
	t.Parallel()
	in := make([]Gate, MaxGates+1)
	for i := range in {
		in[i] = Gate{Kind: KindCmd, Value: "true"}
	}
	if _, err := ParseGates(in); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("err = %v", err)
	}
	ok, err := ParseGates(in[:MaxGates])
	if err != nil || len(ok) != MaxGates {
		t.Fatalf("len=%d err=%v", len(ok), err)
	}
}

func TestRunCmdGates(t *testing.T) {
	t.Parallel()
	r := &Runner{
		CmdRunner: func(_ context.Context, _, command string) (int, string, error) {
			if command == "true" {
				return 0, "ok\n", nil
			}
			return 7, "boom\n", nil
		},
		Now: func() time.Time { return time.Unix(100, 0).UTC() },
	}
	res := r.Run(context.Background(), []Gate{
		{Kind: KindCmd, Value: "true", Description: "pass"},
		{Kind: KindCmd, Value: "false", Description: "fail"},
	}, Input{Claimed: true, Env: EnvMetadata{SessionID: "s1"}})
	if res.Passed || !res.Claimed || res.Verified {
		t.Fatalf("res=%+v", res)
	}
	if len(res.Checks) != 2 || !res.Checks[0].Passed || res.Checks[1].Passed {
		t.Fatalf("checks=%+v", res.Checks)
	}
	if res.Checks[1].ExitCode != 7 {
		t.Fatalf("exit=%d", res.Checks[1].ExitCode)
	}
	if res.Env.SessionID != "s1" || res.Env.StartedAt == "" || res.Env.FinishedAt == "" {
		t.Fatalf("env=%+v", res.Env)
	}
	if !strings.Contains(res.Summary, "fail") {
		t.Fatalf("summary=%q", res.Summary)
	}
}

func TestRunIgnoresModelSelfReport(t *testing.T) {
	t.Parallel()
	// Handoff "looks verified" but cmd fails — must not pass.
	r := &Runner{
		CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 1, "tests failed", nil
		},
	}
	res := r.Run(context.Background(), []Gate{
		{Kind: KindCmd, Value: "make test"},
	}, Input{
		Claimed: true,
		Handoff: &HandoffView{
			Summary:       "all good",
			HasStructured: true,
			Incomplete:    false,
		},
	})
	if res.Passed || res.Verified {
		t.Fatalf("model must not self-certify: %+v", res)
	}
}

func TestRunSchemaHandoff(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	// Incomplete / missing structured → fail.
	res := r.Run(context.Background(), []Gate{
		{Kind: KindSchema, Value: "handoff"},
	}, Input{
		Claimed: true,
		Handoff: &HandoffView{Summary: "x", Incomplete: true, HasStructured: false},
	})
	if res.Passed || res.Verified {
		t.Fatalf("incomplete should fail: %+v", res)
	}
	// Complete structured → pass.
	res = r.Run(context.Background(), []Gate{
		{Kind: KindSchema, Value: "handoff"},
	}, Input{
		Claimed: true,
		Handoff: &HandoffView{Summary: "done", HasStructured: true},
	})
	if !res.Passed || !res.Verified {
		t.Fatalf("complete handoff should pass: %+v", res)
	}
	// Empty summary → fail.
	res = r.Run(context.Background(), []Gate{
		{Kind: KindSchema, Value: "handoff"},
	}, Input{
		Claimed: true,
		Handoff: &HandoffView{Summary: "  ", HasStructured: true},
	})
	if res.Passed {
		t.Fatal("empty summary should fail")
	}
	// Nil handoff → fail.
	res = r.Run(context.Background(), []Gate{
		{Kind: KindSchema, Value: "handoff"},
	}, Input{Claimed: true})
	if res.Passed {
		t.Fatal("nil handoff should fail")
	}
}

func TestRunPathGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{WorkDir: dir}
	res := r.Run(context.Background(), []Gate{
		{Kind: KindPath, Value: "ok.txt"},
		{Kind: KindPath, Value: "missing.txt"},
	}, Input{Claimed: true})
	if res.Passed || !res.Checks[0].Passed || res.Checks[1].Passed {
		t.Fatalf("%+v", res)
	}
}

func TestRunNoGates(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	res := r.Run(context.Background(), nil, Input{Claimed: true})
	if !res.Passed || !res.Verified || !res.Claimed {
		t.Fatalf("%+v", res)
	}
	res = r.Run(context.Background(), nil, Input{Claimed: false})
	if res.Passed || !res.Verified {
		t.Fatalf("%+v", res)
	}
}

func TestRunCmdAndSchemaTogether(t *testing.T) {
	t.Parallel()
	r := &Runner{
		CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 0, "", nil
		},
	}
	res := r.Run(context.Background(), []Gate{
		{Kind: KindCmd, Value: "true"},
		{Kind: KindSchema, Value: "handoff"},
	}, Input{
		Claimed: true,
		Handoff: &HandoffView{Summary: "ok", HasStructured: true},
	})
	if !res.Passed || len(res.Checks) != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestFailedCheckLines(t *testing.T) {
	t.Parallel()
	lines := FailedCheckLines(Result{Checks: []CheckResult{
		{Name: "a", Passed: true},
		{Name: "b", Passed: false, Error: "exit 1", Output: "line1\nline2"},
	}})
	if len(lines) != 1 || !strings.Contains(lines[0], "b") || !strings.Contains(lines[0], "exit 1") {
		t.Fatalf("%v", lines)
	}
}

func TestRunRealCmd(t *testing.T) {
	t.Parallel()
	r := &Runner{WorkDir: t.TempDir(), Timeout: 5 * time.Second}
	res := r.Run(context.Background(), []Gate{
		{Kind: KindCmd, Value: "echo hello && true"},
	}, Input{Claimed: true})
	if !res.Passed {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Checks[0].Output, "hello") {
		t.Fatalf("output=%q", res.Checks[0].Output)
	}
}
