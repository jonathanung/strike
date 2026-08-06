package permission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestModeEvaluateMatrix(t *testing.T) {
	base := Defaults()
	type cell struct {
		mode       protocol.PermissionMode
		permission string
		want       Action
	}
	cases := []cell{
		{protocol.PermissionModeDefault, "read", Allow},
		{protocol.PermissionModeDefault, "edit", Ask},
		{protocol.PermissionModeDefault, "write", Ask},
		{protocol.PermissionModeDefault, "bash", Ask},
		{protocol.PermissionModeDefault, "webfetch", Ask},
		{protocol.PermissionModeDefault, "websearch", Ask},

		{protocol.PermissionModePlan, "read", Allow},
		{protocol.PermissionModePlan, "edit", Deny},
		{protocol.PermissionModePlan, "write", Deny},
		{protocol.PermissionModePlan, "bash", Ask},
		{protocol.PermissionModePlan, "webfetch", Ask},
		{protocol.PermissionModePlan, "websearch", Ask},

		// soft-approve is TUI countdown only — engine matches default.
		{protocol.PermissionModeSoftApprove, "read", Allow},
		{protocol.PermissionModeSoftApprove, "edit", Ask},
		{protocol.PermissionModeSoftApprove, "write", Ask},
		{protocol.PermissionModeSoftApprove, "bash", Ask},
		{protocol.PermissionModeSoftApprove, "webfetch", Ask},
		{protocol.PermissionModeSoftApprove, "websearch", Ask},

		{protocol.PermissionModeAcceptEdits, "read", Allow},
		{protocol.PermissionModeAcceptEdits, "edit", Allow},
		{protocol.PermissionModeAcceptEdits, "write", Allow},
		{protocol.PermissionModeAcceptEdits, "bash", Ask},
		{protocol.PermissionModeAcceptEdits, "webfetch", Ask},
		{protocol.PermissionModeAcceptEdits, "websearch", Ask},

		{protocol.PermissionModeYolo, "read", Allow},
		{protocol.PermissionModeYolo, "edit", Allow},
		{protocol.PermissionModeYolo, "write", Allow},
		{protocol.PermissionModeYolo, "bash", Allow},
		{protocol.PermissionModeYolo, "webfetch", Allow},
		{protocol.PermissionModeYolo, "websearch", Allow},
	}
	for _, tc := range cases {
		late := ModeLateRules(tc.mode)
		got := ApplyMode(tc.mode, tc.permission, Evaluate(tc.permission, "x", base, late))
		if got != tc.want {
			t.Errorf("mode=%s perm=%s: got %s, want %s", tc.mode, tc.permission, got, tc.want)
		}
	}
}

func TestApplyModeNeverWidensDeny(t *testing.T) {
	for _, mode := range protocol.PermissionModes() {
		if got := ApplyMode(mode, "bash", Deny); got != Deny {
			t.Errorf("ApplyMode(%s, deny) = %s, want deny", mode, got)
		}
		if got := ApplyMode(mode, "edit", Allow); got != Allow {
			t.Errorf("ApplyMode(%s, allow) = %s, want allow", mode, got)
		}
	}
	if got := ApplyMode(protocol.PermissionModeAcceptEdits, "bash", Ask); got != Ask {
		t.Errorf("accept-edits bash ask = %s, want ask", got)
	}
	if got := ApplyMode(protocol.PermissionModeAcceptEdits, "edit", Ask); got != Allow {
		t.Errorf("accept-edits edit ask = %s, want allow", got)
	}
	if got := ApplyMode(protocol.PermissionModeYolo, "bash", Ask); got != Allow {
		t.Errorf("yolo bash ask = %s, want allow", got)
	}
}

func TestYoloHonorsExplicitDeny(t *testing.T) {
	svc := New(func(protocol.Event) {}, Defaults())
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	svc.SetAgentRules(Ruleset{{Permission: "bash", Pattern: "*", Action: Deny}})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"rm -rf /"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("yolo + agent deny bash = %v, want DeniedError", err)
	}
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "edit",
		Patterns:   []string{"a.go"},
	}); err != nil {
		t.Fatalf("yolo edit = %v, want nil", err)
	}
}

func TestYoloHonorsConfigDeny(t *testing.T) {
	svc := New(func(protocol.Event) {},
		Defaults(),
		Ruleset{{Permission: "bash", Pattern: "rm *", Action: Deny}},
	)
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"rm -rf tmp"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("yolo + config deny rm = %v, want DeniedError", err)
	}
	// Non-matching bash is upgraded.
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"ls"},
	}); err != nil {
		t.Fatalf("yolo bash ls = %v, want nil", err)
	}
}

func TestPlanModeDenyBeatsSessionAlwaysGrant(t *testing.T) {
	svc := New(func(protocol.Event) {}, Defaults())
	svc.SeedAlwaysGrants(Ruleset{{Permission: "write", Pattern: "*", Action: Allow}})
	svc.SetPermissionMode(protocol.PermissionModePlan)
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"a.go"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("plan + always grant write = %v, want DeniedError", err)
	}
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "read",
		Patterns:   []string{"a.go"},
	}); err != nil {
		t.Fatalf("plan read = %v, want nil", err)
	}
}

func TestAcceptEditsAllowsEditAsksBash(t *testing.T) {
	var mu sync.Mutex
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Defaults())
	svc.SetPermissionMode(protocol.PermissionModeAcceptEdits)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "edit",
		Patterns:   []string{"a.go"},
	}); err != nil {
		t.Fatalf("accept-edits edit = %v, want nil", err)
	}
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "write",
		Patterns:   []string{"b.go"},
	}); err != nil {
		t.Fatalf("accept-edits write = %v, want nil", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"ls"},
		})
	}()
	waitAsked(t, &mu, &asked)
	svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce})
	if err := <-errCh; err != nil {
		t.Fatalf("bash after allow-once: %v", err)
	}
}

func TestSetPermissionModeDefaultClearsYolo(t *testing.T) {
	var mu sync.Mutex
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Defaults())
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash", Patterns: []string{"ls"},
	}); err != nil {
		t.Fatalf("yolo bash = %v", err)
	}
	svc.SetPermissionMode(protocol.PermissionModeDefault)
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"ls"},
		})
	}()
	waitAsked(t, &mu, &asked)
	svc.Reply(protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionReject})
	err := <-errCh
	var rejected *RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("default bash after yolo = %v, want RejectedError", err)
	}
}

func TestSetPermissionModeRejectsPending(t *testing.T) {
	var mu sync.Mutex
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Defaults())
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"ls"},
		})
	}()
	waitAsked(t, &mu, &asked)
	waitPendingAnnounced(t, svc, asked.RequestID)
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	select {
	case err := <-errCh:
		var rejected *RejectedError
		if !errors.As(err, &rejected) {
			t.Fatalf("pending Ask after mode change = %v, want RejectedError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending ask rejection")
	}
}
