package main

import (
	"context"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/schedule/scriptrun"
	"github.com/jonathanung/strike/internal/trust/audit"
)

// processBypassEnabled reports whether a root may carry the process
// --auto / --dangerously-skip-permissions allow-all layer. Unattended
// roots (schedules, triggers) never may: that layer is not removed by
// SetPermissionMode, so plan would still allow bash.
func processBypassEnabled(processSkip, unattended bool) bool {
	return processSkip && !unattended
}

// scheduleScripts is the script fire path's wiring. A nil runner leaves
// script runs unavailable (attach-only); a nil sink writes no script_run
// records. The sink must be retention-bounded (openAuditSink), since an
// every-minute schedule behind a long command appends a skip per tick.
type scheduleScripts struct {
	runner *scriptrun.Runner
	sink   *audit.Sink
}

// openScheduleService loads the clock next to the workspace registry.
// A nil hub leaves fires unavailable (attach-only) but the registry still
// edits. The starter is the same rootstart.Starter triggers should call.
func openScheduleService(dir string, hub *server.LiveHub, services *host.Services, ws *workspace.Store, cwd string, scripts scheduleScripts) (*schedule.Service, error) {
	hooks := schedule.Hooks{
		Starter: server.NewHubStarter(hub),
		WorkDir: func(id string) string {
			if path, ok := workspacePath(ws, id); ok {
				return path
			}
			return cwd
		},
		WorkspaceMode: func(id string) (string, bool) {
			return workspaceMode(ws, id)
		},
	}
	if hub != nil {
		hooks.Busy = func(sessionID string) bool {
			live := hub.LiveFor(sessionID)
			if live == nil {
				return false
			}
			return live.Status().Busy
		}
	}
	if services != nil && services.Goals != nil {
		goals := services.Goals
		hooks.Goal = func(id string) (schedule.GoalRef, bool, error) {
			g, ok, err := goals.Get(id)
			if err != nil || !ok {
				return schedule.GoalRef{}, ok, err
			}
			ref := schedule.GoalRef{ID: g.ID, Description: g.Description}
			for _, c := range g.Criteria {
				if strings.TrimSpace(c.Description) != "" {
					ref.Criteria = append(ref.Criteria, c.Description)
				}
			}
			return ref, true, nil
		}
	}
	if services != nil && services.Workflows != nil {
		catalog := services.Workflows
		hooks.Workflow = func(name string) (bool, bool) {
			sum, ok := catalog.Get(name)
			if !ok {
				return false, false
			}
			return true, sum.Valid && len(sum.Phases) > 0
		}
	}
	if scripts.runner != nil {
		hooks.Script = scriptHook(scripts.runner)
	}
	if scripts.sink != nil {
		hooks.Audit = scriptAuditHook(scripts.sink)
	}
	return schedule.Open(dir, hooks, cwd)
}

// scriptHook adapts the scriptrun runner onto the clock's seam.
func scriptHook(r *scriptrun.Runner) schedule.ScriptRunner {
	return func(ctx context.Context, req schedule.ScriptRequest, started func(schedule.ScriptStart)) schedule.ScriptResult {
		out := r.Run(ctx, scriptrun.Request{
			Command:        req.Command,
			Dir:            req.Dir,
			Timeout:        req.Timeout,
			SecretRefs:     req.SecretRefs,
			PermissionMode: req.PermissionMode,
		}, func(st scriptrun.Start) {
			started(schedule.ScriptStart{Decision: st.Decision, Sandbox: st.Sandbox, StartedAt: st.StartedAt})
		})
		return schedule.ScriptResult{
			Status:    out.Status,
			Decision:  out.Decision,
			Sandbox:   out.Sandbox,
			StartedAt: out.StartedAt,
			EndedAt:   out.EndedAt,
			ExitCode:  out.ExitCode,
			Output:    out.Output,
			Error:     out.Error,
		}
	}
}

// scriptAuditHook writes one script_run record per clock audit event. The
// family's redaction scrubs Command, Dir, OutputTail and Error on write.
func scriptAuditHook(sink *audit.Sink) func(schedule.ScriptAudit) {
	return func(rec schedule.ScriptAudit) {
		_ = sink.Record(audit.FamilyScriptRun, "", "", "", "", audit.ScriptRunPayload{
			ScheduleID:  rec.ScheduleID,
			RunID:       rec.RunID,
			Status:      rec.Status,
			Decision:    rec.Decision,
			Mode:        rec.Mode,
			Sandbox:     rec.Sandbox,
			Command:     rec.Command,
			CommandHash: rec.CommandHash,
			Dir:         rec.Dir,
			StartedAt:   rec.StartedAt,
			EndedAt:     rec.EndedAt,
			ExitCode:    rec.ExitCode,
			Timeout:     rec.Timeout,
			OutputTail:  rec.OutputTail,
			Error:       rec.Error,
		})
	}
}

func workspacePath(ws *workspace.Store, id string) (string, bool) {
	id = strings.TrimSpace(id)
	if ws == nil || id == "" {
		return "", false
	}
	list, _ := ws.List()
	for _, w := range list {
		if w.ID == id && strings.TrimSpace(w.Path) != "" {
			return w.Path, true
		}
	}
	return "", false
}

func workspaceMode(ws *workspace.Store, id string) (string, bool) {
	id = strings.TrimSpace(id)
	if ws == nil || id == "" {
		return "", false
	}
	list, _ := ws.List()
	for _, w := range list {
		if w.ID == id && strings.TrimSpace(w.PermissionMode) != "" {
			return w.PermissionMode, true
		}
	}
	return "", false
}
