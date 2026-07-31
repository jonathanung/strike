package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// teamTask handles team_task tool actions against the shared lead-scoped board.
func (e *Engine) teamTask(ctx context.Context, req tool.TeamTaskRequest) (tool.TeamTaskResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TeamTaskResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.TeamTaskResult{}, fmt.Errorf("team_task is not available (no team)")
	}
	if e.team.Len() == 0 {
		return tool.TeamTaskResult{}, fmt.Errorf("team_task is not available (no team)")
	}
	actor := strings.TrimSpace(e.opts.SessionID)
	if actor == "" {
		return tool.TeamTaskResult{}, fmt.Errorf("session id is required")
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	out := tool.TeamTaskResult{
		LeadID: e.team.LeadID(),
		Action: action,
	}

	switch action {
	case "list":
		out.Tasks = boardTasksToTool(e.team.Board())
		return out, nil

	case "create":
		item, err := e.team.CreateBoardTask(req.Content, actor)
		if err != nil {
			return tool.TeamTaskResult{}, err
		}
		out.Task = boardTaskToTool(item)
		out.Tasks = boardTasksToTool(e.team.Board())
		return out, nil

	case "claim":
		item, err := e.team.ClaimBoardTask(req.ID, actor, req.ExpectedVersion)
		if err != nil {
			return boardConflictResult(out, err)
		}
		out.Task = boardTaskToTool(item)
		out.Tasks = boardTasksToTool(e.team.Board())
		return out, nil

	case "update":
		var contentPtr *string
		if strings.TrimSpace(req.Content) != "" || req.ContentSet {
			c := req.Content
			contentPtr = &c
		}
		var statusPtr *string
		if s := strings.TrimSpace(req.Status); s != "" {
			statusPtr = &s
		}
		item, err := e.team.UpdateBoardTask(req.ID, actor, contentPtr, statusPtr, req.ExpectedVersion)
		if err != nil {
			return boardConflictResult(out, err)
		}
		out.Task = boardTaskToTool(item)
		out.Tasks = boardTasksToTool(e.team.Board())
		return out, nil

	case "complete":
		item, err := e.team.CompleteBoardTask(req.ID, actor, req.ExpectedVersion)
		if err != nil {
			return boardConflictResult(out, err)
		}
		out.Task = boardTaskToTool(item)
		out.Tasks = boardTasksToTool(e.team.Board())
		return out, nil

	default:
		return tool.TeamTaskResult{}, fmt.Errorf("action must be create, list, update, claim, or complete")
	}
}

func boardConflictResult(out tool.TeamTaskResult, err error) (tool.TeamTaskResult, error) {
	var conf *BoardConflictError
	if errors.As(err, &conf) {
		out.Conflict = true
		out.Detail = conf.Reason
		out.Task = boardTaskToTool(conf.Task)
		return out, nil
	}
	return tool.TeamTaskResult{}, err
}

func boardTaskToTool(item BoardTask) *tool.TeamTaskItem {
	out := &tool.TeamTaskItem{
		ID:        item.ID,
		Content:   item.Content,
		Status:    item.Status,
		Owner:     item.Owner,
		Version:   item.Version,
		CreatedBy: item.CreatedBy,
	}
	if !item.UpdatedAt.IsZero() {
		out.UpdatedAt = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func boardTasksToTool(items []BoardTask) []tool.TeamTaskItem {
	if items == nil {
		return []tool.TeamTaskItem{}
	}
	out := make([]tool.TeamTaskItem, 0, len(items))
	for _, item := range items {
		out = append(out, *boardTaskToTool(item))
	}
	return out
}
