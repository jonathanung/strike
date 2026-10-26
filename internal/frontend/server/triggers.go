package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/daemon/trigger"
)

func (s *Server) triggers() *trigger.Service {
	if s == nil {
		return nil
	}
	return s.opts.Triggers
}

func (s *Server) handleTriggersList(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Triggers []trigger.View `json:"triggers"`
		Adapters []string       `json:"adapters"`
	}{Triggers: svc.List(), Adapters: svc.Adapters()})
}

func (s *Server) handleTriggerCreate(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	var in trigger.Input
	if err := decodeBody(w, r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	view, secret, err := svc.Create(in)
	if err != nil {
		writeJSON(w, triggerWriteStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, triggerSaved{View: view, Secret: secret})
}

func (s *Server) handleTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	var in trigger.Input
	if err := decodeBody(w, r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	view, secret, err := svc.Update(r.PathValue("id"), in)
	if err != nil {
		writeJSON(w, triggerWriteStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, triggerSaved{View: view, Secret: secret})
}

func (s *Server) handleTriggerDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	if err := svc.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, triggerWriteStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleTriggerDeliveries(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	rows, err := svc.Deliveries(r.PathValue("id"))
	if err != nil {
		writeJSON(w, triggerWriteStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	if rows == nil {
		rows = []trigger.DeliveryView{}
	}
	writeJSON(w, http.StatusOK, struct {
		Deliveries []trigger.DeliveryView `json:"deliveries"`
	}{Deliveries: rows})
}

func (s *Server) handleTriggerTest(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	var body struct {
		Event      string          `json:"event"`
		DeliveryID string          `json:"deliveryId"`
		Body       json.RawMessage `json:"body"`
		Text       string          `json:"text"`
		Sender     string          `json:"sender"`
		Channel    string          `json:"channel"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	raw := []byte(body.Body)
	if len(raw) > 0 && raw[0] == '"' {
		var asString string
		if err := json.Unmarshal(raw, &asString); err == nil {
			raw = []byte(asString)
		}
	}
	res := svc.Test(r.Context(), r.PathValue("id"), trigger.Hook{
		Event:      body.Event,
		DeliveryID: body.DeliveryID,
		Body:       raw,
		Text:       body.Text,
		Sender:     body.Sender,
		Channel:    body.Channel,
	})
	writeJSON(w, res.Status, res)
}

func (s *Server) handleTriggerReplay(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	var body struct {
		DeliveryID string `json:"deliveryId"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	res := svc.Replay(r.Context(), r.PathValue("id"), strings.TrimSpace(body.DeliveryID))
	writeJSON(w, res.Status, res)
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	svc := s.triggers()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "triggers unavailable"})
		return
	}
	if !s.allowTriggerWrite(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, trigger.MaxBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, opErrorResponse{Error: "payload too large"})
		return
	}
	res := svc.Receive(r.Context(), trigger.Hook{
		ID:         r.PathValue("id"),
		Body:       body,
		Timestamp:  r.Header.Get("X-Strike-Timestamp"),
		DeliveryID: firstHeader(r, "X-Strike-Delivery", "X-GitHub-Delivery"),
		Signature:  firstHeader(r, "X-Strike-Signature", "X-Hub-Signature-256"),
		Event:      firstHeader(r, "X-Strike-Event", "X-GitHub-Event"),
	})
	writeJSON(w, res.Status, res)
}

func (s *Server) allowTriggerWrite(w http.ResponseWriter) bool {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "server is read-only"})
		return false
	}
	return true
}

func triggerWriteStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(err.Error(), "not found") {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

type triggerSaved struct {
	trigger.View
	Secret string `json:"secret,omitempty"`
}

func firstHeader(r *http.Request, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}
