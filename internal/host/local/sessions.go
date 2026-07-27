package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/session"
)

// NewSessions wraps a session.Manager as host.Sessions. projectKey scopes
// List to the current launch project (same identity as history/memory). Empty
// projectKey disables filtering (tests).
func NewSessions(m *session.Manager, projectKey string) host.Sessions {
	if m == nil {
		return nil
	}
	return sessionsAdapter{
		m:          m,
		projectKey: strings.TrimSpace(projectKey),
		viewPR:     defaultViewGitHubPR,
	}
}

// sessionsAdapter implements host.Sessions, host.AllProjectsSessions, and
// host.PRStateRefresher.
type sessionsAdapter struct {
	m          *session.Manager
	projectKey string
	viewPR     func(ctx context.Context, number int, url string) (state string, err error)
}

func (s sessionsAdapter) Get(id string) (host.Session, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return host.Session{}, false, nil
	}
	info, err := s.m.Get(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return host.Session{}, false, nil
		}
		return host.Session{}, false, err
	}
	return toHostSession(info), true, nil
}

func (s sessionsAdapter) List(rootsOnly bool) ([]host.Session, error) {
	return s.list(rootsOnly, false)
}

// ListAllProjects implements host.AllProjectsSessions.
func (s sessionsAdapter) ListAllProjects(rootsOnly bool) ([]host.Session, error) {
	return s.list(rootsOnly, true)
}

func (s sessionsAdapter) list(rootsOnly, allProjects bool) ([]host.Session, error) {
	all, err := s.m.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.Session, 0, len(all))
	for _, info := range all {
		if rootsOnly && info.ParentSessionID != "" {
			continue
		}
		if !allProjects && !session.BelongsToProject(info, s.projectKey) {
			continue
		}
		out = append(out, toHostSession(info))
	}
	return out, nil
}

func (s sessionsAdapter) Children(parentID string) ([]host.Session, error) {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil, nil
	}
	all, err := s.m.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.Session, 0)
	for _, info := range all {
		if info.ParentSessionID == parentID {
			out = append(out, toHostSession(info))
		}
	}
	return out, nil
}

func (s sessionsAdapter) ReplayJSONL(id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("session id is empty")
	}
	path := s.m.Path(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q not found", id)
		}
		return nil, err
	}
	return data, nil
}

func (s sessionsAdapter) Fork(id string) (host.Session, error) {
	return s.ForkAt(id, -1)
}

func (s sessionsAdapter) ForkAt(id string, keepEvents int) (host.Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return host.Session{}, fmt.Errorf("session id is empty")
	}
	info, err := s.m.ForkAt(id, keepEvents)
	if err != nil {
		return host.Session{}, err
	}
	return toHostSession(info), nil
}

func (s sessionsAdapter) Rename(id, title string) (host.Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return host.Session{}, fmt.Errorf("session id is empty")
	}
	info, err := s.m.Rename(id, title)
	if err != nil {
		return host.Session{}, err
	}
	return toHostSession(info), nil
}

func (s sessionsAdapter) Delete(id string, force bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session id is empty")
	}
	return s.m.Delete(id, force)
}

func toHostSession(info session.Info) host.Session {
	return host.Session{
		ID:         info.ID,
		ParentID:   info.ParentSessionID,
		Title:      info.Title,
		Open:       info.Open,
		UpdatedAt:  info.UpdatedAt,
		ProjectKey: info.ProjectKey,
		PRURL:      info.PRURL,
		PRNumber:   info.PRNumber,
		PRState:    session.NormalizePRState(info.PRState),
	}
}

// RefreshPRStates implements host.PRStateRefresher. Best-effort gh pr view;
// failures leave the row unchanged. No tokens are included in returned data.
func (s sessionsAdapter) RefreshPRStates(in []host.Session) []host.Session {
	if len(in) == 0 {
		return in
	}
	out := make([]host.Session, len(in))
	copy(out, in)
	view := s.viewPR
	if view == nil {
		view = defaultViewGitHubPR
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for i := range out {
		sess := out[i]
		if sess.PRURL == "" && sess.PRNumber == 0 {
			continue
		}
		state, err := view(ctx, sess.PRNumber, sess.PRURL)
		if err != nil {
			continue
		}
		state = session.NormalizePRState(state)
		if state == "" || state == sess.PRState {
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = session.UpdateMeta(s.m.Dir(), sess.ID, func(meta *session.Meta) {
			meta.PRState = state
			if sess.PRURL != "" {
				meta.PRURL = sess.PRURL
			}
			if sess.PRNumber != 0 {
				meta.PRNumber = sess.PRNumber
			}
			meta.PRUpdatedAt = now
		})
		if err != nil {
			continue
		}
		out[i].PRState = state
	}
	return out
}

type ghPRViewJSON struct {
	State  string `json:"state"`
	URL    string `json:"url"`
	Number int    `json:"number"`
}

func defaultViewGitHubPR(ctx context.Context, number int, url string) (string, error) {
	args := []string{"pr", "view", "--json", "state,url,number"}
	switch {
	case number > 0:
		args = append(args, fmt.Sprintf("%d", number))
	case strings.TrimSpace(url) != "":
		args = append(args, url)
	default:
		return "", fmt.Errorf("no pr identity")
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Never return stderr (may contain auth hints); keep error generic.
		return "", fmt.Errorf("gh pr view failed")
	}
	var parsed ghPRViewJSON
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return "", fmt.Errorf("gh pr view parse failed")
	}
	return parsed.State, nil
}
