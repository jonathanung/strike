package server

// Session report generation (#1444): POST /v1/sessions/{id}/report reads the
// session's JSONL record and stores a daemon-generated, session-scoped
// session_report artifact. Every figure comes from the record; anything not
// recorded is omitted rather than estimated. Tool arguments and tool output
// are never copied in (names and counts only), and the rendered body passes
// through pkg/redact before it is stored.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	reportMaxAsks     = 20
	reportMaxAskRunes = 160
	reportMaxFiles    = 200
	reportMaxChildren = 50
	reportMaxErrRunes = 200
)

// sessionReportFacts is the factual summary derived from one session record.
type sessionReportFacts struct {
	SessionID string
	Title     string
	Project   string
	Start     time.Time
	End       time.Time
	Models    []string

	TurnsStarted   int
	TurnsCompleted int
	StopReasons    map[string]int
	FinalStatus    string

	Asks []string

	ToolCalls  map[string]int
	ToolErrors int

	Files map[string]string // path -> last change kind

	ApprovalsAsked      int
	ApprovalsAllowed    int
	ApprovalsDenied     int
	ApprovalsUnresolved int
	PolicyAllowed       int
	PolicyDenied        int

	Children []reportChild

	Usage        map[string]int // input/output/cacheRead/cacheCreation
	UsageReports int
	UsageUnknown int

	Artifacts []string // "type id vN" refs touched during the session

	Errors    int
	LastError string
}

type reportChild struct {
	SessionID string
	Name      string
	Agent     string
	Status    string
}

// buildSessionReportFacts folds a raw JSONL session log into report facts.
// Events correlated to another session (child streams) only contribute to
// the children section.
func buildSessionReportFacts(sessionID string, raw []byte) (sessionReportFacts, error) {
	f := sessionReportFacts{
		SessionID:   sessionID,
		StopReasons: map[string]int{},
		ToolCalls:   map[string]int{},
		Files:       map[string]string{},
		Usage:       map[string]int{},
	}
	asked := map[string]bool{}
	resolved := map[string]bool{}
	seenModel := map[string]bool{}
	childIdx := map[string]int{}
	seenArtifact := map[string]int{}
	lastWasError := false

	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			return f, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if env.Type == "" || env.Type == "session.header" {
			continue
		}
		ev, err := env.Decode()
		if err != nil {
			return f, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if !env.Time.IsZero() {
			if f.Start.IsZero() || env.Time.Before(f.Start) {
				f.Start = env.Time
			}
			if env.Time.After(f.End) {
				f.End = env.Time
			}
		}

		// Child lifecycle events carry the child's correlation.
		switch e := ev.(type) {
		case protocol.ChildStarted:
			if e.SessionID != "" && e.SessionID != sessionID {
				if _, ok := childIdx[e.SessionID]; !ok {
					childIdx[e.SessionID] = len(f.Children)
					f.Children = append(f.Children, reportChild{SessionID: e.SessionID, Name: e.Name, Agent: e.Agent, Status: "running"})
				}
			}
			continue
		case protocol.ChildCompleted:
			if i, ok := childIdx[e.SessionID]; ok {
				f.Children[i].Status = string(e.Status)
			} else if e.SessionID != "" && e.SessionID != sessionID {
				childIdx[e.SessionID] = len(f.Children)
				f.Children = append(f.Children, reportChild{SessionID: e.SessionID, Name: e.Name, Status: string(e.Status)})
			}
			continue
		}
		if sid := envelopeSessionID(env.Data); sid != "" && sid != sessionID {
			continue
		}

		switch e := ev.(type) {
		case protocol.SessionTitled:
			f.Title = e.Title
		case protocol.ModelSelected:
			key := strings.Trim(e.Provider+"/"+e.Model, "/")
			if key != "" && !seenModel[key] {
				seenModel[key] = true
				f.Models = append(f.Models, key)
			}
		case protocol.UserMessage:
			if t := briefLine(e.Text, reportMaxAskRunes); t != "" {
				f.Asks = append(f.Asks, t)
			}
		case protocol.TurnStarted:
			f.TurnsStarted++
			lastWasError = false
		case protocol.TurnCompleted:
			f.TurnsCompleted++
			reason := e.StopReason
			if reason == "" {
				reason = "unknown"
			}
			f.StopReasons[reason]++
			f.FinalStatus = reason
			lastWasError = false
			for _, fc := range e.Files {
				if fc.Path != "" {
					f.Files[fc.Path] = fc.Kind
				}
			}
		case protocol.ToolCallBegin:
			if e.Name != "" {
				f.ToolCalls[e.Name]++
			}
		case protocol.ToolCallEnd:
			if e.IsError {
				f.ToolErrors++
			}
		case protocol.PermissionAsked:
			if e.RequestID != "" && !asked[e.RequestID] {
				asked[e.RequestID] = true
				f.ApprovalsAsked++
			}
		case protocol.PermissionResolved:
			f.resolveApproval(e.RequestID, e.Decision, asked, resolved)
		case protocol.PermissionDecided:
			if e.RequestID != "" {
				if e.Decision != "" {
					f.resolveApproval(e.RequestID, e.Decision, asked, resolved)
				} else if e.Action == "deny" {
					f.resolveApproval(e.RequestID, protocol.DecisionReject, asked, resolved)
				}
				continue
			}
			switch e.Action {
			case "allow":
				f.PolicyAllowed++
			case "deny":
				f.PolicyDenied++
			}
		case protocol.UsageReported:
			f.UsageReports++
			addTokens(f.Usage, "input", e.Input, &f.UsageUnknown)
			addTokens(f.Usage, "output", e.Output, nil)
			addTokens(f.Usage, "cacheRead", e.CacheRead, nil)
			addTokens(f.Usage, "cacheCreation", e.CacheCreation, nil)
		case protocol.ArtifactUpdated:
			if e.ID == "" {
				continue
			}
			ref := fmt.Sprintf("%s %s v%d", e.Type, e.ID, e.Version)
			if i, ok := seenArtifact[e.ID]; ok {
				f.Artifacts[i] = ref
			} else {
				seenArtifact[e.ID] = len(f.Artifacts)
				f.Artifacts = append(f.Artifacts, ref)
			}
		case protocol.EngineError:
			f.Errors++
			f.LastError = briefLine(e.Message, reportMaxErrRunes)
			lastWasError = true
		}
	}
	if err := sc.Err(); err != nil {
		return f, err
	}
	for id := range asked {
		if !resolved[id] {
			f.ApprovalsUnresolved++
		}
	}
	switch {
	case lastWasError:
		f.FinalStatus = "error"
	case f.TurnsStarted > f.TurnsCompleted:
		f.FinalStatus = "interrupted"
	case f.FinalStatus == "" && f.TurnsStarted == 0:
		f.FinalStatus = "no turns"
	}
	return f, nil
}

func (f *sessionReportFacts) resolveApproval(id string, d protocol.Decision, asked, resolved map[string]bool) {
	if id == "" || resolved[id] || !asked[id] {
		return
	}
	resolved[id] = true
	if d == protocol.DecisionReject {
		f.ApprovalsDenied++
	} else {
		f.ApprovalsAllowed++
	}
}

// addTokens sums known counts; unknown counts the request once via unknown.
func addTokens(m map[string]int, key string, c protocol.TokenCount, unknown *int) {
	if !c.Known {
		if unknown != nil {
			*unknown++
		}
		return
	}
	m[key] += c.N
}

// envelopeSessionID reads the flattened Correlation.sessionId from an
// envelope payload; empty when absent or legacy.
func envelopeSessionID(data json.RawMessage) string {
	var c struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(data, &c)
	return c.SessionID
}

// briefLine returns the first non-empty line of s, trimmed to max runes.
func briefLine(s string, max int) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if utf8.RuneCountInString(ln) > max {
			r := []rune(ln)
			ln = string(r[:max]) + "…"
		}
		return ln
	}
	return ""
}

// renderSessionReport renders facts as markdown. Sections with no recorded
// data are omitted. The caller redacts the result.
func renderSessionReport(f sessionReportFacts, generatedAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session report\n\n")
	fmt.Fprintf(&b, "_Generated by the Strike daemon from the session record at %s. Facts are not model-written._\n\n", generatedAt.UTC().Format(time.RFC3339))

	b.WriteString("## Session\n\n")
	fmt.Fprintf(&b, "- Session: `%s`\n", f.SessionID)
	if f.Title != "" {
		fmt.Fprintf(&b, "- Title: %s\n", f.Title)
	}
	if f.Project != "" {
		fmt.Fprintf(&b, "- Workspace: %s\n", f.Project)
	}
	if len(f.Models) > 0 {
		fmt.Fprintf(&b, "- Models: %s\n", strings.Join(f.Models, ", "))
	}
	if !f.Start.IsZero() {
		fmt.Fprintf(&b, "- Started: %s\n", f.Start.UTC().Format(time.RFC3339))
		fmt.Fprintf(&b, "- Last event: %s\n", f.End.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "- Turns: %d started, %d completed\n", f.TurnsStarted, f.TurnsCompleted)
	fmt.Fprintf(&b, "- Final status: %s\n", f.FinalStatus)
	if len(f.StopReasons) > 0 {
		fmt.Fprintf(&b, "- Stop reasons: %s\n", joinCounts(f.StopReasons))
	}
	b.WriteString("\n")

	if len(f.Asks) > 0 {
		b.WriteString("## User asks\n\n")
		for i, a := range f.Asks {
			if i == reportMaxAsks {
				fmt.Fprintf(&b, "- … and %d more\n", len(f.Asks)-reportMaxAsks)
				break
			}
			fmt.Fprintf(&b, "%d. %s\n", i+1, a)
		}
		b.WriteString("\n")
	}

	if len(f.ToolCalls) > 0 {
		total := 0
		for _, n := range f.ToolCalls {
			total += n
		}
		b.WriteString("## Tools\n\n")
		fmt.Fprintf(&b, "- Calls: %d (%d errored)\n", total, f.ToolErrors)
		fmt.Fprintf(&b, "- By tool: %s\n\n", joinCounts(f.ToolCalls))
	}

	if len(f.Files) > 0 {
		paths := make([]string, 0, len(f.Files))
		for p := range f.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		fmt.Fprintf(&b, "## Files changed (%d)\n\n", len(paths))
		for i, p := range paths {
			if i == reportMaxFiles {
				fmt.Fprintf(&b, "- … and %d more\n", len(paths)-reportMaxFiles)
				break
			}
			fmt.Fprintf(&b, "- %s `%s`\n", f.Files[p], p)
		}
		b.WriteString("\n")
	}

	if f.ApprovalsAsked > 0 || f.PolicyAllowed > 0 || f.PolicyDenied > 0 {
		b.WriteString("## Approvals\n\n")
		if f.ApprovalsAsked > 0 {
			fmt.Fprintf(&b, "- Asked: %d — allowed %d, denied %d, unresolved %d\n",
				f.ApprovalsAsked, f.ApprovalsAllowed, f.ApprovalsDenied, f.ApprovalsUnresolved)
		}
		if f.PolicyAllowed > 0 || f.PolicyDenied > 0 {
			fmt.Fprintf(&b, "- Decided by policy without asking: allowed %d, denied %d\n", f.PolicyAllowed, f.PolicyDenied)
		}
		b.WriteString("\n")
	}

	if len(f.Children) > 0 {
		fmt.Fprintf(&b, "## Child sessions (%d)\n\n", len(f.Children))
		for i, c := range f.Children {
			if i == reportMaxChildren {
				fmt.Fprintf(&b, "- … and %d more\n", len(f.Children)-reportMaxChildren)
				break
			}
			label := c.Name
			if label == "" {
				label = c.Agent
			}
			if label != "" {
				fmt.Fprintf(&b, "- `%s` %s — %s\n", c.SessionID, label, c.Status)
			} else {
				fmt.Fprintf(&b, "- `%s` — %s\n", c.SessionID, c.Status)
			}
		}
		b.WriteString("\n")
	}

	if f.UsageReports > 0 && f.UsageReports > f.UsageUnknown {
		b.WriteString("## Tokens\n\n")
		fmt.Fprintf(&b, "- Input: %d\n- Output: %d\n", f.Usage["input"], f.Usage["output"])
		if f.Usage["cacheRead"] > 0 || f.Usage["cacheCreation"] > 0 {
			fmt.Fprintf(&b, "- Cache read: %d\n- Cache write: %d\n", f.Usage["cacheRead"], f.Usage["cacheCreation"])
		}
		if f.UsageUnknown > 0 {
			fmt.Fprintf(&b, "- Partial: %d of %d requests reported no input count\n", f.UsageUnknown, f.UsageReports)
		}
		b.WriteString("\n")
	}

	if len(f.Artifacts) > 0 {
		b.WriteString("## Artifacts\n\n")
		for _, a := range f.Artifacts {
			fmt.Fprintf(&b, "- %s\n", a)
		}
		b.WriteString("\n")
	}

	if f.Errors > 0 {
		b.WriteString("## Errors\n\n")
		fmt.Fprintf(&b, "- Engine errors: %d\n", f.Errors)
		if f.LastError != "" {
			fmt.Fprintf(&b, "- Last: %s\n", f.LastError)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func joinCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

// handleSessionReport generates and stores a session_report artifact.
func (s *Server) handleSessionReport(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	writer, ok := s.artifactsService().(host.SessionReportWriter)
	if !ok {
		capabilityUnavailable(w, "artifacts")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "session id is required"})
		return
	}
	sess, found, err := s.opts.Services.Sessions.Get(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not found"})
		return
	}
	raw, err := s.opts.Services.Sessions.ReplayJSONL(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	facts, err := buildSessionReportFacts(id, raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleErrorBody{
			Error: err.Error(),
			Code:  protocol.ErrorCodeSessionCorrupt,
		})
		return
	}
	if facts.Title == "" {
		facts.Title = sess.Title
	}
	facts.Project = sess.ProjectKey
	content := redact.String(renderSessionReport(facts, time.Now()))
	title := "Session report"
	if facts.Title != "" {
		title = briefLine("Session report: "+facts.Title, 200)
	}
	_, actorRoot := s.actorFromRequest(r)
	if actorRoot == "" {
		actorRoot = id
	}
	art, err := writer.CreateSessionReport(id, actorRoot, redact.String(title), content)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toArtifactDTO(art))
}
