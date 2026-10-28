package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/pkg/redact"
)

func (s *Server) customizeDir(r *http.Request) string {
	if cwd := strings.TrimSpace(s.currentCWD(r)); cwd != "" {
		return cwd
	}
	return strings.TrimSpace(s.opts.WorkDir)
}

func customizeFail(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redact.String(err.Error())})
}

func (s *Server) handleCustomize(w http.ResponseWriter, r *http.Request) {
	cat, err := config.LoadCustomize(s.customizeDir(r))
	if err != nil {
		customizeFail(w, err)
		return
	}
	if s.opts.Services != nil && s.opts.Services.MCP != nil {
		var sts []config.MCPStatusView
		for _, st := range s.opts.Services.MCP.Statuses() {
			sts = append(sts, config.MCPStatusView{
				Name:      st.Name,
				State:     st.State,
				Error:     st.Error,
				ToolCount: st.ToolCount,
				Tools:     st.Tools,
			})
		}
		config.AttachMCPStatus(cat.MCP, sts)
	}
	writeJSON(w, http.StatusOK, cat)
}

func (s *Server) handleCustomizeDoc(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	index := 0
	hasIndex := false
	if raw := strings.TrimSpace(q.Get("index")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			customizeFail(w, err)
			return
		}
		index = n
		hasIndex = true
	}
	doc, err := config.OpenCustomizeDoc(
		s.customizeDir(r),
		q.Get("kind"),
		q.Get("name"),
		q.Get("scope"),
		index,
		hasIndex,
	)
	if err != nil {
		customizeFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleCustomizePreview(w http.ResponseWriter, r *http.Request) {
	var ch config.CustomizeChange
	if err := decodeBody(w, r, &ch); err != nil {
		customizeFail(w, err)
		return
	}
	ch.Confirm = false
	res, err := config.PreviewCustomize(s.customizeDir(r), ch)
	if err != nil {
		customizeFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCustomizeApply(w http.ResponseWriter, r *http.Request) {
	// Config files, not protocol ops. Same gate as plugin enable: bearer auth
	// when --auth is on. Tool permission rules are not consulted and not changed.
	var ch config.CustomizeChange
	if err := decodeBody(w, r, &ch); err != nil {
		customizeFail(w, err)
		return
	}
	res, err := config.ApplyCustomize(s.customizeDir(r), ch)
	if err != nil {
		customizeFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
