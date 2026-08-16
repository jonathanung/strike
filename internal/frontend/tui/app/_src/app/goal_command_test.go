package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

type fakeGoals struct {
	goals map[string]host.Goal
	order []string
	runs  int
}

func newFakeGoals(gs ...host.Goal) *fakeGoals {
	f := &fakeGoals{goals: make(map[string]host.Goal)}
	for _, g := range gs {
		f.goals[g.ID] = g
		f.order = append(f.order, g.ID)
	}
	return f
}

func (f *fakeGoals) Set(description string, criteria []string, opts host.GoalSetOptions) (host.Goal, error) {
	if description == "" || len(criteria) == 0 {
		return host.Goal{}, goalFakeErr("validation")
	}
	id := "g" + time.Now().Format("150405.000")
	g := host.Goal{
		ID:            id,
		Description:   description,
		Status:        "pending",
		MaxIterations: opts.MaxIterations,
		MaxCostUSD:    opts.MaxCostUSD,
		AllowedTools:  append([]string(nil), opts.AllowedTools...),
		CreatedAt:     time.Now().UTC(),
	}
	for _, c := range criteria {
		g.Criteria = append(g.Criteria, host.GoalCriterion{Description: c, Check: c})
	}
	f.goals[id] = g
	f.order = append([]string{id}, f.order...)
	return g, nil
}

type goalFakeErr string

func (e goalFakeErr) Error() string { return string(e) }

func (f *fakeGoals) List() ([]host.Goal, error) {
	out := make([]host.Goal, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.goals[id])
	}
	return out, nil
}

func (f *fakeGoals) Get(id string) (host.Goal, bool, error) {
	g, ok := f.goals[id]
	return g, ok, nil
}

func (f *fakeGoals) Run(ctx context.Context, id string) (host.Goal, error) {
	g, ok := f.goals[id]
	if !ok {
		return host.Goal{}, goalFakeErr("not found")
	}
	f.runs++
	g.Status = "done"
	g.LastIteration = 1
	if len(g.Criteria) > 0 {
		g.Criteria[0].Satisfied = true
	}
	f.goals[id] = g
	return g, nil
}

func (f *fakeGoals) Pause(id string) (host.Goal, error) {
	g, ok := f.goals[id]
	if !ok {
		return host.Goal{}, goalFakeErr("not found")
	}
	g.Status = "paused"
	f.goals[id] = g
	return g, nil
}

func (f *fakeGoals) Resume(id string) (host.Goal, error) {
	g, ok := f.goals[id]
	if !ok {
		return host.Goal{}, goalFakeErr("not found")
	}
	g.Status = "active"
	f.goals[id] = g
	return g, nil
}

func (f *fakeGoals) Abort(id string) (host.Goal, error) {
	g, ok := f.goals[id]
	if !ok {
		return host.Goal{}, goalFakeErr("not found")
	}
	g.Status = "aborted"
	f.goals[id] = g
	return g, nil
}

func (f *fakeGoals) Log(id string, iter int) ([]host.GoalIteration, error) {
	if _, ok := f.goals[id]; !ok {
		return nil, goalFakeErr("not found")
	}
	return []host.GoalIteration{{N: 1, Summary: "iter 1 plan=\"evaluate-only\""}}, nil
}

func TestParseGoalSetArgs(t *testing.T) {
	t.Parallel()
	desc, crit, opts, err := parseGoalSetArgs([]string{
		"ship", "feature",
		"--criterion", "cmd: true",
		"--criterion", "cmd: false",
		"--max-iter", "5",
		"--budget-usd", "1.5",
		"--tools", "bash,read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if desc != "ship feature" {
		t.Fatalf("desc=%q", desc)
	}
	if len(crit) != 2 || crit[0] != "cmd: true" {
		t.Fatalf("crit=%v", crit)
	}
	if opts.MaxIterations != 5 || opts.MaxCostUSD != 1.5 {
		t.Fatalf("opts=%+v", opts)
	}
	if len(opts.AllowedTools) != 2 {
		t.Fatalf("tools=%v", opts.AllowedTools)
	}
	if _, _, _, err := parseGoalSetArgs([]string{"only desc"}); err == nil {
		t.Fatal("expected criterion required")
	}
}

func TestGoalSlashCommands(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fg := newFakeGoals()
	m.services.Goals = fg

	next, _ := m.handleCommand(`/goal set tests green --criterion cmd: true`)
	m = next.(Model)
	if !strings.Contains(m.notice, "pending") {
		t.Fatalf("notice=%q", m.notice)
	}
	if len(fg.goals) != 1 {
		t.Fatalf("goals=%d", len(fg.goals))
	}

	next, _ = m.handleCommand("/goal list")
	m = next.(Model)
	if !strings.Contains(m.notice, "pending") {
		t.Fatalf("list notice=%q", m.notice)
	}

	next, _ = m.handleCommand("/goal status")
	m = next.(Model)
	if !strings.Contains(m.notice, "iter=") {
		t.Fatalf("status=%q", m.notice)
	}

	next, cmd := m.handleCommand("/goal run")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("run should return async cmd")
	}
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(goalFinishedMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if finished.err != nil || finished.goal.Status != "done" {
		t.Fatalf("finished=%+v", finished)
	}
	next, _ = m.Update(finished)
	m = next.(Model)
	if !strings.Contains(m.notice, "done") {
		t.Fatalf("after run notice=%q", m.notice)
	}

	// recreate pending for pause path
	fg2 := newFakeGoals(host.Goal{ID: "x1", Description: "d", Status: "active"})
	m.services.Goals = fg2
	next, _ = m.handleCommand("/goal pause x1")
	m = next.(Model)
	if !strings.Contains(m.notice, "paused") {
		t.Fatalf("pause=%q", m.notice)
	}
	next, _ = m.handleCommand("/goal resume x1")
	m = next.(Model)
	if !strings.Contains(m.notice, "resumed") {
		t.Fatalf("resume=%q", m.notice)
	}
	next, _ = m.handleCommand("/goal abort x1")
	m = next.(Model)
	if !strings.Contains(m.notice, "aborted") {
		t.Fatalf("abort=%q", m.notice)
	}
	next, _ = m.handleCommand("/goal log x1")
	m = next.(Model)
	if !strings.Contains(m.notice, "iter 1") {
		t.Fatalf("log=%q", m.notice)
	}
}

func TestGoalCommandNilService(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Goals = nil
	next, _ := m.handleCommand("/goal list")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("notice=%q err=%v", m.notice, m.noticeErr)
	}
}

func TestGoalSetRejectsEmptyCriteria(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Goals = newFakeGoals()
	next, _ := m.handleCommand(`/goal set "vague idea"`)
	m = next.(Model)
	if !m.noticeErr {
		t.Fatalf("expected error notice, got %q", m.notice)
	}
}
