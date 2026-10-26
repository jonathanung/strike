package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

const (
	// sideChatReadOnlyCode is the stable refusal for any protocol op posted
	// on the side-chat channel, including permission.reply.
	sideChatReadOnlyCode = "sidechat_read_only"

	sideChatSystem = `You are Strike side chat, a read-only observer of one running session.
Answer the user's question using the session transcript as context.
You cannot approve tools, edit files, run commands, steer the agent, or send user.input.
If the user asks you to approve, run, or change the session, refuse in one sentence.
Do not invent tool results that are not in the transcript.`

	sideChatMaxQuestion = 4000
	sideChatMaxRunes    = 8000
	sideChatHistory     = 8
)

// SideChatTurn is one read-only question and its answer. It is stored beside
// the session log, never as a session event, so it cannot become user.input.
type SideChatTurn struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	Answer    string `json:"answer,omitempty"`
	At        string `json:"at"`
	FocusName string `json:"focusName,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

// SideChatPrompt is the context for one read-only model call.
type SideChatPrompt struct {
	System     string
	Transcript string
	History    []SideChatTurn
	Question   string
	// Provider and Model are the live root's current selection. Empty keeps
	// the boot provider and its default model.
	Provider string
	Model    string
}

// SideChatCompletion is prose from that call. Tool calls are not a completion.
type SideChatCompletion struct {
	Text     string
	Provider string
	Model    string
}

// SideChatModel answers one question. Implementations must not execute tools
// or submit protocol ops. A nil model makes ask return 503; the ops route
// still refuses approvals.
type SideChatModel interface {
	Answer(ctx context.Context, in SideChatPrompt) (SideChatCompletion, error)
}

type sideChatStore struct {
	mu sync.Mutex
}

func sideChatPath(dir, id string) string {
	// A sibling *.jsonl is a session. Keep turns in a directory so
	// session.Manager.List does not offer the sidecar as a root.
	return filepath.Join(dir, "sidechat", id+".jsonl")
}

func sideChatRefusal(opType string) string {
	switch strings.TrimSpace(opType) {
	case "permission.reply":
		return "side chat cannot approve tools"
	case "user.input":
		return "side chat cannot post user.input to the session"
	case "steer":
		return "side chat cannot steer the running turn"
	case "question.reply":
		return "side chat cannot answer a pending question for the session"
	case "interrupt":
		return "side chat cannot interrupt the session"
	case "":
		return "side chat is read-only"
	default:
		return "side chat is read-only and cannot apply " + strings.TrimSpace(opType)
	}
}

func (s *Server) refuseSideChat(w http.ResponseWriter, opType string) {
	writeJSON(w, http.StatusForbidden, opErrorResponse{
		Error: sideChatRefusal(opType),
		Code:  sideChatReadOnlyCode,
	})
}

// handleSideChatOps is the side-chat op ingress. Every protocol op is refused.
// The handler never calls Live.Submit, so a permission.reply posted here cannot
// approve a tool on the main session.
func (s *Server) handleSideChatOps(w http.ResponseWriter, r *http.Request) {
	body, err := readSideChatBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid side chat op: " + err.Error()})
		return
	}
	var env protocol.OpEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid side chat op: " + err.Error()})
		return
	}
	if strings.TrimSpace(env.Type) == "" {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(body, &probe)
		env.Type = probe.Type
	}
	s.refuseSideChat(w, env.Type)
}

type sideChatAskBody struct {
	Type           string `json:"type"`
	Text           string `json:"text"`
	FocusSessionID string `json:"focusSessionId"`
	FocusName      string `json:"focusName"`
}

func (s *Server) handleSideChatAsk(w http.ResponseWriter, r *http.Request) {
	body, err := readSideChatBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid side chat request: " + err.Error()})
		return
	}
	if opType, refused := sideChatOpShape(body); refused {
		s.refuseSideChat(w, opType)
		return
	}
	var ask sideChatAskBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ask); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid side chat request: " + err.Error()})
		return
	}
	if err := ensureEOF(dec); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(ask.Type) != "" {
		s.refuseSideChat(w, ask.Type)
		return
	}
	question := strings.TrimSpace(ask.Text)
	if question == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "question is empty"})
		return
	}
	if utf8.RuneCountInString(question) > sideChatMaxQuestion {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "question is too long"})
		return
	}
	focusID := strings.TrimSpace(ask.FocusSessionID)
	if focusID != "" {
		if err := validateSessionID(focusID); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid focus session"})
			return
		}
	}
	focusName := clip(strings.Join(strings.Fields(ask.FocusName), " "), 200)

	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.opts.SideChat == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "side chat model is not configured"})
		return
	}
	sessionID := live.SessionID()
	if err := validateSessionID(sessionID); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	if focusID != "" && !sideChatFocusAllowed(live, s.opts.SessionDir, focusID) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "focus session is not part of this session"})
		return
	}
	status := live.Status()

	transcript, err := s.sideChatTranscript(sessionID, focusID, focusName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "reading session transcript: " + err.Error()})
		return
	}
	history := s.readSideChatTurns(sessionID)
	if len(history) > sideChatHistory {
		history = history[len(history)-sideChatHistory:]
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	completion, err := s.opts.SideChat.Answer(ctx, SideChatPrompt{
		System:     sideChatSystem,
		Transcript: transcript,
		History:    history,
		Question:   question,
		Provider:   status.Provider,
		Model:      status.Model,
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	answer := strings.TrimSpace(completion.Text)
	if answer == "" {
		writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: "side chat model returned no text"})
		return
	}
	turn, err := s.appendSideChatTurn(sessionID, SideChatTurn{
		Question:  question,
		Answer:    clip(answer, sideChatMaxRunes),
		At:        time.Now().UTC().Format(time.RFC3339),
		FocusName: focusName,
		Provider:  completion.Provider,
		Model:     completion.Model,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "saving side chat: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        turn.ID,
		"question":  turn.Question,
		"answer":    turn.Answer,
		"readOnly":  true,
		"provider":  completion.Provider,
		"model":     completion.Model,
		"focusName": turn.FocusName,
	})
}

func (s *Server) handleSideChatList(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := live.SessionID()
	if err := validateSessionID(sessionID); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	turns := s.readSideChatTurns(sessionID)
	if turns == nil {
		turns = []SideChatTurn{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"readOnly":  true,
		"sessionId": sessionID,
		"turns":     turns,
	})
}

// sideChatOpShape reports a protocol op smuggled onto the ask route.
// A question body has text and no type. An op envelope has a type.
func sideChatOpShape(body []byte) (string, bool) {
	var loose map[string]json.RawMessage
	if err := json.Unmarshal(body, &loose); err != nil {
		return "", false
	}
	raw, ok := loose["type"]
	if !ok {
		return "", false
	}
	var typ string
	if err := json.Unmarshal(raw, &typ); err != nil {
		return "", true
	}
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "", false
	}
	return typ, true
}

func readSideChatBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("empty body")
	}
	return body, nil
}

// sideChatFocusAllowed is the live root itself or a child on its roster.
// Any other id in the session directory is another root's log.
func sideChatFocusAllowed(live *Live, dir, focusID string) bool {
	if focusID == "" {
		return true
	}
	if live == nil {
		return false
	}
	if focusID == live.SessionID() {
		return true
	}
	for _, member := range live.Team().Members {
		if member.SessionID == focusID {
			return true
		}
	}
	meta, err := session.ReadMeta(dir, focusID)
	if err != nil {
		return false
	}
	root := live.SessionID()
	return meta.ParentSessionID == root || meta.LeadSessionID == root
}

func (s *Server) sideChatTranscript(sessionID, focusID, focusName string) (string, error) {
	var b strings.Builder
	if focusName != "" || focusID != "" {
		b.WriteString("Opened from activity")
		if focusName != "" {
			b.WriteString(" row ")
			b.WriteString(focusName)
		}
		if focusID != "" {
			b.WriteString(" session ")
			b.WriteString(focusID)
		}
		b.WriteString(".\n")
	}
	main, err := sessionTranscript(session.LogPath(s.opts.SessionDir, sessionID), sideChatMaxRunes)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(main) == "" {
		b.WriteString("(no session events yet)\n")
	} else {
		b.WriteString(main)
	}
	if focusID != "" && focusID != sessionID {
		child, err := sessionTranscript(session.LogPath(s.opts.SessionDir, focusID), sideChatMaxRunes/2)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(child) != "" {
			b.WriteString("\nAgent transcript:\n")
			b.WriteString(child)
		}
	}
	return tailRunes(b.String(), sideChatMaxRunes), nil
}

func (s *Server) readSideChatTurns(sessionID string) []SideChatTurn {
	s.sideChat.mu.Lock()
	defer s.sideChat.mu.Unlock()
	f, err := os.Open(sideChatPath(s.opts.SessionDir, sessionID))
	if err != nil {
		return nil
	}
	defer f.Close()
	var turns []SideChatTurn
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var turn SideChatTurn
		if json.Unmarshal(line, &turn) != nil || turn.ID == "" {
			continue
		}
		turns = append(turns, turn)
	}
	return turns
}

func (s *Server) appendSideChatTurn(sessionID string, turn SideChatTurn) (SideChatTurn, error) {
	id, err := newSideChatID()
	if err != nil {
		return SideChatTurn{}, err
	}
	turn.ID = id
	raw, err := json.Marshal(turn)
	if err != nil {
		return SideChatTurn{}, err
	}
	s.sideChat.mu.Lock()
	defer s.sideChat.mu.Unlock()
	path := sideChatPath(s.opts.SessionDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return SideChatTurn{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return SideChatTurn{}, err
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return SideChatTurn{}, err
	}
	return turn, nil
}

func newSideChatID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func sessionTranscript(path string, maxRunes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	var assistant strings.Builder
	flush := func() {
		text := strings.TrimSpace(assistant.String())
		assistant.Reset()
		if text == "" {
			return
		}
		b.WriteString("Assistant: ")
		b.WriteString(clip(strings.Join(strings.Fields(text), " "), 500))
		b.WriteByte('\n')
	}
	sc := bufio.NewScanner(f)
	// Match the session log reader. user.message lines can carry multi-MiB
	// images; a 1 MiB cap fails the whole ask on the first such line.
	sc.Buffer(make([]byte, 64*1024), 32<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env protocol.Envelope
		if json.Unmarshal(line, &env) != nil || env.Type == "" {
			continue
		}
		switch env.Type {
		case "user.message":
			flush()
			var msg struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(env.Data, &msg)
			if strings.TrimSpace(msg.Text) != "" {
				b.WriteString("User: ")
				b.WriteString(clip(strings.Join(strings.Fields(msg.Text), " "), 500))
				b.WriteByte('\n')
			}
		case "text.delta":
			var d struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(env.Data, &d)
			assistant.WriteString(d.Text)
		case "turn.completed", "turn.started":
			flush()
		case "tool.begin":
			flush()
			var tool struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(env.Data, &tool)
			if strings.TrimSpace(tool.Name) != "" {
				b.WriteString("Tool: ")
				b.WriteString(clip(tool.Name, 80))
				b.WriteByte('\n')
			}
		case "tool.end":
			var tool struct {
				Title   string `json:"title"`
				Name    string `json:"name"`
				IsError bool   `json:"isError"`
			}
			_ = json.Unmarshal(env.Data, &tool)
			label := strings.TrimSpace(tool.Title)
			if label == "" {
				label = strings.TrimSpace(tool.Name)
			}
			if label == "" {
				continue
			}
			if tool.IsError {
				b.WriteString("Tool result (error): ")
			} else {
				b.WriteString("Tool result: ")
			}
			b.WriteString(clip(strings.Join(strings.Fields(label), " "), 240))
			b.WriteByte('\n')
		case "permission.asked":
			var asked struct {
				Permission string   `json:"permission"`
				Patterns   []string `json:"patterns"`
			}
			_ = json.Unmarshal(env.Data, &asked)
			b.WriteString("Approval waiting (side chat cannot answer it): ")
			b.WriteString(clip(asked.Permission, 80))
			if len(asked.Patterns) > 0 {
				b.WriteByte(' ')
				b.WriteString(clip(strings.Join(asked.Patterns, " "), 200))
			}
			b.WriteByte('\n')
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	flush()
	return tailRunes(b.String(), maxRunes), nil
}

func clip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func tailRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[len(r)-max:])
}
