package spec

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "specs")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic, strictly increasing clock so List ordering is stable.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	st.now = func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Second)
	}
	return st, dir
}

func mustCreate(t *testing.T, st *Store, name string) Spec {
	t.Helper()
	sp, err := st.Create(name, "Feature "+name, "desc")
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func TestCreateValidatesNameAndRejectsDuplicates(t *testing.T) {
	st, dir := openStore(t)
	for _, bad := range []string{"", "Upper", "../x", "-lead", strings.Repeat("a", 65)} {
		if _, err := st.Create(bad, "t", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Create(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
	sp := mustCreate(t, st, "login")
	if sp.Version != 1 {
		t.Fatalf("version = %d", sp.Version)
	}
	for _, p := range Phases {
		if sp.Status[p] != StatusDraft {
			t.Fatalf("phase %s = %s", p, sp.Status[p])
		}
	}
	if _, err := st.Create("login", "again", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	for _, f := range []string{"spec.json", "requirements.md", "design.md", "tasks.md"} {
		if _, err := os.Stat(filepath.Join(dir, "login", f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
}

func TestPhaseGates(t *testing.T) {
	st, _ := openStore(t)
	sp := mustCreate(t, st, "gates")

	// Design cannot be approved before requirements.
	if _, err := st.SetPhaseStatus("gates", PhaseDesign, StatusApproved, sp.Version); !errors.Is(err, ErrGate) {
		t.Fatalf("design before requirements: %v", err)
	}
	// Requirements cannot be approved while empty.
	if _, err := st.SetPhaseStatus("gates", PhaseRequirements, StatusApproved, sp.Version); !errors.Is(err, ErrGate) {
		t.Fatalf("empty requirements approve: %v", err)
	}
	sp, err := st.SaveRequirements("gates", []Requirement{
		{Story: "As a user I sign in", Criteria: []string{"WHEN the password is wrong THE SYSTEM SHALL show an error"}},
		{Story: "As a user I sign out", Criteria: []string{"sign out works"}},
	}, sp.Version)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Requirements[0].ID != "R1" || sp.Requirements[1].ID != "R2" {
		t.Fatalf("ids = %s %s", sp.Requirements[0].ID, sp.Requirements[1].ID)
	}
	steps := []struct {
		phase, status string
		wantErr       error
	}{
		{PhaseRequirements, StatusReview, nil},
		{PhaseRequirements, StatusApproved, nil},
		{PhaseTasks, StatusApproved, ErrGate}, // design not approved
		{PhaseDesign, StatusApproved, nil},
		{PhaseTasks, StatusApproved, ErrGate}, // no tasks yet
		{"bogus", StatusApproved, ErrInvalid},
		{PhaseDesign, "done", ErrInvalid},
	}
	for _, step := range steps {
		next, err := st.SetPhaseStatus("gates", step.phase, step.status, sp.Version)
		if step.wantErr != nil {
			if !errors.Is(err, step.wantErr) {
				t.Fatalf("%s→%s err = %v, want %v", step.phase, step.status, err, step.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s→%s: %v", step.phase, step.status, err)
		}
		sp = next
	}
	if sp.Status[PhaseDesign] != StatusApproved {
		t.Fatalf("design = %s", sp.Status[PhaseDesign])
	}
	// Editing requirements resets requirements, design and tasks to draft.
	sp, err = st.SaveRequirements("gates", sp.Requirements, sp.Version)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Phases {
		if sp.Status[p] != StatusDraft {
			t.Fatalf("after edit %s = %s", p, sp.Status[p])
		}
	}
}

func TestStaleVersionConflicts(t *testing.T) {
	st, _ := openStore(t)
	sp := mustCreate(t, st, "cas")
	if _, err := st.SaveDesign("cas", "x", sp.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveDesign("cas", "y", sp.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write err = %v", err)
	}
	if _, err := st.SaveDesign("missing", "y", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func approvedSpec(t *testing.T, st *Store, name string, tasks []Task) Spec {
	t.Helper()
	sp := mustCreate(t, st, name)
	var err error
	if sp, err = st.SaveRequirements(name, []Requirement{
		{Story: "story one", Criteria: []string{"THE SYSTEM SHALL do one"}},
		{Story: "story two", Criteria: []string{"WHEN two THE SYSTEM SHALL do two"}},
	}, sp.Version); err != nil {
		t.Fatal(err)
	}
	if sp, err = st.SaveDesign(name, "## Design\nboxes and arrows", sp.Version); err != nil {
		t.Fatal(err)
	}
	if sp, err = st.SaveTasks(name, tasks, sp.Version); err != nil {
		t.Fatal(err)
	}
	for _, p := range Phases {
		if sp, err = st.SetPhaseStatus(name, p, StatusApproved, sp.Version); err != nil {
			t.Fatalf("approve %s: %v", p, err)
		}
	}
	return sp
}

func TestHandOffRefusesUncoveredAndHandsOffOnce(t *testing.T) {
	st, _ := openStore(t)
	sp := approvedSpec(t, st, "partial", []Task{{Title: "only one", Requirements: []string{"R1"}}})
	calls := 0
	create := PlannerFuncs{CreateFn: func(title string, secs []PlanSection) (string, error) {
		calls++
		return "plan-1", nil
	}}
	if _, err := st.HandOff("partial", sp.Version, create); !errors.Is(err, ErrGate) || !strings.Contains(err.Error(), "R2") {
		t.Fatalf("uncovered hand-off err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("create called on refused hand-off")
	}

	full := approvedSpec(t, st, "full", []Task{
		{Title: "build one", Body: "do it", Requirements: []string{"R1"}},
		{Title: "build two", Requirements: []string{"R2", "R1"}},
	})
	var gotTitle string
	var gotSecs []PlanSection
	done, err := st.HandOff("full", full.Version, PlannerFuncs{CreateFn: func(title string, secs []PlanSection) (string, error) {
		gotTitle, gotSecs = title, secs
		return "plan-9", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if done.PlanID != "plan-9" || done.HandedOffAt == nil {
		t.Fatalf("hand-off not recorded: %+v", done)
	}
	if gotTitle != "Feature full" || len(gotSecs) != 2 {
		t.Fatalf("plan = %q %d sections", gotTitle, len(gotSecs))
	}
	if gotSecs[0].Title != "T1 build one" || !strings.Contains(gotSecs[0].Body, "R1: story one") || !strings.Contains(gotSecs[0].Body, "do it") {
		t.Fatalf("section body lacks citation: %+v", gotSecs[0])
	}
	// Read-only afterwards.
	if _, err := st.SaveDesign("full", "changed", done.Version); !errors.Is(err, ErrHandedOff) {
		t.Fatalf("edit after hand-off err = %v", err)
	}
	if _, err := st.HandOff("full", done.Version, create); !errors.Is(err, ErrHandedOff) {
		t.Fatalf("second hand-off err = %v", err)
	}
}

func TestHandOffCreateErrorLeavesSpecEditable(t *testing.T) {
	st, _ := openStore(t)
	sp := approvedSpec(t, st, "fail", []Task{{Title: "t", Requirements: []string{"R1", "R2"}}})
	boom := errors.New("plans unavailable")
	if _, err := st.HandOff("fail", sp.Version, PlannerFuncs{CreateFn: func(string, []PlanSection) (string, error) { return "", boom }}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	got, err := st.Get("fail")
	if err != nil {
		t.Fatal(err)
	}
	if got.HandedOffAt != nil || got.Version != sp.Version {
		t.Fatalf("failed hand-off mutated spec: %+v", got)
	}
}

// fakePlanner records created plans like a plan store would.
type fakePlanner struct {
	plans []fakePlan
	now   func() time.Time
}

type fakePlan struct {
	id, title string
	created   time.Time
}

func (f *fakePlanner) Create(title string, _ []PlanSection) (string, error) {
	id := "plan-" + strconv.Itoa(len(f.plans)+1)
	f.plans = append(f.plans, fakePlan{id: id, title: title, created: f.now()})
	return id, nil
}

func (f *fakePlanner) Find(title string, since time.Time) (string, error) {
	for _, p := range f.plans {
		if p.title == title && !p.created.Before(since) {
			return p.id, nil
		}
	}
	return "", nil
}

// TestHandOffRetryAfterSpecWriteFailureCreatesOnePlan covers #1911: the
// plan exists but the spec write fails; a retry must reuse that plan.
func TestHandOffRetryAfterSpecWriteFailureCreatesOnePlan(t *testing.T) {
	tests := []struct {
		name string
		// loseJournalID simulates a crash before the plan id was journaled,
		// so the retry has to find the plan by title and start time.
		loseJournalID bool
	}{
		{name: "journaled plan id"},
		{name: "intent only, plan found", loseJournalID: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, dir := openStore(t)
			sp := approvedSpec(t, st, "retry", []Task{{Title: "t", Requirements: []string{"R1", "R2"}}})
			planner := &fakePlanner{now: st.now}
			boom := errors.New("disk full")
			st.beforeSave = func(s Spec) error {
				if s.HandedOffAt != nil {
					return boom
				}
				return nil
			}
			if _, err := st.HandOff("retry", sp.Version, planner); !errors.Is(err, boom) {
				t.Fatalf("first hand-off err = %v", err)
			}
			if len(planner.plans) != 1 {
				t.Fatalf("plans after failed write = %d", len(planner.plans))
			}
			if tc.loseJournalID {
				j, ok := st.readJournal("retry")
				if !ok || j.PlanID == "" {
					t.Fatalf("journal = %+v ok=%v", j, ok)
				}
				j.PlanID = ""
				if err := st.writeJournal("retry", j); err != nil {
					t.Fatal(err)
				}
			}
			st.beforeSave = nil
			done, err := st.HandOff("retry", sp.Version, planner)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if len(planner.plans) != 1 {
				t.Fatalf("retry created another plan: %+v", planner.plans)
			}
			if done.PlanID != "plan-1" || done.HandedOffAt == nil {
				t.Fatalf("retry recorded %+v", done)
			}
			if _, err := os.Stat(filepath.Join(dir, "retry", "handoff.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal left after hand-off: %v", err)
			}
		})
	}
}

func TestHandOffJournalVoidedByEdit(t *testing.T) {
	st, _ := openStore(t)
	sp := approvedSpec(t, st, "edited", []Task{{Title: "t", Requirements: []string{"R1", "R2"}}})
	planner := &fakePlanner{now: st.now}
	boom := errors.New("disk full")
	st.beforeSave = func(s Spec) error {
		if s.HandedOffAt != nil {
			return boom
		}
		return nil
	}
	if _, err := st.HandOff("edited", sp.Version, planner); !errors.Is(err, boom) {
		t.Fatalf("first hand-off err = %v", err)
	}
	st.beforeSave = nil
	// The user edits tasks and re-approves: the old plan no longer matches.
	sp, err := st.SaveTasks("edited", []Task{{Title: "new", Requirements: []string{"R1", "R2"}}}, sp.Version)
	if err != nil {
		t.Fatal(err)
	}
	if sp, err = st.SetPhaseStatus("edited", PhaseTasks, StatusApproved, sp.Version); err != nil {
		t.Fatal(err)
	}
	done, err := st.HandOff("edited", sp.Version, planner)
	if err != nil {
		t.Fatal(err)
	}
	if len(planner.plans) != 2 || done.PlanID != "plan-2" {
		t.Fatalf("edited spec reused stale plan: %+v / %s", planner.plans, done.PlanID)
	}
}

func TestCoverageAndLint(t *testing.T) {
	sp := Spec{
		Requirements: []Requirement{
			{ID: "R1", Story: "a", Criteria: []string{"WHEN x THE SYSTEM SHALL y"}},
			{ID: "R2", Story: "b", Criteria: []string{"it works"}},
			{ID: "R3", Story: "c"},
		},
		Tasks: []Task{{ID: "T1", Title: "t", Requirements: []string{"R1", "R9"}}},
	}
	cov := ComputeCoverage(sp)
	if cov.Covered != 1 || cov.Total != 3 || strings.Join(cov.Uncovered, ",") != "R2,R3" || strings.Join(cov.UnknownRefs, ",") != "R9" {
		t.Fatalf("coverage = %+v", cov)
	}
	if cov.Complete() {
		t.Fatal("incomplete coverage reported complete")
	}
	lint := Lint(sp)
	if _, ok := lint["R1#0"]; ok {
		t.Fatalf("valid EARS flagged: %v", lint["R1#0"])
	}
	if len(lint["R2#0"]) != 2 {
		t.Fatalf("R2 lint = %v", lint["R2#0"])
	}
	if len(lint["R3"]) != 1 {
		t.Fatalf("R3 lint = %v", lint["R3"])
	}
	cases := []struct {
		in   string
		want int
	}{
		{"WHILE offline THE SYSTEM SHALL queue writes", 0},
		{"IF the token expires THEN the system shall refresh it", 0},
		{"The system shall log every request", 0},
		{"when idle, shall sleep", 0},
		{"Users can export data", 2},
		{"", 1},
	}
	for _, c := range cases {
		if got := LintEARS(c.in); len(got) != c.want {
			t.Fatalf("LintEARS(%q) = %v, want %d warnings", c.in, got, c.want)
		}
	}
}

func TestNormalizeRejectsDuplicatesAndBlanks(t *testing.T) {
	if _, err := normalizeRequirements([]Requirement{{ID: "R1", Story: "a"}, {ID: "R1", Story: "b"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dup req err = %v", err)
	}
	if _, err := normalizeRequirements([]Requirement{{Story: "  "}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank story err = %v", err)
	}
	got, err := normalizeRequirements([]Requirement{{ID: "R1", Story: "a"}, {Story: "b"}})
	if err != nil || got[1].ID != "R2" {
		t.Fatalf("auto id = %+v %v", got, err)
	}
	if _, err := normalizeTasks([]Task{{Title: ""}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank task err = %v", err)
	}
	tasks, err := normalizeTasks([]Task{{Title: "x", Requirements: []string{" R1 ", "R1", ""}}})
	if err != nil || tasks[0].ID != "T1" || len(tasks[0].Requirements) != 1 {
		t.Fatalf("task norm = %+v %v", tasks, err)
	}
}

func TestListNewestFirst(t *testing.T) {
	st, _ := openStore(t)
	empty, err := st.List()
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list = %v %v", empty, err)
	}
	a := mustCreate(t, st, "a")
	mustCreate(t, st, "b")
	if _, err := st.SaveDesign("a", "touch", a.Version); err != nil {
		t.Fatal(err)
	}
	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "a" {
		t.Fatalf("list = %+v", list)
	}
}
