package local

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/session"
)

// NewSessions wraps a session.Manager as host.Sessions.
func NewSessions(m *session.Manager) host.Sessions {
	if m == nil {
		return nil
	}
	return sessionsAdapter{m: m}
}

type sessionsAdapter struct {
	m *session.Manager
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
	all, err := s.m.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.Session, 0, len(all))
	for _, info := range all {
		if rootsOnly && info.ParentSessionID != "" {
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

func toHostSession(info session.Info) host.Session {
	return host.Session{
		ID:        info.ID,
		ParentID:  info.ParentSessionID,
		Title:     info.Title,
		Open:      info.Open,
		UpdatedAt: info.UpdatedAt,
	}
}
