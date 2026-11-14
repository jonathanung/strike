package permission

import (
	"context"
	"sync"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// TestAskFactsStampedOnPermissionAsked pins the #1507 contract: the facts
// an engine records via SetAskFacts (tool-call working directory and the
// project rule store path) ride on every PermissionAsked it emits, so the
// ask card can show where the action runs and where a project grant is
// stored. Without SetAskFacts both fields stay empty and frontends hide
// the corresponding lines instead of inventing them.
func TestAskFactsStampedOnPermissionAsked(t *testing.T) {
	tests := []struct {
		name        string
		set         bool
		workDir     string
		rulePath    string
		wantWorkDir string
		wantPath    string
	}{
		{
			name:        "facts stamped",
			set:         true,
			workDir:     "/work/project",
			rulePath:    "/work/project/.strike/config",
			wantWorkDir: "/work/project",
			wantPath:    "/work/project/.strike/config",
		},
		{
			name:        "workdir only (project grants not persisted)",
			set:         true,
			workDir:     "/work/project",
			wantWorkDir: "/work/project",
			wantPath:    "",
		},
		{
			name:        "unset facts stay empty",
			set:         false,
			wantWorkDir: "",
			wantPath:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var asked protocol.PermissionAsked
			svc := New(func(ev protocol.Event) {
				if a, ok := ev.(protocol.PermissionAsked); ok {
					mu.Lock()
					asked = a
					mu.Unlock()
				}
			}, Defaults())
			if tc.set {
				svc.SetAskFacts(tc.workDir, tc.rulePath)
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- svc.Ask(context.Background(), tool.AskRequest{
					Permission: "bash",
					Patterns:   []string{"ls -la"},
					Always:     []string{"ls *"},
				})
			}()
			waitAsked(t, &mu, &asked)

			mu.Lock()
			got := asked
			mu.Unlock()
			if got.WorkDir != tc.wantWorkDir {
				t.Errorf("PermissionAsked.WorkDir = %q, want %q", got.WorkDir, tc.wantWorkDir)
			}
			if got.ProjectRulePath != tc.wantPath {
				t.Errorf("PermissionAsked.ProjectRulePath = %q, want %q", got.ProjectRulePath, tc.wantPath)
			}

			svc.Reply(protocol.PermissionReply{RequestID: got.RequestID, Decision: protocol.DecisionOnce})
			if err := <-errCh; err != nil {
				t.Fatalf("ask after allow-once reply: %v", err)
			}
		})
	}
}

// TestSetAskFactsNilService guards the nil-receiver contract used by
// engines that run without a permission service.
func TestSetAskFactsNilService(t *testing.T) {
	var svc *Service
	svc.SetAskFacts("/work/project", "/work/project/.strike/config") // must not panic
}
