package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

type sessionsResponse struct {
	Sessions []sessionListItem `json:"sessions"`
	LiveID   string            `json:"liveId,omitempty"`
}

type sessionListItem struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Mtime int64  `json:"mtime,omitempty"`
}

type agentsResponse struct {
	Agents []AgentInfo `json:"agents"`
}

type opErrorResponse struct {
	Error string `json:"error"`
}

type opOKResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Live.Status())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		writeJSON(w, http.StatusOK, agentsResponse{Agents: nil})
		return
	}
	writeJSON(w, http.StatusOK, agentsResponse{Agents: s.opts.Live.Agents()})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	items, err := listSessionFiles(s.opts.SessionDir)
	if err != nil {
		http.Error(w, "session list unavailable", http.StatusInternalServerError)
		return
	}
	liveID := ""
	if s.opts.Live != nil {
		liveID = s.opts.Live.SessionID()
	}
	writeJSON(w, http.StatusOK, sessionsResponse{Sessions: items, LiveID: liveID})
}

func listSessionFiles(dir string) ([]sessionListItem, error) {
	mgr := session.NewManager(dir)
	list, err := mgr.List()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]sessionListItem, 0, len(list))
	for _, info := range list {
		if info.ParentSessionID != "" {
			continue // roots only in switcher
		}
		out = append(out, sessionListItem{
			ID:    info.ID,
			Title: info.Title,
			Mtime: info.UpdatedAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mtime != out[j].Mtime {
			return out[i].Mtime > out[j].Mtime
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Server) handleOps(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	var env protocol.OpEnvelope
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid op envelope: " + err.Error()})
		return
	}
	op, err := env.Decode()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.opts.Live.Submit(ctx, op); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	// Subscribe before backlog so concurrent publishes are not dropped.
	ch := s.opts.Live.Subscribe(ctx)
	// Replay durable backlog for the live session id when the log exists.
	id := s.opts.Live.SessionID()
	path := session.LogPath(s.opts.SessionDir, id)
	if _, err := os.Stat(path); err == nil {
		if _, err := s.writeEventsFrom(ctx, w, flusher, path, 0); err != nil {
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			env, err := protocol.Wrap(ev)
			if err != nil {
				continue
			}
			payload, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if _, err := w.Write(payload); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	ctx := r.Context()
	// Subscribe before backlog/status so live publishes during replay are not lost.
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := s.opts.Live.Subscribe(subCtx)

	// Hello: push current status as a synthetic control message.
	statusEnv := map[string]any{
		"type": "status",
		"data": s.opts.Live.Status(),
	}
	if b, err := json.Marshal(statusEnv); err == nil {
		_ = ws.WriteText(string(b))
	}

	// Replay backlog then stream live.
	id := s.opts.Live.SessionID()
	path := session.LogPath(s.opts.SessionDir, id)
	if _, err := os.Stat(path); err == nil {
		_ = s.writeWSBacklog(ctx, ws, path)
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			text, err := ws.ReadText()
			if err != nil {
				errCh <- err
				return
			}
			var env protocol.OpEnvelope
			if err := json.Unmarshal([]byte(text), &env); err != nil {
				_ = ws.WriteText(`{"type":"error","data":{"message":"invalid json"}}`)
				continue
			}
			// Allow status request control message.
			if env.Type == "status.get" {
				b, _ := json.Marshal(map[string]any{"type": "status", "data": s.opts.Live.Status()})
				_ = ws.WriteText(string(b))
				continue
			}
			op, err := env.Decode()
			if err != nil {
				msg, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]string{"message": err.Error()}})
				_ = ws.WriteText(string(msg))
				continue
			}
			submitCtx, submitCancel := context.WithTimeout(ctx, 5*time.Second)
			err = s.opts.Live.Submit(submitCtx, op)
			submitCancel()
			if err != nil {
				msg, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]string{"message": err.Error()}})
				_ = ws.WriteText(string(msg))
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-errCh:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			env, err := protocol.Wrap(ev)
			if err != nil {
				continue
			}
			payload, err := json.Marshal(env)
			if err != nil {
				continue
			}
			if err := ws.WriteText(string(payload)); err != nil {
				return
			}
		}
	}
}

func (s *Server) writeWSBacklog(ctx context.Context, ws *wsConn, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Reuse line reader via writeEventsFrom pattern — simple scan.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_ = f
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" || !json.Valid([]byte(line)) {
			continue
		}
		if err := ws.WriteText(line); err != nil {
			return err
		}
	}
	return nil
}
