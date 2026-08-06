package permission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestEvaluateLastMatchWins(t *testing.T) {
	base := Defaults()
	project := Ruleset{
		{Permission: "bash", Pattern: "git *", Action: Allow},
		{Permission: "bash", Pattern: "git push *", Action: Deny},
	}
	cases := []struct {
		perm, pattern string
		want          Action
	}{
		{"read", "any.go", Allow},
		{"bash", "ls", Ask},
		{"bash", "git status", Allow},
		{"bash", "git push origin main", Deny},
		{"edit", "x.go", Ask},
		{"task", "*", Allow},
		{"unknown", "*", Ask},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.perm, tc.pattern, base, project); got != tc.want {
			t.Errorf("Evaluate(%q,%q) = %q, want %q", tc.perm, tc.pattern, got, tc.want)
		}
	}
}

func TestDefaultsIncludesTaskAllow(t *testing.T) {
	if got := Evaluate("task", "*", Defaults()); got != Allow {
		t.Errorf("Defaults task = %q, want allow", got)
	}
}

func TestDefaultsIncludesAgentRosterAllow(t *testing.T) {
	if got := Evaluate("agent_roster", "*", Defaults()); got != Allow {
		t.Errorf("Defaults agent_roster = %q, want allow", got)
	}
}

func TestDefaultsIncludesTeamMessagingAllow(t *testing.T) {
	// In-team messaging defaults to allow so peers do not prompt every send.
	for _, perm := range []string{"agent_message", "agent_broadcast", "agent_roster", "agent_ownership", "team_task"} {
		if got := Evaluate(perm, "*", Defaults()); got != Allow {
			t.Errorf("Defaults %s = %q, want allow", perm, got)
		}
	}
}

func TestDefaultsIncludesPlanToolsAllow(t *testing.T) {
	// Independent of write/edit so plan mode can revise structured plans.
	for _, perm := range []string{"plan_write", "plan_read"} {
		if got := Evaluate(perm, "*", Defaults()); got != Allow {
			t.Errorf("Defaults %s = %q, want allow", perm, got)
		}
	}
	// Plan phase hard-denies write/edit without touching plan_*.
	phase := Ruleset{
		{Permission: "write", Pattern: "*", Action: Deny},
		{Permission: "edit", Pattern: "*", Action: Deny},
	}
	if got := Evaluate("plan_write", "*", Defaults(), phase); got != Allow {
		t.Errorf("plan_write under write/edit deny = %q, want allow", got)
	}
	if got := Evaluate("write", "x.go", Defaults(), phase); got != Deny {
		t.Errorf("write under phase deny = %q, want deny", got)
	}
}

func TestDenyRuleAgentMessageBlocks(t *testing.T) {
	// Config/agent deny must hard-block agent_message without prompting.
	base := Defaults()
	deny := Ruleset{{Permission: "agent_message", Pattern: "*", Action: Deny}}
	if got := Evaluate("agent_message", "*", base, deny); got != Deny {
		t.Fatalf("Evaluate agent_message with deny = %q, want deny", got)
	}
	// Sibling messaging tools stay allow unless also denied.
	if got := Evaluate("agent_broadcast", "*", base, deny); got != Allow {
		t.Errorf("agent_broadcast = %q, want allow", got)
	}
	if got := Evaluate("agent_roster", "*", base, deny); got != Allow {
		t.Errorf("agent_roster = %q, want allow", got)
	}

	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, base, deny)
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "agent_message",
		Patterns:   []string{"*"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Ask agent_message = %v, want DeniedError", err)
	}
	if len(events) != 0 {
		t.Errorf("deny must not emit permission events, got %#v", events)
	}
}

func TestDefaultsTeamMessagingNoPrompt(t *testing.T) {
	// Default allow: Ask succeeds with zero PermissionAsked events.
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Defaults())
	for _, perm := range []string{"agent_message", "agent_broadcast", "agent_roster", "agent_ownership"} {
		if err := svc.Ask(context.Background(), tool.AskRequest{
			Permission: perm,
			Patterns:   []string{"*"},
		}); err != nil {
			t.Fatalf("Ask %s: %v", perm, err)
		}
	}
	if len(events) != 0 {
		t.Errorf("default team messaging must not prompt, got %#v", events)
	}
}

func TestValidateRulesetAcceptsTeamMessaging(t *testing.T) {
	rs := Ruleset{
		{Permission: "agent_message", Pattern: "*", Action: Deny},
		{Permission: "agent_broadcast", Pattern: "*", Action: Allow},
		{Permission: "agent_roster", Pattern: "*", Action: Ask},
		{Permission: "agent_ownership", Pattern: "*", Action: Allow},
		{Permission: "team_task", Pattern: "*", Action: Allow},
	}
	if err := ValidateRuleset(rs); err != nil {
		t.Fatalf("ValidateRuleset team messaging: %v", err)
	}
}

func TestDenyOnly(t *testing.T) {
	in := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Allow},
		{Permission: "edit", Pattern: "*", Action: Deny},
		{Permission: "write", Pattern: "*", Action: Ask},
		{Permission: "read", Pattern: "secret/*", Action: Deny},
	}
	got := DenyOnly(in)
	want := Ruleset{
		{Permission: "edit", Pattern: "*", Action: Deny},
		{Permission: "read", Pattern: "secret/*", Action: Deny},
	}
	if len(got) != len(want) {
		t.Fatalf("DenyOnly len = %d, want %d; got %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DenyOnly[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
	// Defensive copy: mutating result must not touch input.
	if len(got) > 0 {
		got[0].Action = Allow
		if in[1].Action != Deny {
			t.Errorf("input mutated to %q", in[1].Action)
		}
	}
	if DenyOnly(nil) != nil && len(DenyOnly(nil)) != 0 {
		t.Errorf("DenyOnly(nil) = %#v, want nil/empty", DenyOnly(nil))
	}
}

func TestChildAgentRules(t *testing.T) {
	parentAsk := []Ruleset{Defaults()}
	general := Ruleset{
		{Permission: "bash", Pattern: "*", Action: Allow},
		{Permission: "task", Pattern: "*", Action: Deny},
	}
	got := ChildAgentRules(parentAsk, general)
	if !rulesetHasAction(got, "bash", Allow) {
		t.Fatalf("under parent Ask, bash allow kept: %+v", got)
	}
	if !rulesetHasAction(got, "task", Deny) {
		t.Fatalf("task deny kept: %+v", got)
	}
	if Evaluate("bash", "ls", append(parentAsk, got)...) != Allow {
		t.Fatalf("bash effective under general child = %q, want allow", Evaluate("bash", "ls", append(parentAsk, got)...))
	}

	parentDenyBash := []Ruleset{{
		{Permission: "bash", Pattern: "*", Action: Deny},
	}}
	got = ChildAgentRules(parentDenyBash, general)
	if rulesetHasAction(got, "bash", Allow) {
		t.Fatalf("under parent Deny, bash allow must drop: %+v", got)
	}
	if !rulesetHasAction(got, "task", Deny) {
		t.Fatalf("task deny still kept under parent bash deny: %+v", got)
	}
	if Evaluate("bash", "ls", append(parentDenyBash, got)...) != Deny {
		t.Fatalf("bash effective = %q, want deny", Evaluate("bash", "ls", append(parentDenyBash, got)...))
	}

	// Ask entries dropped.
	mixed := Ruleset{
		{Permission: "webfetch", Pattern: "*", Action: Ask},
		{Permission: "edit", Pattern: "*", Action: Deny},
	}
	got = ChildAgentRules(parentAsk, mixed)
	if rulesetHasAction(got, "webfetch", Ask) || rulesetHasAction(got, "webfetch", Allow) {
		t.Fatalf("Ask must drop: %+v", got)
	}
	if !rulesetHasAction(got, "edit", Deny) {
		t.Fatalf("edit deny kept: %+v", got)
	}
}

func rulesetHasAction(rs Ruleset, perm string, action Action) bool {
	for _, r := range rs {
		if r.Permission == perm && r.Action == action {
			return true
		}
	}
	return false
}

func TestDeriveChildRules(t *testing.T) {
	t.Run("parent deny beats childExtra allow", func(t *testing.T) {
		parent := []Ruleset{{{Permission: "bash", Pattern: "*", Action: Deny}}}
		childExtra := Ruleset{{Permission: "bash", Pattern: "*", Action: Allow}}
		derived := DeriveChildRules(parent, true, childExtra)
		if got := Evaluate("bash", "echo hi", derived...); got != Deny {
			t.Errorf("bash = %q, want deny (child cannot widen)", got)
		}
	})

	t.Run("parent ask upgraded by childExtra allow", func(t *testing.T) {
		// Ask→Allow is permitted so task subagents (e.g. general) can use
		// permission.bash: allow without prompting on every command.
		parent := []Ruleset{{{Permission: "bash", Pattern: "*", Action: Ask}}}
		childExtra := Ruleset{{Permission: "bash", Pattern: "*", Action: Allow}}
		derived := DeriveChildRules(parent, true, childExtra)
		if got := Evaluate("bash", "echo hi", derived...); got != Allow {
			t.Errorf("bash = %q, want allow (child may upgrade parent Ask)", got)
		}
	})

	t.Run("parent allow-after-deny preserved", func(t *testing.T) {
		parent := []Ruleset{{
			{Permission: "bash", Pattern: "*", Action: Deny},
			{Permission: "bash", Pattern: "git *", Action: Allow},
		}}
		derived := DeriveChildRules(parent, true)
		if got := Evaluate("bash", "git status", derived...); got != Allow {
			t.Errorf("git status = %q, want allow", got)
		}
		if got := Evaluate("bash", "rm -rf /", derived...); got != Deny {
			t.Errorf("rm = %q, want deny", got)
		}
	})

	t.Run("denies task when denyTask", func(t *testing.T) {
		parent := []Ruleset{Defaults()}
		derived := DeriveChildRules(parent, true)
		if got := Evaluate("task", "*", derived...); got != Deny {
			t.Errorf("task = %q, want deny", got)
		}
		// Parent defaults still allow task.
		if got := Evaluate("task", "*", parent...); got != Allow {
			t.Errorf("parent task = %q, want allow", got)
		}
	})

	t.Run("keeps team messaging when denyTask", func(t *testing.T) {
		// Depth ceiling denies nested task spawn only; leaves still message.
		parent := []Ruleset{Defaults()}
		derived := DeriveChildRules(parent, true)
		if got := Evaluate("task", "*", derived...); got != Deny {
			t.Fatalf("task = %q, want deny at depth cap", got)
		}
		for _, perm := range []string{
			"agent_message", "agent_broadcast", "agent_roster", "agent_ownership", "task_message",
		} {
			if got := Evaluate(perm, "*", derived...); got != Allow {
				t.Errorf("%s = %q, want allow (not stripped by denyTask)", perm, got)
			}
		}
	})

	t.Run("parent deny agent_message survives denyTask", func(t *testing.T) {
		parent := []Ruleset{
			Defaults(),
			{{Permission: "agent_message", Pattern: "*", Action: Deny}},
		}
		derived := DeriveChildRules(parent, true)
		if got := Evaluate("agent_message", "*", derived...); got != Deny {
			t.Errorf("agent_message = %q, want deny", got)
		}
		if got := Evaluate("agent_broadcast", "*", derived...); got != Allow {
			t.Errorf("agent_broadcast = %q, want allow", got)
		}
	})

	t.Run("allows task when nesting remains", func(t *testing.T) {
		parent := []Ruleset{Defaults()}
		derived := DeriveChildRules(parent, false)
		if got := Evaluate("task", "*", derived...); got != Allow {
			t.Errorf("task = %q, want allow (depth remains)", got)
		}
	})

	t.Run("deep copy does not mutate parent", func(t *testing.T) {
		parentLayer := Ruleset{{Permission: "read", Pattern: "*", Action: Allow}}
		parent := []Ruleset{parentLayer}
		derived := DeriveChildRules(parent, true)
		if len(derived) == 0 {
			t.Fatal("derived empty")
		}
		// Mutate returned ruleset layers.
		derived[0][0].Action = Deny
		derived[0] = append(derived[0], Rule{Permission: "bash", Pattern: "*", Action: Deny})
		if parentLayer[0].Action != Allow {
			t.Errorf("parent rule mutated to %q", parentLayer[0].Action)
		}
		if len(parentLayer) != 1 {
			t.Errorf("parent layer length = %d, want 1", len(parentLayer))
		}
		if got := Evaluate("read", "x", parent...); got != Allow {
			t.Errorf("parent evaluate after mutate = %q, want allow", got)
		}
	})

	t.Run("childExtra deny is kept", func(t *testing.T) {
		parent := []Ruleset{{{Permission: "edit", Pattern: "*", Action: Allow}}}
		childExtra := Ruleset{{Permission: "edit", Pattern: "secret.go", Action: Deny}}
		derived := DeriveChildRules(parent, true, childExtra)
		if got := Evaluate("edit", "secret.go", derived...); got != Deny {
			t.Errorf("secret.go = %q, want deny", got)
		}
		if got := Evaluate("edit", "ok.go", derived...); got != Allow {
			t.Errorf("ok.go = %q, want allow", got)
		}
	})
}

func TestEvaluateEmptyPatternMatchesStar(t *testing.T) {
	set := Ruleset{{Permission: "edit", Pattern: "", Action: Deny}}
	if got := Evaluate("edit", "foo.go", set); got != Deny {
		t.Errorf("got %q, want deny", got)
	}
}

func TestAskAllowAndDeny(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Ruleset{
		{Permission: "read", Pattern: "*", Action: Allow},
		{Permission: "bash", Pattern: "*", Action: Deny},
	})
	if err := svc.Ask(context.Background(), tool.AskRequest{Permission: "read", Patterns: []string{"a"}}); err != nil {
		t.Fatalf("allow: %v", err)
	}
	err := svc.Ask(context.Background(), tool.AskRequest{Permission: "bash", Patterns: []string{"rm -rf /"}})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("deny err = %v, want DeniedError", err)
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("deny err text = %q, want Permission denied", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no ask events for allow/deny, got %#v", events)
	}
}

func TestAskOnceReply(t *testing.T) {
	var mu sync.Mutex
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"echo hi"},
		})
	}()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		id := asked.RequestID
		mu.Unlock()
		if id != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ask")
		case <-time.After(5 * time.Millisecond):
		}
	}

	mu.Lock()
	id := asked.RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionOnce})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
}

func TestAskWithCorrelationRetainsPendingCorrelationAndRoutesByPermissionRequestID(t *testing.T) {
	events := make(chan protocol.Event, 2)
	svc := New(func(ev protocol.Event) { events <- ev }, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})
	wantCorr := protocol.Correlation{
		SessionID:         "session-1",
		TurnID:            "turn-1",
		ProviderRequestID: "provider-1",
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.AskWithCorrelation(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"echo hi"},
		}, wantCorr)
	}()

	var asked protocol.PermissionAsked
	select {
	case ev := <-events:
		var ok bool
		asked, ok = ev.(protocol.PermissionAsked)
		if !ok {
			t.Fatalf("first event = %T, want PermissionAsked", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PermissionAsked")
	}
	if asked.Correlation != wantCorr {
		t.Errorf("asked correlation = %#v, want %#v", asked.Correlation, wantCorr)
	}
	if asked.RequestID == "" || asked.RequestID == asked.ProviderRequestID {
		t.Fatalf("permission requestId = %q, providerRequestId = %q; want distinct IDs", asked.RequestID, asked.ProviderRequestID)
	}

	// A provider request ID is not a permission reply routing key.
	svc.Reply(protocol.PermissionReply{RequestID: asked.ProviderRequestID, Decision: protocol.DecisionOnce})
	select {
	case err := <-errCh:
		t.Fatalf("AskWithCorrelation returned after reply using provider request ID: %v", err)
	case ev := <-events:
		t.Fatalf("event emitted after reply using provider request ID: %#v", ev)
	case <-time.After(25 * time.Millisecond):
	}

	svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("AskWithCorrelation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AskWithCorrelation reply")
	}
	select {
	case ev := <-events:
		resolved, ok := ev.(protocol.PermissionResolved)
		if !ok {
			t.Fatalf("second event = %T, want PermissionResolved", ev)
		}
		if resolved.Correlation != wantCorr {
			t.Errorf("resolved correlation = %#v, want pending %#v", resolved.Correlation, wantCorr)
		}
		if resolved.RequestID != asked.RequestID {
			t.Errorf("resolved requestId = %q, want %q", resolved.RequestID, asked.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PermissionResolved")
	}
}

func TestCascadeResolutionsPreserveCorrelation(t *testing.T) {
	cases := []struct {
		name          string
		reply         protocol.Decision
		wantDecisions []protocol.Decision
		wantRejected  bool
	}{
		{
			name:          "always",
			reply:         protocol.DecisionAlways,
			wantDecisions: []protocol.Decision{protocol.DecisionAlways, protocol.DecisionOnce},
		},
		{
			name:          "reject",
			reply:         protocol.DecisionReject,
			wantDecisions: []protocol.Decision{protocol.DecisionReject, protocol.DecisionReject},
			wantRejected:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []protocol.Event
			var asked []protocol.PermissionAsked
			svc := New(func(ev protocol.Event) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, ev)
				if event, ok := ev.(protocol.PermissionAsked); ok {
					asked = append(asked, event)
				}
			}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

			correlations := []protocol.Correlation{
				{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"},
				{SessionID: "session-1", TurnID: "turn-2", ProviderRequestID: "provider-2"},
			}
			errs := []chan error{make(chan error, 1), make(chan error, 1)}
			requests := []tool.AskRequest{
				{Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"}},
				{Permission: "bash", Patterns: []string{"git log"}, Always: []string{"git *"}},
			}

			go func() {
				errs[0] <- svc.AskWithCorrelation(context.Background(), requests[0], correlations[0])
			}()
			waitAskedN(t, &mu, &asked, 1)
			go func() {
				errs[1] <- svc.AskWithCorrelation(context.Background(), requests[1], correlations[1])
			}()
			waitAskedN(t, &mu, &asked, 2)

			mu.Lock()
			directID := asked[0].RequestID
			mu.Unlock()
			svc.Reply(protocol.PermissionReply{RequestID: directID, Decision: tc.reply})

			for i, errCh := range errs {
				select {
				case err := <-errCh:
					var rejected *RejectedError
					if gotRejected := errors.As(err, &rejected); gotRejected != tc.wantRejected {
						t.Errorf("ask %d rejection = %v (err %v), want %v", i+1, gotRejected, err, tc.wantRejected)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for ask %d", i+1)
				}
			}

			mu.Lock()
			gotEvents := append([]protocol.Event(nil), events...)
			gotAsked := append([]protocol.PermissionAsked(nil), asked...)
			mu.Unlock()

			var resolved []protocol.PermissionResolved
			for _, ev := range gotEvents {
				if event, ok := ev.(protocol.PermissionResolved); ok {
					resolved = append(resolved, event)
				}
			}
			if len(gotAsked) != 2 {
				t.Errorf("PermissionAsked events = %d, want 2", len(gotAsked))
			}
			if len(resolved) != 2 {
				t.Errorf("PermissionResolved events = %d, want 2", len(resolved))
			}

			resolvedByID := make(map[string]protocol.PermissionResolved, len(resolved))
			for _, event := range resolved {
				resolvedByID[event.RequestID] = event
			}
			for i, ask := range gotAsked {
				if ask.Correlation != correlations[i] {
					t.Errorf("ask %d correlation = %#v, want %#v", i+1, ask.Correlation, correlations[i])
				}
				resolution, ok := resolvedByID[ask.RequestID]
				if !ok {
					t.Errorf("no PermissionResolved for ask %d requestId %q", i+1, ask.RequestID)
					continue
				}
				if resolution.Correlation != correlations[i] {
					t.Errorf("resolution for ask %d correlation = %#v, want %#v", i+1, resolution.Correlation, correlations[i])
				}
				if resolution.Decision != tc.wantDecisions[i] {
					t.Errorf("resolution for ask %d decision = %q, want %q", i+1, resolution.Decision, tc.wantDecisions[i])
				}
			}
		})
	}
}

func TestReplyEmitsResolvedBeforeReturningAnnouncedAsk(t *testing.T) {
	tests := []struct {
		name         string
		decision     protocol.Decision
		wantRejected bool
	}{
		{name: "once", decision: protocol.DecisionOnce},
		{name: "always", decision: protocol.DecisionAlways},
		{name: "reject", decision: protocol.DecisionReject, wantRejected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			askedCh := make(chan protocol.PermissionAsked, 1)
			resolvedEntered := make(chan protocol.PermissionResolved, 1)
			releaseResolved := make(chan struct{})
			svc := New(func(ev protocol.Event) {
				switch ev := ev.(type) {
				case protocol.PermissionAsked:
					askedCh <- ev
				case protocol.PermissionResolved:
					resolvedEntered <- ev
					<-releaseResolved
				}
			}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

			corr := protocol.Correlation{SessionID: "session", TurnID: "turn", ProviderRequestID: "provider"}
			errCh := make(chan error, 1)
			go func() {
				errCh <- svc.AskWithCorrelation(context.Background(), tool.AskRequest{
					Permission: "bash",
					Patterns:   []string{"echo ordered"},
					Always:     []string{"echo *"},
				}, corr)
			}()

			asked := receivePermissionAsked(t, askedCh)
			waitPendingAnnounced(t, svc, asked.RequestID)
			go svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: tt.decision})

			resolved := receivePermissionResolved(t, resolvedEntered)
			if resolved.RequestID != asked.RequestID || resolved.Correlation != corr || resolved.Decision != tt.decision {
				t.Errorf("PermissionResolved = %#v, want request %q correlation %#v decision %q", resolved, asked.RequestID, corr, tt.decision)
			}
			select {
			case err := <-errCh:
				close(releaseResolved)
				t.Fatalf("Ask returned before PermissionResolved emission completed: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			close(releaseResolved)
			assertPermissionResult(t, errCh, tt.wantRejected)
		})
	}
}

func TestCascadeEmitsAllAnnouncedResolutionsBeforeWakingWaiters(t *testing.T) {
	tests := []struct {
		name          string
		decision      protocol.Decision
		wantDecisions map[int]protocol.Decision
		wantRejected  bool
	}{
		{
			name:          "always",
			decision:      protocol.DecisionAlways,
			wantDecisions: map[int]protocol.Decision{0: protocol.DecisionAlways, 1: protocol.DecisionOnce},
		},
		{
			name:          "reject",
			decision:      protocol.DecisionReject,
			wantDecisions: map[int]protocol.Decision{0: protocol.DecisionReject, 1: protocol.DecisionReject},
			wantRejected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			askedCh := make(chan protocol.PermissionAsked, 2)
			resolvedEntered := make(chan protocol.PermissionResolved, 2)
			releaseResolved := make(chan struct{}, 2)
			svc := New(func(ev protocol.Event) {
				switch ev := ev.(type) {
				case protocol.PermissionAsked:
					askedCh <- ev
				case protocol.PermissionResolved:
					resolvedEntered <- ev
					<-releaseResolved
				}
			}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})
			defer func() {
				for range 2 {
					select {
					case releaseResolved <- struct{}{}:
					default:
					}
				}
			}()

			correlations := []protocol.Correlation{
				{SessionID: "session", TurnID: "turn-1", ProviderRequestID: "provider-1"},
				{SessionID: "session", TurnID: "turn-2", ProviderRequestID: "provider-2"},
			}
			errs := []chan error{make(chan error, 1), make(chan error, 1)}
			asks := make([]protocol.PermissionAsked, 2)
			for i := range asks {
				i := i
				go func() {
					errs[i] <- svc.AskWithCorrelation(context.Background(), tool.AskRequest{
						Permission: "bash",
						Patterns:   []string{"git status"},
						Always:     []string{"git *"},
					}, correlations[i])
				}()
				asks[i] = receivePermissionAsked(t, askedCh)
				waitPendingAnnounced(t, svc, asks[i].RequestID)
			}

			go svc.Reply(protocol.PermissionReply{RequestID: asks[0].RequestID, Decision: tt.decision})
			resolved := make([]protocol.PermissionResolved, 0, 2)
			for len(resolved) < 2 {
				resolved = append(resolved, receivePermissionResolved(t, resolvedEntered))
				select {
				case err := <-errs[0]:
					t.Fatalf("ask 1 returned after only %d of 2 PermissionResolved emissions completed: %v", len(resolved)-1, err)
				case err := <-errs[1]:
					t.Fatalf("ask 2 returned after only %d of 2 PermissionResolved emissions completed: %v", len(resolved)-1, err)
				case <-time.After(100 * time.Millisecond):
				}
				releaseResolved <- struct{}{}
			}

			askIndex := map[string]int{asks[0].RequestID: 0, asks[1].RequestID: 1}
			for _, event := range resolved {
				i, ok := askIndex[event.RequestID]
				if !ok {
					t.Errorf("PermissionResolved has unknown requestId %q", event.RequestID)
					continue
				}
				if event.Correlation != correlations[i] || event.Decision != tt.wantDecisions[i] {
					t.Errorf("PermissionResolved for ask %d = %#v, want correlation %#v decision %q", i+1, event, correlations[i], tt.wantDecisions[i])
				}
			}
			for _, errCh := range errs {
				assertPermissionResult(t, errCh, tt.wantRejected)
			}
		})
	}
}

func receivePermissionAsked(t *testing.T, events <-chan protocol.PermissionAsked) protocol.PermissionAsked {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PermissionAsked")
		return protocol.PermissionAsked{}
	}
}

func receivePermissionResolved(t *testing.T, events <-chan protocol.PermissionResolved) protocol.PermissionResolved {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PermissionResolved emitter")
		return protocol.PermissionResolved{}
	}
}

func waitPendingAnnounced(t *testing.T, svc *Service, requestID string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		svc.mu.Lock()
		pending := svc.pending[requestID]
		announced := pending != nil && pending.announced
		svc.mu.Unlock()
		if announced {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("permission request %q was not fully announced", requestID)
		default:
		}
	}
}

func assertPermissionResult(t *testing.T, errCh <-chan error, wantRejected bool) {
	t.Helper()
	select {
	case err := <-errCh:
		var rejected *RejectedError
		if gotRejected := errors.As(err, &rejected); gotRejected != wantRejected {
			t.Fatalf("Ask rejection = %v (err %v), want %v", gotRejected, err, wantRejected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask result")
	}
}

func TestCascadeAskedBeforeResolvedOrderWithBlockedEmitter(t *testing.T) {
	cases := []struct {
		name         string
		decision     protocol.Decision
		wantRejected bool
	}{
		{name: "always", decision: protocol.DecisionAlways},
		{name: "reject", decision: protocol.DecisionReject, wantRejected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []protocol.Event
			askedCount := 0
			firstAsked := make(chan protocol.PermissionAsked, 1)
			secondAsked := make(chan protocol.PermissionAsked, 1)
			releaseSecondAsked := make(chan struct{})

			svc := New(func(ev protocol.Event) {
				if asked, ok := ev.(protocol.PermissionAsked); ok {
					mu.Lock()
					askedCount++
					ordinal := askedCount
					mu.Unlock()
					switch ordinal {
					case 1:
						mu.Lock()
						events = append(events, ev)
						mu.Unlock()
						firstAsked <- asked
						return
					case 2:
						secondAsked <- asked
						<-releaseSecondAsked
					}
				}
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

			correlations := []protocol.Correlation{
				{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"},
				{SessionID: "session-1", TurnID: "turn-2", ProviderRequestID: "provider-2"},
			}
			requests := []tool.AskRequest{
				{Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"}},
				{Permission: "bash", Patterns: []string{"git log"}, Always: []string{"git *"}},
			}
			errs := []chan error{make(chan error, 1), make(chan error, 1)}

			go func() {
				errs[0] <- svc.AskWithCorrelation(context.Background(), requests[0], correlations[0])
			}()
			var asks [2]protocol.PermissionAsked
			select {
			case asks[0] = <-firstAsked:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for first PermissionAsked")
			}

			go func() {
				errs[1] <- svc.AskWithCorrelation(context.Background(), requests[1], correlations[1])
			}()
			select {
			case asks[1] = <-secondAsked:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for blocked second PermissionAsked")
			}

			replied := make(chan struct{})
			go func() {
				svc.Reply(protocol.PermissionReply{RequestID: asks[0].RequestID, Decision: tc.decision})
				close(replied)
			}()
			select {
			case <-replied:
			case <-time.After(2 * time.Second):
				t.Fatal("Reply deadlocked while PermissionAsked emitter was blocked")
			}

			close(releaseSecondAsked)
			for i, errCh := range errs {
				select {
				case err := <-errCh:
					var rejected *RejectedError
					if gotRejected := errors.As(err, &rejected); gotRejected != tc.wantRejected {
						t.Errorf("ask %d rejection = %v (err %v), want %v", i+1, gotRejected, err, tc.wantRejected)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for ask %d", i+1)
				}
			}

			mu.Lock()
			gotEvents := append([]protocol.Event(nil), events...)
			mu.Unlock()
			assertAskedBeforeResolved(t, gotEvents, asks[:], correlations)
		})
	}
}

func TestReentrantEmitterAskedBeforeResolvedOrder(t *testing.T) {
	var mu sync.Mutex
	var events []protocol.Event
	var svc *Service
	svc = New(func(ev protocol.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		if asked, ok := ev.(protocol.PermissionAsked); ok {
			svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce})
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	wantCorr := protocol.Correlation{
		SessionID:         "session-reentrant",
		TurnID:            "turn-reentrant",
		ProviderRequestID: "provider-reentrant",
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.AskWithCorrelation(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"echo reentrant"},
		}, wantCorr)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("AskWithCorrelation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskWithCorrelation deadlocked with reentrant emitter")
	}

	mu.Lock()
	gotEvents := append([]protocol.Event(nil), events...)
	mu.Unlock()
	if len(gotEvents) == 0 {
		t.Fatal("no permission events emitted")
	}
	asked, ok := gotEvents[0].(protocol.PermissionAsked)
	if !ok {
		t.Fatalf("first event = %T, want PermissionAsked", gotEvents[0])
	}
	assertAskedBeforeResolved(t, gotEvents, []protocol.PermissionAsked{asked}, []protocol.Correlation{wantCorr})
}

func assertAskedBeforeResolved(t *testing.T, events []protocol.Event, asks []protocol.PermissionAsked, correlations []protocol.Correlation) {
	t.Helper()
	for i, ask := range asks {
		askedAt, resolvedAt := -1, -1
		for eventIndex, ev := range events {
			switch event := ev.(type) {
			case protocol.PermissionAsked:
				if event.RequestID == ask.RequestID {
					askedAt = eventIndex
					if event.Correlation != correlations[i] {
						t.Errorf("PermissionAsked for %q correlation = %#v, want %#v", ask.RequestID, event.Correlation, correlations[i])
					}
				}
			case protocol.PermissionResolved:
				if event.RequestID == ask.RequestID {
					resolvedAt = eventIndex
					if event.Correlation != correlations[i] {
						t.Errorf("PermissionResolved for %q correlation = %#v, want %#v", ask.RequestID, event.Correlation, correlations[i])
					}
				}
			}
		}
		if askedAt < 0 || resolvedAt < 0 {
			t.Errorf("events for request %q: asked index %d, resolved index %d; events %#v", ask.RequestID, askedAt, resolvedAt, events)
			continue
		}
		if askedAt >= resolvedAt {
			t.Errorf("events for request %q out of order: PermissionAsked index %d, PermissionResolved index %d", ask.RequestID, askedAt, resolvedAt)
		}
	}
}

func TestAskRejectWithMessage(t *testing.T) {
	var asked protocol.PermissionAsked
	var mu sync.Mutex
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"f.go"}})
	}()
	waitAsked(t, &mu, &asked)
	mu.Lock()
	id := asked.RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionReject, Message: "nope"})
	err := <-errCh
	var rej *RejectedError
	if !errors.As(err, &rej) || rej.Message != "nope" {
		t.Fatalf("err = %v", err)
	}
	if rej.Error() == "" || !strings.Contains(rej.Error(), "nope") {
		t.Errorf("Error() = %q", rej.Error())
	}
}

func TestAlwaysGrantResolvesSiblings(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() {
		err1 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"},
		})
	}()
	// Wait for first ask before starting second so IDs are ordered.
	waitAskedN(t, &mu, &asked, 1)
	go func() {
		err2 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git log"}, Always: []string{"git *"},
		})
	}()
	waitAskedN(t, &mu, &asked, 2)

	mu.Lock()
	first := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: first, Decision: protocol.DecisionAlways})

	for i, ch := range []chan error{err1, err2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("ask %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on ask %d", i+1)
		}
	}

	// Subsequent matching ask should auto-allow without emitting another ask.
	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash", Patterns: []string{"git diff"},
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := len(asked)
	mu.Unlock()
	if after != before {
		t.Errorf("always grant still prompted; asks %d -> %d", before, after)
	}
}

func TestRejectCascadesToSiblings(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})

	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() {
		err1 <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"a.go"}})
	}()
	waitAskedN(t, &mu, &asked, 1)
	go func() {
		err2 <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"b.go"}})
	}()
	waitAskedN(t, &mu, &asked, 2)

	mu.Lock()
	id := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionReject})

	for i, ch := range []chan error{err1, err2} {
		select {
		case err := <-ch:
			var rej *RejectedError
			if !errors.As(err, &rej) {
				t.Fatalf("ask %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on ask %d", i+1)
		}
	}
}

func TestAskContextCancel(t *testing.T) {
	svc := New(func(protocol.Event) {}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"sleep 99"}})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestWorstCaseAcrossPatterns(t *testing.T) {
	svc := New(func(protocol.Event) {}, Ruleset{
		{Permission: "edit", Pattern: "safe.go", Action: Allow},
		{Permission: "edit", Pattern: "secret.go", Action: Deny},
	})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "edit",
		Patterns:   []string{"safe.go", "secret.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want DeniedError", err)
	}
}

func waitAsked(t *testing.T, mu *sync.Mutex, asked *protocol.PermissionAsked) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		ok := asked.RequestID != ""
		mu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ask")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitAskedN(t *testing.T, mu *sync.Mutex, asked *[]protocol.PermissionAsked, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(*asked)
		mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d asks (have %d)", n, got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestValidateRuleset(t *testing.T) {
	cases := []struct {
		name    string
		rs      Ruleset
		wantErr bool
	}{
		{
			name: "valid write deny",
			rs:   Ruleset{{Permission: "write", Pattern: "*", Action: Deny}},
		},
		{
			name:    "unknown permission",
			rs:      Ruleset{{Permission: "nope", Pattern: "*", Action: Deny}},
			wantErr: true,
		},
		{
			name:    "bad action",
			rs:      Ruleset{{Permission: "write", Pattern: "*", Action: "sometimes"}},
			wantErr: true,
		},
		{
			name:    "empty permission name",
			rs:      Ruleset{{Permission: "", Pattern: "*", Action: Deny}},
			wantErr: true,
		},
		{
			name: "star allow ok",
			rs:   Ruleset{{Permission: "*", Pattern: "*", Action: Allow}},
		},
		{
			name: "empty pattern ok",
			rs:   Ruleset{{Permission: "write", Pattern: "", Action: Deny}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRuleset(tc.rs)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateRuleset(%#v) = nil, want error", tc.rs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateRuleset(%#v) = %v, want nil", tc.rs, err)
			}
		})
	}
}

func TestSetAgentRulesDenyBeatsBaseAllow(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Ruleset{
		{Permission: "write", Pattern: "*", Action: Allow},
	})
	svc.SetAgentRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})

	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"secret.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Ask = %v, want DeniedError", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no permission events on hard deny, got %#v", events)
	}
}

func TestSetAgentRulesClearsAlwaysGrants(t *testing.T) {
	// AG2 exit: session "always" grants must not survive an agent profile swap.
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "write",
			Patterns:   []string{"a.go"},
			Always:     []string{"*"},
		})
	}()
	waitAskedN(t, &mu, &asked, 1)
	mu.Lock()
	id := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionAlways})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first Ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first Ask")
	}

	// Grant is live: second Ask auto-allows with no new PermissionAsked.
	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"b.go"},
	}); err != nil {
		t.Fatalf("second Ask under always grant: %v", err)
	}
	mu.Lock()
	afterGrant := len(asked)
	mu.Unlock()
	if afterGrant != before {
		t.Fatalf("always grant still prompted; asks %d -> %d", before, afterGrant)
	}

	// Agent deny replaces the layer and clears session grants.
	svc.SetAgentRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"c.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("third Ask after SetAgentRules deny = %v, want DeniedError", err)
	}
	mu.Lock()
	afterDeny := len(asked)
	mu.Unlock()
	if afterDeny != afterGrant {
		t.Errorf("deny path emitted PermissionAsked; asks %d -> %d", afterGrant, afterDeny)
	}
}

func TestSetAgentRulesEmptyClearsAgentLayer(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Ruleset{
		{Permission: "write", Pattern: "*", Action: Allow},
	})
	svc.SetAgentRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"x.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Ask under agent deny = %v, want DeniedError", err)
	}

	svc.SetAgentRules(nil)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"y.go"},
	}); err != nil {
		t.Fatalf("Ask after clearing agent layer: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no ask events for allow/deny paths, got %#v", events)
	}
}

func TestSetAgentRulesRejectsPending(t *testing.T) {
	var mu sync.Mutex
	var events []protocol.Event
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		mu.Lock()
		events = append(events, ev)
		if a, ok := ev.(protocol.PermissionAsked); ok {
			asked = a
		}
		mu.Unlock()
	}, Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "write",
			Patterns:   []string{"pending.go"},
		})
	}()
	waitAsked(t, &mu, &asked)
	waitPendingAnnounced(t, svc, asked.RequestID)

	svc.SetAgentRules(Ruleset{{Permission: "bash", Pattern: "*", Action: Deny}})

	select {
	case err := <-errCh:
		var rej *RejectedError
		if !errors.As(err, &rej) {
			t.Fatalf("pending Ask after SetAgentRules = %v, want RejectedError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending Ask to reject")
	}

	mu.Lock()
	got := append([]protocol.Event(nil), events...)
	mu.Unlock()
	var resolved *protocol.PermissionResolved
	for _, ev := range got {
		if r, ok := ev.(protocol.PermissionResolved); ok && r.RequestID == asked.RequestID {
			event := r
			resolved = &event
		}
	}
	if resolved == nil {
		t.Fatalf("expected PermissionResolved for pending ask; events=%#v", got)
	}
	if resolved.Decision != protocol.DecisionReject {
		t.Errorf("PermissionResolved decision = %q, want reject", resolved.Decision)
	}
}

func TestSetPhaseRulesDenyBeatsBaseAllow(t *testing.T) {
	svc := New(func(protocol.Event) {}, Ruleset{
		{Permission: "write", Pattern: "*", Action: Allow},
	})
	svc.SetPhaseRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"a.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Ask = %v, want DeniedError", err)
	}
}

func TestPhaseDenyBeatsGrantedAlways(t *testing.T) {
	var asked protocol.PermissionAsked
	var mu sync.Mutex
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Ruleset{{Permission: "write", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "write",
			Patterns:   []string{"a.go"},
			Always:     []string{"*"},
		})
	}()
	waitAsked(t, &mu, &asked)
	waitPendingAnnounced(t, svc, asked.RequestID)
	svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionAlways})
	if err := <-errCh; err != nil {
		t.Fatalf("always grant: %v", err)
	}

	// Now phase deny must still block.
	svc.SetPhaseRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"b.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("after phase deny Ask = %v, want DeniedError", err)
	}
}

func TestProjectGrantResolvesSiblingsAndPersists(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	var persisted []Rule
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})
	svc.SetProjectPersister(func(r Rule) error {
		mu.Lock()
		persisted = append(persisted, r)
		mu.Unlock()
		return nil
	})

	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() {
		err1 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"},
		})
	}()
	waitAskedN(t, &mu, &asked, 1)
	go func() {
		err2 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git log"}, Always: []string{"git *"},
		})
	}()
	waitAskedN(t, &mu, &asked, 2)

	mu.Lock()
	first := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: first, Decision: protocol.DecisionProject})

	for i, ch := range []chan error{err1, err2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("ask %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on ask %d", i+1)
		}
	}

	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash", Patterns: []string{"git diff"},
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := len(asked)
	gotPersisted := append(Ruleset(nil), persisted...)
	mu.Unlock()
	if after != before {
		t.Errorf("project grant still prompted; asks %d -> %d", before, after)
	}
	want := Rule{Permission: "bash", Pattern: "git *", Action: Allow}
	if len(gotPersisted) != 1 || gotPersisted[0] != want {
		t.Errorf("persisted = %#v, want [%#v]", gotPersisted, want)
	}
}

func TestProjectGrantSurvivesAgentSwitch(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"ls"},
			Always:     []string{"ls*"},
		})
	}()
	waitAskedN(t, &mu, &asked, 1)
	mu.Lock()
	id := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionProject})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first Ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first Ask")
	}

	// Empty agent layer swap clears session grants only.
	svc.SetAgentRules(nil)
	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"ls -la"},
	}); err != nil {
		t.Fatalf("Ask after agent swap: %v", err)
	}
	mu.Lock()
	after := len(asked)
	mu.Unlock()
	if after != before {
		t.Fatalf("project grant cleared on agent swap; asks %d -> %d", before, after)
	}

	// Agent deny still beats project allow.
	svc.SetAgentRules(Ruleset{{Permission: "bash", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"ls"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Ask under agent deny = %v, want DeniedError", err)
	}
}

func TestProjectPersistFailureStillGrants(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "edit", Pattern: "*", Action: Ask}})
	svc.SetProjectPersister(func(Rule) error {
		return errors.New("disk full")
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "edit",
			Patterns:   []string{"a.go"},
		})
	}()
	waitAskedN(t, &mu, &asked, 1)
	mu.Lock()
	id := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionProject})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Ask after failed persist: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "edit",
		Patterns:   []string{"a.go"},
	}); err != nil {
		t.Fatalf("in-memory grant after persist failure: %v", err)
	}
	mu.Lock()
	after := len(asked)
	mu.Unlock()
	if after != before {
		t.Errorf("still prompted after project grant with persist error; asks %d -> %d", before, after)
	}
}

func TestSeedAlwaysGrants(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Defaults())
	svc.SeedAlwaysGrants(Ruleset{
		{Permission: "bash", Pattern: "git *", Action: Allow},
	})
	ctx := context.Background()
	if err := svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"}}); err != nil {
		t.Fatalf("Ask after seed: %v", err)
	}
	mu.Lock()
	n := len(asked)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("PermissionAsked count = %d, want 0", n)
	}
}

func TestPeekReflectsHardDeny(t *testing.T) {
	svc := New(func(protocol.Event) {}, Defaults())
	if got := svc.Peek("read"); got != Allow {
		t.Fatalf("Peek(read) = %q, want allow", got)
	}
	if got := svc.Peek("write"); got != Ask {
		t.Fatalf("Peek(write) = %q, want ask", got)
	}
	svc.SetAgentRules(Ruleset{{Permission: "write", Pattern: "*", Action: Deny}})
	if got := svc.Peek("write"); got != Deny {
		t.Fatalf("Peek(write) after agent deny = %q, want deny", got)
	}
	svc.SetPermissionMode(protocol.PermissionModePlan)
	if got := svc.Peek("edit"); got != Deny {
		t.Fatalf("Peek(edit) in plan mode = %q, want deny", got)
	}
	if got := svc.Peek("read"); got != Allow {
		t.Fatalf("Peek(read) in plan mode = %q, want allow", got)
	}
}

func TestPeekNilService(t *testing.T) {
	var svc *Service
	if got := svc.Peek("read"); got != Ask {
		t.Fatalf("nil Peek = %q, want ask", got)
	}
}
