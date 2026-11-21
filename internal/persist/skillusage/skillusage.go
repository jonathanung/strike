// Package skillusage counts how often each skill ran, derived from the
// recorded session JSONL logs on this gateway (#1442). Nothing new is
// written. Counts are computed on demand and stay local.
//
// Two recorded shapes count as one run:
//   - a user.message whose text starts with /name or $name, where name is a
//     known skill. The engine records the user's raw input as the message
//     text when the composer expands a skill, and an unknown /command never
//     becomes a user.message.
//   - a tool.begin for the skill tool whose args name a known skill. A call
//     that only loads an adjacent resource file (args.resource set) is a
//     follow-up read of a skill already loaded, not a new run.
//
// Child-agent runs count toward the same total. A child event that is also
// copied onto its parent's log is counted once: events are deduped on their
// own session id plus call id (tool) or turn id and time (message).
package skillusage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike/pkg/protocol"
)

// Usage is one skill's run count and the time of its latest run.
type Usage struct {
	Count    int       `json:"count"`
	LastUsed time.Time `json:"lastUsed,omitempty"`
}

// Report is per-skill usage across every session log in a directory.
// Skills lists every requested name, including names that never ran.
type Report struct {
	Skills       map[string]Usage `json:"skills"`
	Sessions     int              `json:"sessions"`
	SkippedLines int              `json:"skippedLines,omitempty"`
}

// lineLimit matches the session reader's ceiling: a longer line (a huge tool
// argument or image) is skipped rather than failing the whole report.
var lineLimit = 32 << 20

// Aggregate scans dir/*.jsonl and counts runs of the named skills. A missing
// directory is an empty report, not an error.
func Aggregate(dir string, names []string) (Report, error) {
	report := Report{Skills: make(map[string]Usage, len(names))}
	known := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		known[n] = struct{}{}
		report.Skills[n] = Usage{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	seen := map[string]struct{}{}
	for _, ent := range entries {
		name := ent.Name()
		if !ent.Type().IsRegular() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if id == "" {
			continue
		}
		report.Sessions++
		skipped, err := scanFile(filepath.Join(dir, name), id, known, seen, report.Skills)
		report.SkippedLines += skipped
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func scanFile(path, fileID string, known map[string]struct{}, seen map[string]struct{}, out map[string]Usage) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	skipped := 0
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, tooLong, err := readCappedLine(br, lineLimit)
		if tooLong {
			skipped++
		} else if line := strings.TrimSpace(raw); line != "" {
			if !applyLine(line, fileID, known, seen, out) {
				skipped++
			}
		}
		if errors.Is(err, io.EOF) {
			return skipped, nil
		}
		if err != nil {
			return skipped, err
		}
	}
}

// applyLine counts one envelope. It returns false only for an undecodable
// line; lines that are not skill runs return true.
func applyLine(line, fileID string, known map[string]struct{}, seen map[string]struct{}, out map[string]Usage) bool {
	var env protocol.Envelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return false
	}
	switch env.Type {
	case "user.message":
		var ev protocol.UserMessage
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false
		}
		name := InvokedSkill(ev.Text)
		if _, ok := known[name]; !ok {
			return true
		}
		key := "msg\x00" + sessionOf(ev.SessionID, fileID) + "\x00" + ev.TurnID + "\x00" + env.Time.UTC().Format(time.RFC3339Nano)
		record(out, seen, key, name, env.Time)
	case "tool.begin":
		var ev protocol.ToolCallBegin
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false
		}
		if ev.Name != "skill" {
			return true
		}
		var args struct {
			Name     string `json:"name"`
			Resource string `json:"resource"`
		}
		if err := json.Unmarshal(ev.Args, &args); err != nil {
			return true
		}
		name := strings.TrimSpace(args.Name)
		if _, ok := known[name]; !ok || strings.TrimSpace(args.Resource) != "" {
			return true
		}
		key := "tool\x00" + sessionOf(ev.SessionID, fileID) + "\x00" + ev.CallID
		if ev.CallID == "" {
			key += "\x00" + env.Time.UTC().Format(time.RFC3339Nano)
		}
		record(out, seen, key, name, env.Time)
	}
	return true
}

func sessionOf(eventSession, fileID string) string {
	if s := strings.TrimSpace(eventSession); s != "" {
		return s
	}
	return fileID
}

func record(out map[string]Usage, seen map[string]struct{}, key, name string, at time.Time) {
	if _, dup := seen[key]; dup {
		return
	}
	seen[key] = struct{}{}
	u := out[name]
	u.Count++
	if at.After(u.LastUsed) {
		u.LastUsed = at.UTC()
	}
	out[name] = u
}

// InvokedSkill returns the skill name a composer line invokes: the first
// token of "/name …" or "$name …", without the sigil. It does not check
// that the name is a real skill.
func InvokedSkill(text string) string {
	text = strings.TrimSpace(text)
	if len(text) < 2 || (text[0] != '/' && text[0] != '$') {
		return ""
	}
	fields := strings.Fields(text[1:])
	if len(fields) == 0 || text[1] == ' ' {
		return ""
	}
	return fields[0]
}

func readCappedLine(r *bufio.Reader, max int) (string, bool, error) {
	var b strings.Builder
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if b.Len()+len(chunk) > max {
				tooLong = true
				b.Reset()
			} else {
				b.Write(chunk)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return "", true, err
		}
		return b.String(), false, err
	}
}
