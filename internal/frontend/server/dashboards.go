package server

// Agent-authored dashboards (#1628). A dashboard is a "dashboard" artifact
// whose content is a block spec (internal/persist/artifact/dashboard.go).
// These routes are the cockpit's read surface plus a template starter:
//
//   GET  /v1/dashboards        dashboards the actor may read (artifact access)
//   GET  /v1/dashboards/{id}   the page with measured blocks filled now
//   POST /v1/dashboards        {template,title}: start a page from a template
//
// Measured blocks are filled here, from daemon records, at every read:
// the spec only names the metric. A metric the daemon cannot measure is
// returned with measured=false and a null value, never 0 and never a
// number from the spec. Agent-written blocks pass through with
// provenance "agent" so the cockpit marks them.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/artifact"
)

const (
	dashboardKanbanCards = 20
	dashboardBodyLimit   = 4 << 10
)

type dashboardColumnDTO struct {
	Title string   `json:"title"`
	Cards []string `json:"cards"`
}

type dashboardBlockDTO struct {
	Kind string `json:"kind"`
	// Provenance is "measured" (daemon-filled) or "agent" (agent wrote it).
	Provenance string               `json:"provenance"`
	Title      string               `json:"title,omitempty"`
	Metric     string               `json:"metric,omitempty"`
	Value      *float64             `json:"value,omitempty"`
	Measured   bool                 `json:"measured"`
	Unit       string               `json:"unit,omitempty"`
	Note       string               `json:"note,omitempty"`
	Body       string               `json:"body,omitempty"`
	Items      []string             `json:"items,omitempty"`
	Columns    []dashboardColumnDTO `json:"columns,omitempty"`
}

type dashboardPageDTO struct {
	artifactMetaDTO
	Template   string              `json:"template,omitempty"`
	Blocks     []dashboardBlockDTO `json:"blocks"`
	RenderedAt time.Time           `json:"renderedAt"`
}

type dashboardCreateRequest struct {
	Template string `json:"template"`
	Title    string `json:"title"`
}

// dashboardRecords is the daemon data measured blocks read, gathered once
// per request. A nil slice means the source is unavailable (unmeasured),
// distinct from an empty one (measured zero).
type dashboardRecords struct {
	now      time.Time
	sessions []sessionListItem
	roots    []RootSummary
}

func (s *Server) dashboardRecords() dashboardRecords {
	rec := dashboardRecords{now: time.Now()}
	if s.opts.SessionDir != "" {
		if items, err := listSessionFiles(s.opts.SessionDir); err == nil {
			items = s.decorateSessions(items)
			if items == nil {
				items = []sessionListItem{}
			}
			rec.sessions = items
		}
	}
	if s.opts.LiveHub != nil {
		roots := s.opts.LiveHub.List()
		s.decorateRoots(roots)
		if roots == nil {
			roots = []RootSummary{}
		}
		rec.roots = roots
	}
	return rec
}

// measureMetric resolves one metric name. ok=false means not measured.
func (rec dashboardRecords) measureMetric(name string) (value float64, unit, note string, ok bool) {
	countSessions := func(match func(sessionListItem) bool) (float64, string, string, bool) {
		if rec.sessions == nil {
			return 0, "", "session records unavailable", false
		}
		n := 0
		for _, it := range rec.sessions {
			if match(it) {
				n++
			}
		}
		return float64(n), "", "", true
	}
	countRoots := func(match func(RootSummary) bool) (float64, string, string, bool) {
		if rec.roots == nil {
			return 0, "", "live sessions unavailable", false
		}
		n := 0
		for _, r := range rec.roots {
			if match(r) {
				n++
			}
		}
		return float64(n), "", "", true
	}
	switch name {
	case "sessions.total":
		return countSessions(func(sessionListItem) bool { return true })
	case "sessions.running":
		return countSessions(func(it sessionListItem) bool { return it.Status == "running" })
	case "sessions.waiting":
		return countSessions(func(it sessionListItem) bool { return it.Status == "waiting" })
	case "sessions.idle":
		return countSessions(func(it sessionListItem) bool { return it.Status == "idle" })
	case "sessions.active_24h":
		since := rec.now.Add(-24 * time.Hour).UnixMilli()
		return countSessions(func(it sessionListItem) bool { return it.LastActivityAt >= since })
	case "approvals.pending":
		return countRoots(func(r RootSummary) bool { return r.PermissionPending })
	case "questions.pending":
		return countRoots(func(r RootSummary) bool { return r.QuestionPending })
	case "cost.usd":
		// No priced usage record is aggregated across sessions yet: a dash,
		// never $0.
		return 0, "usd", "cost is not measured across sessions", false
	default:
		return 0, "", "unknown metric", false
	}
}

func (rec dashboardRecords) sessionsKanban() ([]dashboardColumnDTO, bool) {
	if rec.sessions == nil {
		return nil, false
	}
	order := []struct{ title, status string }{
		{"Running", "running"},
		{"Waiting", "waiting"},
		{"Idle", "idle"},
	}
	cols := make([]dashboardColumnDTO, len(order))
	for i, o := range order {
		cols[i] = dashboardColumnDTO{Title: o.title, Cards: []string{}}
	}
	for _, it := range rec.sessions {
		idx := 2
		switch it.Status {
		case "running":
			idx = 0
		case "waiting":
			idx = 1
		}
		if len(cols[idx].Cards) >= dashboardKanbanCards {
			continue
		}
		label := strings.TrimSpace(it.Title)
		if label == "" {
			label = it.ID
		}
		cols[idx].Cards = append(cols[idx].Cards, label)
	}
	return cols, true
}

// renderDashboard fills measured blocks from rec. Agent blocks pass through.
func renderDashboard(spec artifact.DashboardSpec, rec dashboardRecords) []dashboardBlockDTO {
	out := make([]dashboardBlockDTO, 0, len(spec.Blocks))
	for _, b := range spec.Blocks {
		d := dashboardBlockDTO{Kind: b.Kind, Title: b.Title}
		switch b.Kind {
		case artifact.BlockMetric:
			d.Provenance = "measured"
			d.Metric = b.Metric
			v, unit, note, ok := rec.measureMetric(b.Metric)
			d.Unit = unit
			d.Note = note
			if ok {
				d.Value = &v
				d.Measured = true
			}
		case artifact.BlockSessionsKanban:
			d.Provenance = "measured"
			if cols, ok := rec.sessionsKanban(); ok {
				d.Columns = cols
				d.Measured = true
			} else {
				d.Note = "session records unavailable"
			}
		default:
			d.Provenance = "agent"
			d.Body = b.Body
			d.Items = b.Items
			for _, c := range b.Columns {
				cards := c.Cards
				if cards == nil {
					cards = []string{}
				}
				d.Columns = append(d.Columns, dashboardColumnDTO{Title: c.Title, Cards: cards})
			}
		}
		out = append(out, d)
	}
	return out
}

// dashboardTemplate returns the starter spec for name, or false.
func dashboardTemplate(name string) (string, artifact.DashboardSpec, bool) {
	metric := func(title, m string) artifact.DashboardBlock {
		return artifact.DashboardBlock{Kind: artifact.BlockMetric, Title: title, Metric: m}
	}
	text := func(title string) artifact.DashboardBlock {
		return artifact.DashboardBlock{Kind: artifact.BlockText, Title: title}
	}
	switch name {
	case "project_report":
		return "Project report", artifact.DashboardSpec{Version: 1, Template: name, Blocks: []artifact.DashboardBlock{
			metric("Sessions", "sessions.total"),
			metric("Running now", "sessions.running"),
			metric("Waiting on approval", "approvals.pending"),
			metric("Cost (USD)", "cost.usd"),
			{Kind: artifact.BlockSessionsKanban, Title: "Sessions by status"},
			text("Summary"),
		}}, true
	case "standup":
		return "Standup", artifact.DashboardSpec{Version: 1, Template: name, Blocks: []artifact.DashboardBlock{
			metric("Active in the last 24 h", "sessions.active_24h"),
			metric("Running now", "sessions.running"),
			metric("Waiting on you", "approvals.pending"),
			text("Yesterday"),
			text("Today"),
			text("Blockers"),
		}}, true
	case "kanban":
		return "Work board", artifact.DashboardSpec{Version: 1, Template: name, Blocks: []artifact.DashboardBlock{
			{Kind: artifact.BlockSessionsKanban, Title: "Sessions"},
			{Kind: artifact.BlockKanban, Title: "Work", Columns: []artifact.DashboardColumn{
				{Title: "To do", Cards: []string{}},
				{Title: "Doing", Cards: []string{}},
				{Title: "Done", Cards: []string{}},
			}},
		}}, true
	default:
		return "", artifact.DashboardSpec{}, false
	}
}

func (s *Server) dashboardPage(a host.Artifact) (dashboardPageDTO, error) {
	spec, err := artifact.ParseDashboard(a.Content)
	if err != nil {
		return dashboardPageDTO{}, err
	}
	meta := toArtifactDTO(a).artifactMetaDTO
	return dashboardPageDTO{
		artifactMetaDTO: meta,
		Template:        spec.Template,
		Blocks:          renderDashboard(spec, s.dashboardRecords()),
		RenderedAt:      time.Now().UTC(),
	}, nil
}

func (s *Server) handleDashboardsList(w http.ResponseWriter, r *http.Request) {
	svc := s.artifactsService()
	if svc == nil {
		capabilityUnavailable(w, "artifacts")
		return
	}
	actorSession, actorRoot := s.actorFromRequest(r)
	if actorSession == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "actorSession or root is required"})
		return
	}
	items, err := svc.List(actorSession, actorRoot, host.ArtifactListFilter{Type: artifact.TypeDashboard})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	out := make([]artifactMetaDTO, 0, len(items))
	for _, m := range items {
		out = append(out, toArtifactMetaDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": out})
}

func (s *Server) handleDashboardGet(w http.ResponseWriter, r *http.Request) {
	svc := s.artifactsService()
	if svc == nil {
		capabilityUnavailable(w, "artifacts")
		return
	}
	actorSession, actorRoot := s.actorFromRequest(r)
	if actorSession == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "actorSession or root is required"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	a, ok, err := svc.Get(id, actorSession, actorRoot)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok || a.Type != artifact.TypeDashboard {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "dashboard not found"})
		return
	}
	page, err := s.dashboardPage(a)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleDashboardCreate(w http.ResponseWriter, r *http.Request) {
	writer, ok := s.artifactsService().(host.DashboardWriter)
	if !ok {
		capabilityUnavailable(w, "artifacts")
		return
	}
	actorSession, actorRoot := s.actorFromRequest(r)
	if actorSession == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "actorSession or root is required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, dashboardBodyLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in dashboardCreateRequest
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid dashboard request"})
		return
	}
	if err := ensureEOF(dec); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid dashboard request"})
		return
	}
	defTitle, spec, found := dashboardTemplate(strings.TrimSpace(in.Template))
	if !found {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "template must be project_report, standup or kanban"})
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = defTitle
	}
	if len([]rune(title)) > 120 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title is longer than 120 characters"})
		return
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "template unavailable"})
		return
	}
	a, err := writer.CreateDashboard(actorSession, actorRoot, title, string(raw))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	page, err := s.dashboardPage(a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, page)
}
