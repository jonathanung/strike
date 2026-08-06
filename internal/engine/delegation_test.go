package engine

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestDelegationCreateQueuedOnUnmetDeps(t *testing.T) {
	tm := NewTeam("L", "build")
	up, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "upstream",
		OwnerSessionID: "L",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force upstream not done (queued with spawn pending).
	if up.State != protocol.DelegationQueued {
		t.Fatalf("upstream state = %s", up.State)
	}
	// Clear spawn so it stays queued without a session.
	tm.ClearSpawnPending(up.ID)

	dep, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "downstream",
		OwnerSessionID: "L",
		Deps:           []string{up.ID},
		Criteria:       []string{"tests pass"},
		Subscribe:      []string{"done", "blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dep.State != protocol.DelegationQueued {
		t.Fatalf("dep state = %s, want queued", dep.State)
	}
	if dep.SpawnPending {
		t.Fatal("dependent should not be spawn-pending while upstream unmet")
	}
	if len(dep.Criteria) != 1 || dep.Criteria[0] != "tests pass" {
		t.Fatalf("criteria = %#v", dep.Criteria)
	}
	if len(dep.Subscribe) != 2 {
		t.Fatalf("subscribe = %#v", dep.Subscribe)
	}
}

func TestDelegationDepsReleaseOnDone(t *testing.T) {
	tm := NewTeam("L", "build")
	up, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "upstream",
		OwnerSessionID: "L",
		SessionID:      "child-up",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	down, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "downstream",
		OwnerSessionID: "L",
		Deps:           []string{up.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if down.SpawnPending {
		t.Fatal("should wait on upstream")
	}

	// Complete upstream via child bind.
	done, ok := tm.BindDelegationOnChildCompleted("child-up", protocol.ChildStatusCompleted)
	if !ok || done.State != protocol.DelegationDone {
		t.Fatalf("upstream bind = %+v ok=%v", done, ok)
	}
	// Dependent should now be spawn-pending.
	got, ok := tm.GetDelegation(down.ID)
	if !ok || !got.SpawnPending {
		t.Fatalf("dependent after upstream done = %+v", got)
	}
	pending := tm.TakeSpawnPending("L")
	if len(pending) != 1 || pending[0].ID != down.ID {
		t.Fatalf("TakeSpawnPending = %+v", pending)
	}
}

func TestDelegationFailedDepBlocksDependent(t *testing.T) {
	tm := NewTeam("L", "build")
	up, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "upstream",
		OwnerSessionID: "L",
		SessionID:      "u1",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	down, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "down",
		OwnerSessionID: "L",
		Deps:           []string{up.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, ok := tm.BindDelegationOnChildCompleted("u1", protocol.ChildStatusFailed)
	if !ok {
		t.Fatal("bind failed")
	}
	got, ok := tm.GetDelegation(down.ID)
	if !ok || got.State != protocol.DelegationBlocked {
		t.Fatalf("dependent = %+v", got)
	}
	if got.BlockReason == "" {
		t.Fatal("expected block reason")
	}
}

func TestDelegationTransitionRules(t *testing.T) {
	tm := NewTeam("L", "build")
	d, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "work",
		OwnerSessionID: "L",
		SessionID:      "s1",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Illegal: working → queued
	_, err = tm.TransitionDelegation(d.ID, "L", protocol.DelegationQueued, "", 0)
	var tr *DelegationTransitionError
	if !errors.As(err, &tr) {
		t.Fatalf("want transition error, got %v", err)
	}
	// Legal: working → blocked
	blocked, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationBlocked, "waiting on user", d.Version)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != protocol.DelegationBlocked || blocked.BlockReason != "waiting on user" {
		t.Fatalf("blocked = %+v", blocked)
	}
	// CAS conflict
	_, err = tm.TransitionDelegation(d.ID, "L", protocol.DelegationWorking, "", blocked.Version-1)
	var conf *DelegationConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("want CAS conflict, got %v", err)
	}
	// working again
	working, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationWorking, "", blocked.Version)
	if err != nil || working.State != protocol.DelegationWorking {
		t.Fatalf("working = %+v err=%v", working, err)
	}
	// review → done
	rev, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationReview, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	done, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationDone, "", rev.Version)
	if err != nil || done.State != protocol.DelegationDone {
		t.Fatalf("done = %+v err=%v", done, err)
	}
	// terminal stuck
	_, err = tm.TransitionDelegation(d.ID, "L", protocol.DelegationWorking, "", 0)
	if !errors.As(err, &tr) {
		t.Fatalf("terminal transition err = %v", err)
	}
}

func TestDelegationCriteriaCompletionGoesToReview(t *testing.T) {
	tm := NewTeam("L", "build")
	_, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "impl",
		OwnerSessionID: "L",
		SessionID:      "c1",
		StartState:     protocol.DelegationWorking,
		Criteria:       []string{"make test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := tm.BindDelegationOnChildCompleted("c1", protocol.ChildStatusCompleted)
	if !ok || d.State != protocol.DelegationReview {
		t.Fatalf("got %+v", d)
	}
	// Without criteria → done
	_, err = tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "simple",
		OwnerSessionID: "L",
		SessionID:      "c2",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, ok := tm.BindDelegationOnChildCompleted("c2", protocol.ChildStatusCompleted)
	if !ok || d2.State != protocol.DelegationDone {
		t.Fatalf("no-criteria = %+v", d2)
	}
}

func TestDelegationOwnershipBoundary(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll B")
	}
	if !tm.Enroll(TeamMember{SessionID: "childA", ParentSessionID: "A", Depth: 2}) {
		t.Fatal("enroll childA")
	}
	d, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "owned by A",
		OwnerSessionID: "A",
		SessionID:      "childA",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	// B cannot transition A's delegation.
	_, err = tm.TransitionDelegation(d.ID, "B", protocol.DelegationBlocked, "nope", 0)
	var conf *DelegationConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("foreign transition err = %v", err)
	}
	// Lead can.
	if _, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationBlocked, "lead", 0); err != nil {
		t.Fatal(err)
	}
	// Linked child can self-report blocked.
	d2, _ := tm.GetDelegation(d.ID)
	// Move back to working first via lead.
	if _, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationWorking, "", d2.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.TransitionDelegation(d.ID, "childA", protocol.DelegationBlocked, "need help", 0); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationConcurrentCAS(t *testing.T) {
	tm := NewTeam("L", "build")
	d, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "race",
		OwnerSessionID: "L",
		SessionID:      "s",
		StartState:     protocol.DelegationWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 32
	var wins atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := tm.TransitionDelegation(d.ID, "L", protocol.DelegationBlocked, "x", d.Version)
			if err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("CAS winners = %d, want 1", wins.Load())
	}
	got, _ := tm.GetDelegation(d.ID)
	if got.State != protocol.DelegationBlocked || got.Version != d.Version+1 {
		t.Fatalf("after race = %+v", got)
	}
}

func TestDelegationUnknownDepRejected(t *testing.T) {
	tm := NewTeam("L", "build")
	_, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt:         "x",
		OwnerSessionID: "L",
		Deps:           []string{"d999"},
	})
	if err == nil {
		t.Fatal("expected unknown dep error")
	}
}

func TestDelegationDissolveClears(t *testing.T) {
	tm := NewTeam("L", "build")
	if _, err := tm.CreateDelegation(CreateDelegationSpec{
		Prompt: "x", OwnerSessionID: "L",
	}); err != nil {
		t.Fatal(err)
	}
	if len(tm.Delegations()) != 1 {
		t.Fatal("expected 1")
	}
	tm.Dissolve()
	if len(tm.Delegations()) != 0 {
		t.Fatalf("after dissolve = %d", len(tm.Delegations()))
	}
}

func TestValidDelegationTransitionTable(t *testing.T) {
	cases := []struct {
		from, to protocol.DelegationState
		ok       bool
	}{
		{protocol.DelegationQueued, protocol.DelegationWorking, true},
		{protocol.DelegationQueued, protocol.DelegationDone, false},
		{protocol.DelegationWorking, protocol.DelegationReview, true},
		{protocol.DelegationWorking, protocol.DelegationDone, true},
		{protocol.DelegationReview, protocol.DelegationDone, true},
		{protocol.DelegationDone, protocol.DelegationWorking, false},
		{protocol.DelegationFailed, protocol.DelegationQueued, false},
		{protocol.DelegationBlocked, protocol.DelegationWorking, true},
	}
	for _, tc := range cases {
		got := ValidDelegationTransition(tc.from, tc.to)
		if got != tc.ok {
			t.Errorf("%s→%s = %v, want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestNormalizeSubscribeAndCriteria(t *testing.T) {
	subs, err := NormalizeSubscribeKinds([]string{" Done ", "blocked", "done"})
	if err != nil || len(subs) != 2 {
		t.Fatalf("subs = %v err=%v", subs, err)
	}
	_, err = NormalizeSubscribeKinds([]string{"nope"})
	if err == nil {
		t.Fatal("want bad kind error")
	}
	crit, err := NormalizeCriteria([]string{" a ", "", "b"})
	if err != nil || len(crit) != 2 {
		t.Fatalf("crit = %v err=%v", crit, err)
	}
	big := make([]string, MaxDelegationCriteria+1)
	for i := range big {
		big[i] = fmt.Sprintf("c%d", i)
	}
	if _, err := NormalizeCriteria(big); err == nil {
		t.Fatal("want criteria cap error")
	}
}
