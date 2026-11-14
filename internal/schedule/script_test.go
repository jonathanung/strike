package schedule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func scriptSpec(name string) Spec {
	return Spec{
		Name:    name,
		Kind:    KindInterval,
		Every:   "1h",
		Source:  SourceScript,
		Command: "make test",
	}
}

func openScript(t *testing.T, dir string, now time.Time) *Service {
	t.Helper()
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestPrepareScriptValidation(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	okDir := t.TempDir()
	file := filepath.Join(okDir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	armed := time.Date(2026, 11, 14, 7, 0, 0, 0, time.FixedZone("MST", -7*3600))
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
		check   func(t *testing.T, sch Schedule)
	}{
		{
			name:   "defaults",
			mutate: func(s *Spec) { s.Source = " Script "; s.ArmedAt = armed },
			check: func(t *testing.T, sch Schedule) {
				if sch.Source != SourceScript || sch.Command != "make test" || sch.Overlap != OverlapSkip || sch.Missed != MissedSkip {
					t.Fatalf("defaults %+v", sch)
				}
				if sch.Timeout != "30m" {
					t.Fatalf("timeout default %q", sch.Timeout)
				}
				if sch.CommandHash != HashCommand("make test") || len(sch.CommandHash) != 64 {
					t.Fatalf("hash %q", sch.CommandHash)
				}
				if !sch.ArmedAt.Equal(armed) || sch.ArmedAt.Location() != time.UTC {
					t.Fatalf("armedAt %s", sch.ArmedAt)
				}
				if sch.Dir != "" || sch.SecretRefs != nil {
					t.Fatalf("unexpected dir/refs %+v", sch)
				}
			},
		},
		{
			name:   "unarmed stays unarmed",
			mutate: func(s *Spec) {},
			check: func(t *testing.T, sch Schedule) {
				if !sch.ArmedAt.IsZero() {
					t.Fatalf("armedAt %s", sch.ArmedAt)
				}
			},
		},
		{name: "command required", mutate: func(s *Spec) { s.Command = "  " }, wantErr: "command is required"},
		{name: "command too long", mutate: func(s *Spec) { s.Command = strings.Repeat("x", maxCommand+1) }, wantErr: "command is too long"},
		{
			name:   "command at cap",
			mutate: func(s *Spec) { s.Command = strings.Repeat("x", maxCommand) },
			check: func(t *testing.T, sch Schedule) {
				if len(sch.Command) != maxCommand {
					t.Fatalf("len %d", len(sch.Command))
				}
			},
		},
		{name: "prompt rejected", mutate: func(s *Spec) { s.Prompt = "hi" }, wantErr: "prompt, goalId, and workflow must be empty"},
		{name: "goalId rejected", mutate: func(s *Spec) { s.GoalID = "g1" }, wantErr: "prompt, goalId, and workflow must be empty"},
		{name: "workflow rejected", mutate: func(s *Spec) { s.Workflow = "wf" }, wantErr: "prompt, goalId, and workflow must be empty"},
		{name: "overlap queue rejected", mutate: func(s *Spec) { s.Overlap = OverlapQueue }, wantErr: "overlap must be skip for script"},
		{name: "overlap allow rejected", mutate: func(s *Spec) { s.Overlap = "Allow" }, wantErr: "overlap must be skip for script"},
		{name: "overlap garbage rejected", mutate: func(s *Spec) { s.Overlap = "sometimes" }, wantErr: "overlap must be skip, queue, or allow"},
		{
			name:   "overlap skip explicit",
			mutate: func(s *Spec) { s.Overlap = "SKIP" },
			check: func(t *testing.T, sch Schedule) {
				if sch.Overlap != OverlapSkip {
					t.Fatalf("overlap %q", sch.Overlap)
				}
			},
		},
		{
			name:   "missed once kept",
			mutate: func(s *Spec) { s.Missed = MissedOnce },
			check: func(t *testing.T, sch Schedule) {
				if sch.Missed != MissedOnce {
					t.Fatalf("missed %q", sch.Missed)
				}
			},
		},
		{name: "timeout below minimum", mutate: func(s *Spec) { s.Timeout = "9s" }, wantErr: "timeout: minimum is 10s"},
		{name: "timeout above maximum", mutate: func(s *Spec) { s.Timeout = "6h1s" }, wantErr: "timeout: maximum is 6h"},
		{name: "timeout unparsable", mutate: func(s *Spec) { s.Timeout = "soon" }, wantErr: "timeout:"},
		{
			name:   "timeout bounds inclusive",
			mutate: func(s *Spec) { s.Timeout = " 10s " },
			check: func(t *testing.T, sch Schedule) {
				if sch.Timeout != "10s" {
					t.Fatalf("timeout %q", sch.Timeout)
				}
			},
		},
		{
			name:   "timeout max inclusive",
			mutate: func(s *Spec) { s.Timeout = "6h" },
			check: func(t *testing.T, sch Schedule) {
				if sch.Timeout != "6h" {
					t.Fatalf("timeout %q", sch.Timeout)
				}
			},
		},
		{name: "dir relative", mutate: func(s *Spec) { s.Dir = "relative/path" }, wantErr: "dir must be an absolute path"},
		{name: "dir missing", mutate: func(s *Spec) { s.Dir = filepath.Join(okDir, "missing") }, wantErr: "dir:"},
		{name: "dir is a file", mutate: func(s *Spec) { s.Dir = file }, wantErr: "dir is not a directory"},
		{
			name:   "dir ok",
			mutate: func(s *Spec) { s.Dir = okDir },
			check: func(t *testing.T, sch Schedule) {
				if sch.Dir != okDir {
					t.Fatalf("dir %q", sch.Dir)
				}
			},
		},
		{name: "secret ref bad", mutate: func(s *Spec) { s.SecretRefs = []string{"PLAIN_VALUE"} }, wantErr: "secretRefs:"},
		{name: "secret ref wrong kind", mutate: func(s *Spec) { s.SecretRefs = []string{"secret://vault/X"} }, wantErr: "secretRefs:"},
		{
			name:   "secret refs canonical",
			mutate: func(s *Spec) { s.SecretRefs = []string{" secret://env/TOKEN ", "", "{secret:env:OTHER}"} },
			check: func(t *testing.T, sch Schedule) {
				want := []string{"secret://env/TOKEN", "secret://env/OTHER"}
				if strings.Join(sch.SecretRefs, ",") != strings.Join(want, ",") {
					t.Fatalf("refs %v", sch.SecretRefs)
				}
			},
		},
		{name: "bad permission mode still checked", mutate: func(s *Spec) { s.PermissionMode = "not-a-mode" }, wantErr: "unknown permission mode"},
		{name: "bad source", mutate: func(s *Spec) { s.Source = "shell" }, wantErr: "source must be prompt, goal, workflow, or script"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := scriptSpec("s")
			tc.mutate(&spec)
			sch, err := prepare(spec, now, "sch_test")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got schedule %+v", tc.wantErr, sch)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error %v is not ErrInvalid", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, sch)
		})
	}
}

func TestPrepareScriptFieldsRejectedForOtherSources(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"command", func(s *Spec) { s.Command = "rm -rf /" }},
		{"dir", func(s *Spec) { s.Dir = "/" }},
		{"timeout", func(s *Spec) { s.Timeout = "1m" }},
		{"secretRefs", func(s *Spec) { s.SecretRefs = []string{"secret://env/X"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := promptSpec("p", "1h")
			tc.mutate(&spec)
			_, err := prepare(spec, now, "sch_test")
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "apply to script schedules only") {
				t.Fatalf("err %v", err)
			}
		})
	}
	// A prompt schedule never carries a hash or arming time.
	sch, err := prepare(promptSpec("p", "1h"), now, "sch_test")
	if err != nil {
		t.Fatal(err)
	}
	if sch.CommandHash != "" || !sch.ArmedAt.IsZero() || sch.Timeout != "" {
		t.Fatalf("prompt schedule carries script fields: %+v", sch)
	}
}

func TestReplaceRefusesSourceChangeToOrFromScript(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := openScript(t, dir, now)
	script, err := svc.Create(scriptSpec("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := svc.Create(promptSpec("chat", "1h"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Replace(script.ID, promptSpec("nightly", "1h")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "to or from script") {
		t.Fatalf("script->prompt: %v", err)
	}
	if _, err := svc.Replace(prompt.ID, scriptSpec("chat")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "to or from script") {
		t.Fatalf("prompt->script: %v", err)
	}
	// Same source still replaces, re-arms, and re-pins the hash.
	next := scriptSpec("nightly")
	next.Command = "make lint"
	next.ArmedAt = now
	got, err := svc.Replace(script.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "make lint" || got.CommandHash != HashCommand("make lint") || !got.ArmedAt.Equal(now) {
		t.Fatalf("replace %+v", got)
	}
	if got.CommandHash == script.CommandHash {
		t.Fatal("hash did not change with the command")
	}
	// The refusal happens before any hook lookup: goal source with no Goal hook
	// still reports the source change, not "goals are unavailable".
	goal := promptSpec("nightly", "1h")
	goal.Source = SourceGoal
	goal.GoalID = "g1"
	if _, err := svc.Replace(script.ID, goal); err == nil || !strings.Contains(err.Error(), "to or from script") {
		t.Fatalf("script->goal: %v", err)
	}
}

func TestScriptScheduleRoundTripsThroughStore(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := openScript(t, dir, now)
	workDir := t.TempDir()
	spec := scriptSpec("nightly")
	spec.Dir = workDir
	spec.Timeout = "45m"
	spec.SecretRefs = []string{"secret://env/GH_TOKEN"}
	spec.ArmedAt = now.Add(-time.Minute)
	sch, err := svc.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	code := 2
	run := Run{
		ID:            "run_1",
		ScheduleID:    sch.ID,
		FiredAt:       now,
		ScheduledFor:  now,
		Status:        StatusFailed,
		StartedAt:     now.Add(time.Second),
		EndedAt:       now.Add(3 * time.Second),
		ExitCode:      &code,
		OutputPreview: "FAIL\tpkg",
		OutputRef:     "run_1",
		Decision:      DecisionArmed,
		Error:         "exit 2",
	}
	denied := Run{ID: "run_0", ScheduleID: sch.ID, FiredAt: now.Add(-time.Hour), Status: StatusDenied, Decision: DecisionDeny}
	if _, err := svc.store.Mutate(sch.ID, func(s *Schedule) error {
		addRun(s, denied)
		addRun(s, run)
		s.LastStatus = StatusFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopen := openScript(t, dir, now)
	got, ok := reopen.Get(sch.ID)
	if !ok {
		t.Fatal("missing after reopen")
	}
	if got.Source != SourceScript || got.Command != "make test" || got.Dir != workDir || got.Timeout != "45m" {
		t.Fatalf("schedule %+v", got)
	}
	if len(got.SecretRefs) != 1 || got.SecretRefs[0] != "secret://env/GH_TOKEN" {
		t.Fatalf("refs %v", got.SecretRefs)
	}
	if !got.ArmedAt.Equal(now.Add(-time.Minute)) || got.CommandHash != HashCommand("make test") {
		t.Fatalf("arming %s %q", got.ArmedAt, got.CommandHash)
	}
	if got.Prompt != "" || got.LastSessionID != "" || got.LastStatus != StatusFailed {
		t.Fatalf("unexpected session fields %+v", got)
	}
	if len(got.Runs) != 2 || got.Runs[0].ID != "run_1" || got.Runs[1].Status != StatusDenied || got.Runs[1].Decision != DecisionDeny {
		t.Fatalf("runs %+v", got.Runs)
	}
	r := got.Runs[0]
	if r.Status != StatusFailed || r.ExitCode == nil || *r.ExitCode != 2 || r.Decision != DecisionArmed {
		t.Fatalf("run %+v", r)
	}
	if !r.StartedAt.Equal(run.StartedAt) || !r.EndedAt.Equal(run.EndedAt) || r.OutputPreview != "FAIL\tpkg" || r.OutputRef != "run_1" || r.SessionID != "" {
		t.Fatalf("run %+v", r)
	}
	if got.Runs[1].ExitCode != nil {
		t.Fatalf("denied run has exit code %d", *got.Runs[1].ExitCode)
	}
	// ExitCode is deep-copied: a caller mutating its copy cannot reach the store.
	*r.ExitCode = 99
	again, _ := reopen.Get(sch.ID)
	if *again.Runs[0].ExitCode != 2 {
		t.Fatalf("exit code aliased: %d", *again.Runs[0].ExitCode)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"source": "script"`, `"command": "make test"`, `"timeout": "45m"`, `"secretRefs"`, `"armedAt"`, `"commandHash"`, `"exitCode": 2`, `"decision": "armed"`, `"outputRef": "run_1"`, `"startedAt"`, `"endedAt"`, `"outputPreview"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("schedules.json lacks %s:\n%s", key, raw)
		}
	}
}

func TestScriptRunsSkipIdleFlip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Busy: func(string) bool { return false }}, dir)
	if err != nil {
		t.Fatal(err)
	}
	script, err := svc.Create(scriptSpec("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := svc.Create(promptSpec("chat", "1h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{script.ID, prompt.ID} {
		if _, err := svc.store.Mutate(id, func(s *Schedule) error {
			s.LastStatus = StatusStarted
			addRun(s, Run{ID: "run_live", ScheduleID: s.ID, FiredAt: now, Status: StatusStarted})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Tick(context.Background(), false)
	gotScript, _ := svc.Get(script.ID)
	if gotScript.LastStatus != StatusStarted || gotScript.Runs[0].Status != StatusStarted {
		t.Fatalf("script run flipped by idle check: %s / %s", gotScript.LastStatus, gotScript.Runs[0].Status)
	}
	gotPrompt, _ := svc.Get(prompt.ID)
	if gotPrompt.LastStatus != StatusFinished || gotPrompt.Runs[0].Status != StatusFinished {
		t.Fatalf("prompt run not flipped: %s / %s", gotPrompt.LastStatus, gotPrompt.Runs[0].Status)
	}
	if startedRun(gotScript) {
		t.Fatal("startedRun reports a script schedule")
	}
	copyScript := gotScript
	noteIdle(&copyScript)
	if copyScript.LastStatus != StatusStarted {
		t.Fatal("noteIdle touched a script schedule")
	}
}

func TestRunOutputSidecarWriteReadAndBounds(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "AKIAABCDEFGHIJKLMNOP"
	big := bytes.Repeat([]byte("line of output\n"), (1<<20)/15)
	big = append(big, []byte("key "+secret+"\nlast line Z")...)
	ref, err := st.WriteRunOutput("sch_a", "run_1", big)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "run_1" {
		t.Fatalf("ref %q", ref)
	}
	got, err := st.ReadRunOutput("sch_a", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxRunOutput {
		t.Fatalf("sidecar %d bytes exceeds %d", len(got), maxRunOutput)
	}
	if !bytes.HasSuffix(got, []byte("last line Z")) {
		t.Fatalf("sidecar does not end with the tail: %q", got[len(got)-40:])
	}
	if !bytes.HasPrefix(got, []byte("… (")) || !bytes.Contains(got, []byte(" bytes dropped)\n")) {
		t.Fatalf("missing dropped note: %q", got[:40])
	}
	if bytes.Contains(got, []byte(secret)) {
		t.Fatal("sidecar leaked a credential-shaped token")
	}
	if !bytes.Contains(got, []byte("[REDACTED_AWS_KEY]")) {
		t.Fatal("sidecar is not scrubbed")
	}
	path := filepath.Join(dir, "schedules", "runs", "sch_a", "run_1.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o", info.Mode().Perm())
	}
	for _, d := range []string{filepath.Join(dir, "schedules"), filepath.Join(dir, "schedules", "runs"), filepath.Join(dir, "schedules", "runs", "sch_a")} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s mode %o", d, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp file left behind: %v", err)
	}
	// Small output is stored whole, without a note.
	if _, err := st.WriteRunOutput("sch_a", "run_2", []byte("ok\n")); err != nil {
		t.Fatal(err)
	}
	small, err := st.ReadRunOutput("sch_a", "run_2")
	if err != nil || string(small) != "ok\n" {
		t.Fatalf("small %q %v", small, err)
	}
	if _, err := st.ReadRunOutput("sch_a", "run_none"); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("missing sidecar: %v", err)
	}
	for _, bad := range [][2]string{{"../x", "run_1"}, {"sch_a", "../../etc"}, {"", "run_1"}, {"sch_a", ""}, {"a/b", "run_1"}, {"sch_a", "."}} {
		if _, err := st.WriteRunOutput(bad[0], bad[1], []byte("x")); err == nil {
			t.Fatalf("accepted unsafe ids %q", bad)
		}
		if _, err := st.ReadRunOutput(bad[0], bad[1]); err == nil || errors.Is(err, ErrNoOutput) {
			t.Fatalf("read accepted unsafe ids %q: %v", bad, err)
		}
	}
}

func TestTailOutputBoundary(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
	}{
		{"exact", strings.Repeat("a", 100), 100},
		{"one over", strings.Repeat("a", 101), 100},
		{"far over", strings.Repeat("ab", 5000), 100},
		{"multibyte", strings.Repeat("é", 300), 100},
		{"digit rollover", strings.Repeat("x", 100+99), 100},
		{"limit below note", strings.Repeat("x", 50), 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tailOutput(tc.in, tc.limit)
			if len(out) > tc.limit {
				t.Fatalf("len %d > %d", len(out), tc.limit)
			}
			if len(tc.in) <= tc.limit {
				if out != tc.in {
					t.Fatalf("short input changed: %q", out)
				}
				return
			}
			if tc.limit < 32 {
				// Nothing of the text fits beside the note; no panic, empty result.
				if out != "" {
					t.Fatalf("tiny limit produced %q", out)
				}
				return
			}
			note, body, ok := strings.Cut(out, "\n")
			if !ok || !strings.HasPrefix(note, "… (") || !strings.HasSuffix(note, " bytes dropped)") {
				t.Fatalf("bad note: %q", out)
			}
			if !strings.HasSuffix(tc.in, body) || body == "" {
				t.Fatalf("tail is not a suffix: %q", out)
			}
			if !utf8.ValidString(out) {
				t.Fatalf("invalid utf8: %q", out)
			}
		})
	}
}

func TestRunOutputFallsOffWithRunCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := openScript(t, dir, now)
	sch, err := svc.Create(scriptSpec("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	runID := func(i int) string { return fmt.Sprintf("run_%03d", i) }
	for i := 0; i <= maxRuns; i++ {
		id := runID(i)
		if _, err := svc.store.Mutate(sch.ID, func(s *Schedule) error {
			addRun(s, Run{ID: id, ScheduleID: s.ID, FiredAt: now.Add(time.Duration(i) * time.Minute), Status: StatusFinished, OutputRef: id})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.store.WriteRunOutput(sch.ID, id, []byte("out "+id)); err != nil {
			t.Fatal(err)
		}
		if i == maxRuns-1 {
			if _, err := svc.store.ReadRunOutput(sch.ID, runID(0)); err != nil {
				t.Fatalf("oldest dropped early: %v", err)
			}
		}
	}
	got, _ := svc.Get(sch.ID)
	if len(got.Runs) != maxRuns || got.Runs[0].ID != runID(maxRuns) || got.Runs[maxRuns-1].ID != runID(1) {
		t.Fatalf("runs %d first %s last %s", len(got.Runs), got.Runs[0].ID, got.Runs[len(got.Runs)-1].ID)
	}
	if _, err := svc.store.ReadRunOutput(sch.ID, runID(0)); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("oldest sidecar survived the cap: %v", err)
	}
	for _, i := range []int{1, maxRuns} {
		if _, err := svc.RunOutput(sch.ID, runID(i)); err != nil {
			t.Fatalf("run %d sidecar missing: %v", i, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "schedules", "runs", sch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxRuns {
		t.Fatalf("%d sidecar files, want %d", len(entries), maxRuns)
	}
	if _, err := svc.RunOutput("sch_missing", runID(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing schedule: %v", err)
	}
	if _, err := svc.RunOutput(sch.ID, "run_nope"); !errors.Is(err, ErrNoOutput) {
		t.Fatalf("missing run: %v", err)
	}
}

func TestDeleteRemovesRunOutputs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := openScript(t, dir, now)
	sch, err := svc.Create(scriptSpec("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(scriptSpec("other"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sch.ID, other.ID} {
		if _, err := svc.store.WriteRunOutput(id, "run_1", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Delete(sch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "schedules", "runs", sch.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar dir survived delete: %v", err)
	}
	if _, err := svc.store.ReadRunOutput(other.ID, "run_1"); err != nil {
		t.Fatalf("neighbour's sidecar removed: %v", err)
	}
	if err := svc.Delete(sch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestOpenStoreReconcilesOrphanOutputs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	svc := openScript(t, dir, now)
	sch, err := svc.Create(scriptSpec("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.Mutate(sch.ID, func(s *Schedule) error {
		addRun(s, Run{ID: "run_keep", ScheduleID: s.ID, FiredAt: now, Status: StatusFinished, OutputRef: "run_keep"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dir, "schedules", "runs")
	if _, err := svc.store.WriteRunOutput(sch.ID, "run_keep", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.WriteRunOutput(sch.ID, "run_orphan", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.WriteRunOutput("sch_gone", "run_1", []byte("gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.WriteRunOutput("sch_mixed", "run_1", []byte("gone")); err != nil {
		t.Fatal(err)
	}
	foreignInMixed := filepath.Join(runs, "sch_mixed", "notes.json")
	if err := os.WriteFile(foreignInMixed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	strayTmp := filepath.Join(runs, sch.ID, "run_half.log.tmp")
	if err := os.WriteFile(strayTmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignInLive := filepath.Join(runs, sch.ID, "README")
	if err := os.WriteFile(foreignInLive, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	strayFile := filepath.Join(runs, "junk")
	if err := os.WriteFile(strayFile, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	// ~/.strike/runs is the session snapshot root; the store must never look there.
	snapshot := filepath.Join(dir, "runs", "sess_abc", "root.json")
	if err := os.MkdirAll(filepath.Dir(snapshot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopen := openScript(t, dir, now)
	if _, err := reopen.RunOutput(sch.ID, "run_keep"); err != nil {
		t.Fatalf("live sidecar removed: %v", err)
	}
	for _, p := range []string{
		filepath.Join(runs, sch.ID, "run_orphan.log"),
		filepath.Join(runs, "sch_gone"),
		filepath.Join(runs, "sch_mixed", "run_1.log"),
		strayTmp,
	} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived reconcile: %v", p, err)
		}
	}
	for _, p := range []string{foreignInMixed, foreignInLive, strayFile, snapshot} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("reconcile removed a file it did not write, %s: %v", p, err)
		}
	}

	// A store with no schedules.json yet still sweeps a leftover sidecar
	// tree, and still leaves the snapshot root alone.
	fresh := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fresh, "schedules", "runs", "sch_x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, "schedules", "runs", "sch_x", "run_1.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	freshSnapshot := filepath.Join(fresh, "runs", "sess_x", "root.json")
	if err := os.MkdirAll(filepath.Dir(freshSnapshot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freshSnapshot, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "schedules", "runs", "sch_x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh store kept an orphan schedule dir: %v", err)
	}
	if _, err := os.Stat(freshSnapshot); err != nil {
		t.Fatalf("fresh store touched the snapshot root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "schedules.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconcile created schedules.json: %v", err)
	}
}
