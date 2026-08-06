package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/issue"
)

// IssueStore is the durable project issue surface used by issue tools.
type IssueStore interface {
	Get(id int) (issue.Issue, bool, error)
	List(status string) ([]issue.Issue, error)
	Create(title, body string) (issue.Issue, error)
	Update(id int, title, body, status *string) (issue.Issue, error)
	CloseIssue(id int) (issue.Issue, error)
}

type issueWriteTool struct {
	store IssueStore
}

// NewIssueWrite builds the issue_write tool. store must be non-nil.
func NewIssueWrite(store IssueStore) Tool {
	return &issueWriteTool{store: store}
}

func (t *issueWriteTool) Name() string { return "issue_write" }

func (t *issueWriteTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyConditional)
}

func (t *issueWriteTool) Description() string {
	return `Create or update a project-local tracked issue that persists across sessions.

Use this to file work items, bugs, or follow-ups for the current project as you
work. Issues have numeric ids and open/closed status.

Usage notes:
  - Omit id to create a new open issue (title required; body optional).
  - Provide id to update an existing issue; only supplied fields change.
  - status may be "open" or "closed". Prefer status=closed to close.
  - Issues are project-scoped only — never global across repos.`
}

func (t *issueWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "integer", "description": "Existing issue id to update; omit to create"},
			"title": {"type": "string", "description": "Issue title (required on create)"},
			"body": {"type": "string", "description": "Optional issue body/details"},
			"status": {"type": "string", "enum": ["open", "closed"], "description": "Issue status"}
		}
	}`)
}

func (t *issueWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("issue store is unavailable")
	}
	var in struct {
		ID     *int    `json:"id"`
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Status *string `json:"status"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}

	pattern := "create"
	if in.ID != nil {
		pattern = fmt.Sprintf("%d", *in.ID)
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "issue_write",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	var (
		iss issue.Issue
		err error
	)
	if in.ID == nil {
		title := ""
		if in.Title != nil {
			title = *in.Title
		}
		body := ""
		if in.Body != nil {
			body = *in.Body
		}
		iss, err = t.store.Create(title, body)
		if err != nil {
			return Result{}, err
		}
		if in.Status != nil && strings.TrimSpace(*in.Status) != "" && *in.Status != issue.StatusOpen {
			iss, err = t.store.Update(iss.ID, nil, nil, in.Status)
			if err != nil {
				return Result{}, err
			}
		}
	} else {
		if in.Title == nil && in.Body == nil && in.Status == nil {
			return Result{}, errors.New("provide title, body, and/or status to update")
		}
		iss, err = t.store.Update(*in.ID, in.Title, in.Body, in.Status)
		if err != nil {
			if errors.Is(err, issue.ErrNotFound) {
				return Result{
					Title:  "issue miss",
					Output: fmt.Sprintf("no issue #%d", *in.ID),
				}, nil
			}
			return Result{}, err
		}
	}

	out, err := json.MarshalIndent(iss, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{
		"id":     iss.ID,
		"title":  iss.Title,
		"status": iss.Status,
	})
	return Result{
		Title:    fmt.Sprintf("issue #%d %s", iss.ID, iss.Status),
		Output:   string(out),
		Metadata: meta,
	}, nil
}

type issueReadTool struct {
	store IssueStore
}

// NewIssueRead builds the issue_read tool. store must be non-nil.
func NewIssueRead(store IssueStore) Tool {
	return &issueReadTool{store: store}
}

func (t *issueReadTool) Name() string { return "issue_read" }

func (t *issueReadTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *issueReadTool) Description() string {
	return `Read project-local tracked issues that persist across sessions.

Usage notes:
  - Provide id to fetch one issue.
  - Omit id to list issues; optional status filters to open or closed.
  - Returns JSON. Empty list when nothing matches.`
}

func (t *issueReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "integer", "description": "Fetch a single issue by id"},
			"status": {"type": "string", "enum": ["open", "closed"], "description": "When listing, only issues with this status"}
		}
	}`)
}

func (t *issueReadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if t.store == nil {
		return Result{}, errors.New("issue store is unavailable")
	}
	var in struct {
		ID     *int   `json:"id"`
		Status string `json:"status"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	status := strings.TrimSpace(in.Status)

	pattern := "*"
	if in.ID != nil {
		pattern = fmt.Sprintf("%d", *in.ID)
	} else if status != "" {
		pattern = "status:" + status
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "issue_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	if in.ID != nil {
		iss, ok, err := t.store.Get(*in.ID)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{
				Title:  "issue miss",
				Output: fmt.Sprintf("no issue #%d", *in.ID),
			}, nil
		}
		out, err := json.MarshalIndent(iss, "", "  ")
		if err != nil {
			return Result{}, err
		}
		meta, _ := json.Marshal(map[string]any{"id": iss.ID, "issue": iss})
		return Result{
			Title:    fmt.Sprintf("issue #%d", iss.ID),
			Output:   string(out),
			Metadata: meta,
		}, nil
	}

	issues, err := t.store.List(status)
	if err != nil {
		return Result{}, err
	}
	if issues == nil {
		issues = []issue.Issue{}
	}
	out, err := json.MarshalIndent(issues, "", "  ")
	if err != nil {
		return Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{"count": len(issues), "status": status, "issues": issues})
	title := fmt.Sprintf("%d issues", len(issues))
	if status != "" {
		title = fmt.Sprintf("%d issues status:%s", len(issues), status)
	}
	return Result{
		Title:    title,
		Output:   string(out),
		Metadata: meta,
	}, nil
}
