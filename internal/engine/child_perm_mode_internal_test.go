package engine

import (
	"context"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestChildInitialPermissionMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		lock     bool
		initial  protocol.PermissionMode
		live     protocol.PermissionMode
		restored protocol.PermissionMode
		want     protocol.PermissionMode
	}{
		{
			name: "inherit live yolo when unlocked",
			live: protocol.PermissionModeYolo,
			want: protocol.PermissionModeYolo,
		},
		{
			name: "inherit live accept-edits when unlocked",
			live: protocol.PermissionModeAcceptEdits,
			want: protocol.PermissionModeAcceptEdits,
		},
		{
			name: "inherit live soft-approve when unlocked",
			live: protocol.PermissionModeSoftApprove,
			want: protocol.PermissionModeSoftApprove,
		},
		{
			name: "empty live stays empty (engine default at startup)",
			live: "",
			want: "",
		},
		{
			name:     "restored wins over live when unlocked",
			live:     protocol.PermissionModeYolo,
			restored: protocol.PermissionModeAcceptEdits,
			want:     protocol.PermissionModeAcceptEdits,
		},
		{
			name:    "mdm lock pins initial even when live differs",
			lock:    true,
			initial: protocol.PermissionModeDefault,
			live:    protocol.PermissionModeYolo,
			want:    protocol.PermissionModeDefault,
		},
		{
			name:     "mdm lock pins initial over restored",
			lock:     true,
			initial:  protocol.PermissionModePlan,
			live:     protocol.PermissionModeYolo,
			restored: protocol.PermissionModeAcceptEdits,
			want:     protocol.PermissionModePlan,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &Engine{
				opts: Options{
					LockPermissionMode:    tc.lock,
					InitialPermissionMode: tc.initial,
				},
				permMode: tc.live,
			}
			got := e.childInitialPermissionMode(tc.restored)
			if got != tc.want {
				t.Fatalf("childInitialPermissionMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpawnChildInheritsParentPermissionMode(t *testing.T) {
	// End-to-end: parent live yolo → child Options.InitialPermissionMode yolo.
	eng := New(Options{
		SessionID:       "parent-perm-inherit",
		InitialProvider: "echo",
		Select: func(string) (provider.Provider, string, error) {
			return echo.New(), "echo", nil
		},
		Registry: tool.NewRegistry(tool.NewTask()),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
		Agents: []Agent{
			{Name: "build", Description: "build"},
			{Name: "general", Description: "general"},
		},
		InitialAgent:      "build",
		DelegationPolicy:  DelegationPolicyConfig{Mode: PolicyOff},
		OpenChildSession:  func(_, id, _ string) (string, error) { return id, nil },
		AppendChildEvent:  func(string, protocol.Event) error { return nil },
		CloseChildSession: func(string) error { return nil },
	})
	eng.permMode = protocol.PermissionModeYolo
	eng.agent = Agent{Name: "build", Description: "build"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// runCtx is used as the child lifetime parent.
	eng.runCtx = ctx

	out, err := eng.spawnChild(ctx, tool.TaskRequest{
		Prompt:        "implement a multi-file feature with careful design and tests across packages",
		Agent:         "general",
		ForceDelegate: true,
	})
	if err != nil {
		t.Fatalf("spawnChild: %v", err)
	}
	if out.SessionID == "" {
		t.Fatalf("spawnChild returned empty session id: %+v", out)
	}

	eng.childMu.Lock()
	h := eng.children[out.SessionID]
	eng.childMu.Unlock()
	if h == nil || h.eng == nil {
		t.Fatalf("missing child handle for %q", out.SessionID)
	}
	got := h.eng.opts.InitialPermissionMode
	if got != protocol.PermissionModeYolo {
		t.Fatalf("child InitialPermissionMode = %q, want yolo", got)
	}

	// Tear down child promptly.
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child done")
	}
}

func TestSpawnChildLockedPermissionModeIgnoresLive(t *testing.T) {
	eng := New(Options{
		SessionID:             "parent-perm-locked",
		InitialProvider:       "echo",
		InitialPermissionMode: protocol.PermissionModeDefault,
		LockPermissionMode:    true,
		Select: func(string) (provider.Provider, string, error) {
			return echo.New(), "echo", nil
		},
		Registry: tool.NewRegistry(tool.NewTask()),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
		Agents: []Agent{
			{Name: "build", Description: "build"},
			{Name: "general", Description: "general"},
		},
		InitialAgent:      "build",
		DelegationPolicy:  DelegationPolicyConfig{Mode: PolicyOff},
		OpenChildSession:  func(_, id, _ string) (string, error) { return id, nil },
		AppendChildEvent:  func(string, protocol.Event) error { return nil },
		CloseChildSession: func(string) error { return nil },
	})
	// Live dial would be yolo if unlocked; lock must pin managed default.
	eng.permMode = protocol.PermissionModeYolo
	eng.agent = Agent{Name: "build", Description: "build"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.runCtx = ctx

	out, err := eng.spawnChild(ctx, tool.TaskRequest{
		Prompt:        "implement a multi-file feature with careful design and tests across packages",
		Agent:         "general",
		ForceDelegate: true,
	})
	if err != nil {
		t.Fatalf("spawnChild: %v", err)
	}
	eng.childMu.Lock()
	h := eng.children[out.SessionID]
	eng.childMu.Unlock()
	if h == nil || h.eng == nil {
		t.Fatalf("missing child handle for %q", out.SessionID)
	}
	got := h.eng.opts.InitialPermissionMode
	if got != protocol.PermissionModeDefault {
		t.Fatalf("child InitialPermissionMode = %q, want default (locked)", got)
	}
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child done")
	}
}
