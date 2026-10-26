package main

import (
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/schedule"
)

// openScheduleService loads the clock next to the workspace registry.
// A nil hub leaves fires unavailable (attach-only) but the registry still
// edits. The starter is the same rootstart.Starter triggers should call.
func openScheduleService(dir string, hub *server.LiveHub, services *host.Services, ws *workspace.Store, cwd string) (*schedule.Service, error) {
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
	return schedule.Open(dir, hooks, cwd)
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
