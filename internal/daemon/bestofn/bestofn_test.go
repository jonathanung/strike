package bestofn

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeSpawner stands in for the team.spawn delegation boundary.
type fakeSpawner struct {
	specs     []SpawnSpec
	failModel map[string]bool
	cancelErr error
	cancelled []string
	n         int
}

func (f *fakeSpawner) Spawn(_ context.Context, spec SpawnSpec) (string, error) {
	f.specs = append(f.specs, spec)
	if f.failModel[spec.Model] {
		return "", errors.New("provider " + spec.Model + " not configured")
	}
	f.n++
	return fmt.Sprintf("child-%d", f.n), nil
}

func (f *fakeSpawner) Cancel(id string) error {
	f.cancelled = append(f.cancelled, id)
	return f.cancelErr
}

func fixedClock() func() time.Time {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

func req3(edits bool) Request {
	return Request{
		Prompt:         "fix the flaky test",
		Candidates:     []Candidate{{Model: "a"}, {Model: "b", Effort: "high"}, {Model: "c"}},
		Edits:          edits,
		PermissionMode: "ask",
		Budget:         Budget{MaxTurns: 20, Timeout: 10 * time.Minute},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Request)
		wantErr string
	}{
		{name: "ok", mut: func(*Request) {}},
		{name: "empty prompt", mut: func(r *Request) { r.Prompt = "  " }, wantErr: "prompt"},
		{name: "one model", mut: func(r *Request) { r.Candidates = r.Candidates[:1] }, wantErr: "pick 2–4"},
		{name: "five models", mut: func(r *Request) {
			r.Candidates = append(r.Candidates, Candidate{Model: "d"}, Candidate{Model: "e"})
		}, wantErr: "pick 2–4"},
		{name: "blank model", mut: func(r *Request) { r.Candidates[1].Model = "" }, wantErr: "no model"},
		{name: "duplicate", mut: func(r *Request) { r.Candidates[2] = Candidate{Model: "b", Effort: "high"} }, wantErr: "listed twice"},
		{name: "same model different effort ok", mut: func(r *Request) { r.Candidates[2] = Candidate{Model: "b", Effort: "low"} }},
		{name: "negative budget", mut: func(r *Request) { r.Budget.MaxTokens = -1 }, wantErr: "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := req3(true)
			tt.mut(&r)
			err := r.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpecs(t *testing.T) {
	tests := []struct {
		edits bool
		iso   string
	}{{true, IsolationWorktree}, {false, IsolationShared}}
	for _, tt := range tests {
		r := req3(tt.edits)
		specs := r.Specs()
		if len(specs) != 3 || r.CostMultiplier() != 3 {
			t.Fatalf("specs = %d, multiplier = %d", len(specs), r.CostMultiplier())
		}
		for i, s := range specs {
			if s.Isolation != tt.iso || s.PermissionMode != "ask" || s.Objective != r.Prompt ||
				s.Model != r.Candidates[i].Model || s.Effort != r.Candidates[i].Effort || s.Budget != r.Budget {
				t.Fatalf("spec %d = %+v", i, s)
			}
		}
		if specs[0].Name == specs[1].Name {
			t.Fatal("spec names must be distinct")
		}
	}
}

func TestStartRejectsInvalid(t *testing.T) {
	sp := &fakeSpawner{}
	if _, err := Start(context.Background(), sp, Request{}); err == nil {
		t.Fatal("invalid request should fail")
	}
	if len(sp.specs) != 0 {
		t.Fatal("nothing should spawn for an invalid request")
	}
	if _, err := Start(context.Background(), nil, req3(true)); err == nil {
		t.Fatal("nil spawner should fail")
	}
}

func TestHappyPathAdopt(t *testing.T) {
	sp := &fakeSpawner{}
	r, err := start(context.Background(), sp, req3(true), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete() {
		t.Fatal("fresh run should not be complete")
	}
	if _, err := r.Adopt("child-1"); err == nil {
		t.Fatal("adopt while running should fail")
	}
	ok := true
	for i, id := range []string{"child-1", "child-2", "child-3"} {
		out := Outcome{Summary: "done " + id, FilesChanged: i + 1, Added: 10, Removed: 2, Verified: &ok}
		if err := r.Finish(id, out); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Finish("child-1", Outcome{}); err == nil {
		t.Fatal("finishing twice should fail")
	}
	if !r.Complete() {
		t.Fatal("run should be complete")
	}
	d, err := r.Adopt("child-2")
	if err != nil {
		t.Fatal(err)
	}
	want := Decision{Keep: "child-2", Archive: []string{"child-1", "child-3"}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("decision = %+v", d)
	}
	if _, err := r.KeepNone(); err == nil {
		t.Fatal("second decision should fail")
	}
	if got, ok := r.Decision(); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decision() = %+v %v", got, ok)
	}
}

func TestFailedAndCancelledCompleteHonestly(t *testing.T) {
	sp := &fakeSpawner{failModel: map[string]bool{"c": true}}
	r, err := start(context.Background(), sp, req3(true), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	cands := r.Candidates()
	if cands[2].Status != StatusFailed || cands[2].ChildID != "" || !strings.Contains(cands[2].Outcome.Err, "not configured") {
		t.Fatalf("spawn failure not recorded: %+v", cands[2])
	}
	// child-1 is cancelled mid-flight; child-2 finishes.
	if err := r.Cancel("child-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Cancel("child-1"); err == nil {
		t.Fatal("cancelling twice should fail")
	}
	if err := r.Finish("child-2", Outcome{Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	if !r.Complete() {
		t.Fatal("run should be complete with remaining candidates")
	}
	if _, err := r.Adopt("child-1"); err == nil {
		t.Fatal("adopting a cancelled candidate should fail")
	}
	d, err := r.Adopt("child-2")
	if err != nil {
		t.Fatal(err)
	}
	// The never-spawned candidate has no child to archive.
	if !reflect.DeepEqual(d, Decision{Keep: "child-2", Archive: []string{"child-1"}}) {
		t.Fatalf("decision = %+v", d)
	}
}

func TestFinishWithErrMarksFailed(t *testing.T) {
	r, err := start(context.Background(), &fakeSpawner{}, req3(false), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish("child-1", Outcome{Err: "turn budget exhausted"}); err != nil {
		t.Fatal(err)
	}
	if got := r.Candidates()[0].Status; got != StatusFailed {
		t.Fatalf("status = %s", got)
	}
	if err := r.Finish("nope", Outcome{}); err == nil {
		t.Fatal("unknown child should fail")
	}
	if err := r.Finish("", Outcome{}); err == nil {
		t.Fatal("empty child id should fail")
	}
}

func TestCancelAllAndKeepNone(t *testing.T) {
	sp := &fakeSpawner{cancelErr: errors.New("child gone")}
	r, err := start(context.Background(), sp, req3(true), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish("child-3", Outcome{Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	err = r.CancelAll()
	if err == nil || !strings.Contains(err.Error(), "child gone") {
		t.Fatalf("CancelAll err = %v", err)
	}
	if !reflect.DeepEqual(sp.cancelled, []string{"child-1", "child-2"}) {
		t.Fatalf("cancelled = %v (finished child must not be interrupted)", sp.cancelled)
	}
	for _, c := range r.Candidates()[:2] {
		if c.Status != StatusCancelled {
			t.Fatalf("candidate %s status = %s, want cancelled even when interrupt errs", c.ChildID, c.Status)
		}
	}
	d, err := r.KeepNone()
	if err != nil {
		t.Fatal(err)
	}
	if d.Keep != "" || !reflect.DeepEqual(d.Archive, []string{"child-1", "child-2", "child-3"}) {
		t.Fatalf("decision = %+v", d)
	}
}

func TestCandidatesReturnsCopies(t *testing.T) {
	r, err := start(context.Background(), &fakeSpawner{}, req3(false), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	v := true
	if err := r.Finish("child-1", Outcome{Verified: &v}); err != nil {
		t.Fatal(err)
	}
	c := r.Candidates()
	*c[0].Outcome.Verified = false
	c[1].Status = StatusDone
	again := r.Candidates()
	if !*again[0].Outcome.Verified || again[1].Status != StatusRunning {
		t.Fatal("Candidates leaked internal state")
	}
	rq := r.Request()
	rq.Candidates[0].Model = "mutated"
	if r.Request().Candidates[0].Model != "a" {
		t.Fatal("Request leaked internal state")
	}
}

func TestCandidateLabel(t *testing.T) {
	if got := (Candidate{Model: "m"}).Label(); got != "m" {
		t.Fatalf("label = %q", got)
	}
	if got := (Candidate{Model: "m", Effort: "high"}).Label(); got != "m (high)" {
		t.Fatalf("label = %q", got)
	}
}

func TestDiffstat(t *testing.T) {
	tests := []struct {
		name                  string
		patch                 string
		files, added, removed int
	}{
		{name: "empty", patch: ""},
		{
			name: "one file",
			patch: "diff --git a/x.go b/x.go\nindex 1..2 100644\n--- a/x.go\n+++ b/x.go\n" +
				"@@ -1,3 +1,3 @@\n keep\n-old\n+new\n+extra\n",
			files: 1, added: 2, removed: 1,
		},
		{
			name: "content lines that look like headers count",
			patch: "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n---dashes\n+++plus\n" +
				"diff --git a/b b/b\nnew file mode 100644\n--- /dev/null\n+++ b/b\n@@ -0,0 +1 @@\n+hi\n\\ No newline at end of file\n",
			files: 2, added: 2, removed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, a, r := Diffstat(tt.patch)
			if f != tt.files || a != tt.added || r != tt.removed {
				t.Fatalf("Diffstat = %d/%d/%d, want %d/%d/%d", f, a, r, tt.files, tt.added, tt.removed)
			}
		})
	}
}

func TestCheckAdoptDoesNotDecide(t *testing.T) {
	r, err := start(context.Background(), &fakeSpawner{}, req3(true), fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckAdopt("child-1"); err == nil {
		t.Fatal("check while running should fail")
	}
	_ = r.Finish("child-1", Outcome{Summary: "ok"})
	_ = r.Finish("child-2", Outcome{Err: "boom"})
	_ = r.Cancel("child-3")
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"child-1", false},
		{"child-2", true},
		{"child-3", true},
		{"nope", true},
		{"", true},
	}
	for _, tt := range tests {
		if err := r.CheckAdopt(tt.id); (err != nil) != tt.wantErr {
			t.Fatalf("CheckAdopt(%q) err = %v, wantErr %v", tt.id, err, tt.wantErr)
		}
	}
	if _, ok := r.Decision(); ok {
		t.Fatal("CheckAdopt must not record a decision")
	}
	if _, err := r.Adopt("child-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckAdopt("child-1"); err == nil {
		t.Fatal("check after a decision should fail")
	}
}
