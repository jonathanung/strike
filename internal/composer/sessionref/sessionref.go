// Package sessionref reads another session's log for an @session
// reference (#1550): the latest compaction summary plus the last N
// user/assistant turns, never the raw full JSONL.
package sessionref

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/jonathanung/strike/internal/composer/triggers"
	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
)

// DefaultTurns is the default "last N turns" (open question Q2).
const DefaultTurns = 10

// validID rejects ids that could escape the session directory.
func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

// Read loads the reference for id from the session directory dir.
// A missing log is triggers.ErrSessionNotFound.
func Read(dir, id string, turns int) (triggers.SessionRef, error) {
	if !validID(id) {
		return triggers.SessionRef{}, errors.New("invalid session id")
	}
	if turns <= 0 {
		turns = DefaultTurns
	}
	path := session.LogPath(dir, id)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return triggers.SessionRef{}, triggers.ErrSessionNotFound
		}
		return triggers.SessionRef{}, err
	}
	defer f.Close()
	ref := triggers.SessionRef{ID: id}
	if st, err := f.Stat(); err == nil {
		ref.Updated = st.ModTime()
	}
	var all []triggers.SessionTurn
	var assistant strings.Builder
	flush := func() {
		text := strings.TrimSpace(assistant.String())
		assistant.Reset()
		if text != "" {
			all = append(all, triggers.SessionTurn{Role: "Assistant", Text: text})
		}
	}
	sc := bufio.NewScanner(f)
	// user.message lines can carry multi-MiB images (same cap as the
	// session log reader).
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
			if t := strings.TrimSpace(msg.Text); t != "" {
				all = append(all, triggers.SessionTurn{Role: "User", Text: t})
			}
		case "text.delta":
			var d struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(env.Data, &d)
			assistant.WriteString(d.Text)
		case "turn.completed", "turn.started", "tool.begin":
			flush()
		case "session.titled":
			var t struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(env.Data, &t)
			if s := strings.TrimSpace(t.Title); s != "" {
				ref.Title = s
			}
		case "compaction.completed":
			var c struct {
				Summary string `json:"summary"`
			}
			_ = json.Unmarshal(env.Data, &c)
			if s := strings.TrimSpace(c.Summary); s != "" {
				ref.Summary = s
			}
		}
	}
	if err := sc.Err(); err != nil {
		return triggers.SessionRef{}, err
	}
	flush()
	if len(all) > turns {
		ref.Omitted = len(all) - turns
		all = all[len(all)-turns:]
	}
	ref.Turns = all
	return ref, nil
}

// Reader binds Read to a directory and turn count for triggers.Sessions.
func Reader(dir string, turns int) func(id string) (triggers.SessionRef, error) {
	return func(id string) (triggers.SessionRef, error) {
		return Read(dir, id, turns)
	}
}
