package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/persist/attachment"
	"github.com/jonathanung/strike/pkg/protocol"
)

const (
	maxAttachmentsPerOp    = 8
	maxAttachmentNameRunes = 180
)

// attachmentStatusError is a client-visible ingest failure with an HTTP status.
type attachmentStatusError struct {
	status int
	msg    string
}

func (e *attachmentStatusError) Error() string { return e.msg }

func attachmentFail(status int, msg string) error {
	return &attachmentStatusError{status: status, msg: msg}
}

func attachmentHTTPStatus(err error) int {
	var ae *attachmentStatusError
	if errors.As(err, &ae) && ae.status != 0 {
		return ae.status
	}
	return http.StatusBadRequest
}

type attachmentIngestRequest struct {
	Name      string              `json:"name"`
	MIME      string              `json:"mime"`
	Kind      string              `json:"kind,omitempty"`
	Data      string              `json:"data"`
	SessionID string              `json:"sessionId,omitempty"`
	Regions   []attachment.Region `json:"regions,omitempty"`
}

type attachmentIngestResponse struct {
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	MIME   string `json:"mime"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

func (s *Server) handleAttachmentIngest(w http.ResponseWriter, r *http.Request) {
	if err := s.admitOp(r, "http", "attachment.ingest", ""); err != nil {
		switch {
		case errors.Is(err, errOpsReadOnly):
			writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
		case errors.Is(err, errOpsRateLimited):
			writeJSON(w, http.StatusTooManyRequests, opErrorResponse{Error: err.Error()})
		default:
			writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
		}
		return
	}
	if s.opts.Attachments == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "attachment store is not configured"})
		return
	}
	var req attachmentIngestRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid attachment: " + err.Error()})
		return
	}
	if err := ensureEOF(dec); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	meta, err := s.storeIngest(req, "")
	if err != nil {
		s.recordServeOpError(r, "http", "attachment.ingest", "")
		writeJSON(w, attachmentHTTPStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	s.auditOpOK(r, "http", "attachment.ingest", meta.SessionID)
	writeJSON(w, http.StatusOK, attachmentIngestResponse{
		Ref:    attachment.RefFor(meta.SHA256),
		SHA256: meta.SHA256,
		Bytes:  meta.Bytes,
		MIME:   meta.MIME,
		Kind:   meta.Kind,
		Name:   attachment.RedactName(meta.Name),
	})
}

// prepareOp enforces attachment limits on user.input and steer, stores inline
// bytes, and submits refs only. Text-only ops are unchanged.
func (s *Server) prepareOp(op protocol.Op, sessionID string) (protocol.Op, error) {
	switch o := op.(type) {
	case protocol.UserInput:
		imgs, err := s.prepareImages(o.Images, sessionID)
		if err != nil {
			return nil, err
		}
		o.Images = imgs
		return o, nil
	case protocol.Steer:
		imgs, err := s.prepareImages(o.Images, sessionID)
		if err != nil {
			return nil, err
		}
		o.Images = imgs
		return o, nil
	default:
		return op, nil
	}
}

func (s *Server) prepareImages(images []protocol.ImageAttachment, sessionID string) ([]protocol.ImageAttachment, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > maxAttachmentsPerOp {
		return nil, attachmentFail(http.StatusBadRequest, fmt.Sprintf("too many attachments (%d > %d)", len(images), maxAttachmentsPerOp))
	}
	if s.opts.Attachments == nil {
		return nil, attachmentFail(http.StatusServiceUnavailable, "attachment store is not configured")
	}
	out := make([]protocol.ImageAttachment, 0, len(images))
	for _, img := range images {
		got, err := s.prepareOneImage(img, sessionID)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	return out, nil
}

func (s *Server) prepareOneImage(img protocol.ImageAttachment, sessionID string) (protocol.ImageAttachment, error) {
	if strings.TrimSpace(img.Data) != "" {
		meta, err := s.storeIngest(attachmentIngestRequest{
			Name:      img.Name,
			MIME:      img.MIME,
			Kind:      img.Kind,
			Data:      img.Data,
			SessionID: sessionID,
		}, sessionID)
		if err != nil {
			return protocol.ImageAttachment{}, err
		}
		return refAttachment(meta, ""), nil
	}
	if _, ok := attachment.ParseRef(img.Ref); !ok {
		return protocol.ImageAttachment{}, attachmentFail(http.StatusBadRequest, "attachment needs data or a valid ref")
	}
	_, meta, err := s.opts.Attachments.Get(img.Ref)
	if err != nil {
		if errors.Is(err, attachment.ErrNotFound) {
			return protocol.ImageAttachment{}, attachmentFail(http.StatusBadRequest, "attachment ref not found")
		}
		return protocol.ImageAttachment{}, attachmentFail(http.StatusBadRequest, err.Error())
	}
	return refAttachment(meta, img.Name), nil
}

func refAttachment(meta attachment.Meta, name string) protocol.ImageAttachment {
	name = attachment.RedactName(clipAttachmentName(name))
	if name == "" {
		name = attachment.RedactName(meta.Name)
	}
	return protocol.ImageAttachment{
		MIME:   meta.MIME,
		Ref:    attachment.RefFor(meta.SHA256),
		SHA256: meta.SHA256,
		Bytes:  meta.Bytes,
		Kind:   meta.Kind,
		Name:   name,
	}
}

func (s *Server) storeIngest(req attachmentIngestRequest, sessionFallback string) (attachment.Meta, error) {
	if s == nil || s.opts.Attachments == nil {
		return attachment.Meta{}, attachmentFail(http.StatusServiceUnavailable, "attachment store is not configured")
	}
	raw, err := decodeIngestData(req.Data)
	if err != nil {
		return attachment.Meta{}, err
	}
	if len(raw) > attachment.DefaultMaxBytes {
		return attachment.Meta{}, attachmentFail(http.StatusBadRequest, attachmentTooLarge(len(raw)))
	}
	mime := req.MIME
	kind := req.Kind
	if len(req.Regions) > 0 {
		painted, err := attachment.RedactRegions(raw, mime, req.Regions)
		if err != nil {
			return attachment.Meta{}, attachmentFail(http.StatusBadRequest, err.Error())
		}
		raw = painted
		if len(raw) > attachment.DefaultMaxBytes {
			return attachment.Meta{}, attachmentFail(http.StatusBadRequest, attachmentTooLarge(len(raw)))
		}
		// RedactRegions re-encodes as PNG and strips ancillary metadata.
		mime = "image/png"
		if kind == "" || strings.EqualFold(kind, attachment.KindImage) {
			kind = attachment.KindImage
		}
	}
	sessionID, _ := cleanSessionID(req.SessionID)
	if sessionID == "" {
		sessionID, _ = cleanSessionID(sessionFallback)
	}
	name := attachment.RedactName(clipAttachmentName(req.Name))
	meta, err := s.opts.Attachments.Put(raw, attachment.PutInput{
		MIME:      mime,
		Name:      name,
		Kind:      kind,
		SessionID: sessionID,
		MaxBytes:  attachment.DefaultMaxBytes,
	})
	if err != nil {
		return attachment.Meta{}, attachmentFail(http.StatusBadRequest, err.Error())
	}
	meta.Name = attachment.RedactName(meta.Name)
	return meta, nil
}

func decodeIngestData(data string) ([]byte, error) {
	data = strings.TrimSpace(data)
	if strings.HasPrefix(strings.ToLower(data), "data:") {
		if i := strings.IndexByte(data, ','); i >= 0 {
			data = data[i+1:]
		}
	}
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, attachmentFail(http.StatusBadRequest, "attachment is empty")
	}
	if len(data) > base64.StdEncoding.EncodedLen(attachment.DefaultMaxBytes)+16 {
		return nil, attachmentFail(http.StatusBadRequest, attachmentTooLarge(attachment.DefaultMaxBytes+1))
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(data)
		if err != nil {
			return nil, attachmentFail(http.StatusBadRequest, "invalid attachment base64")
		}
	}
	if len(raw) == 0 {
		return nil, attachmentFail(http.StatusBadRequest, "attachment is empty")
	}
	return raw, nil
}

func attachmentTooLarge(n int) string {
	return fmt.Sprintf("attachment exceeds %d bytes (%d)", attachment.DefaultMaxBytes, n)
}

func clipAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.ReplaceAll(name, "\r", " ")
	if utf8.RuneCountInString(name) <= maxAttachmentNameRunes {
		return name
	}
	return string([]rune(name)[:maxAttachmentNameRunes])
}

// cleanSessionID is the shared session-id validator for the server
// package (also used by the session-status handlers): only letters,
// digits, '-' and '_' are allowed, so path separators and ".." can
// never appear in a cleaned id.
func cleanSessionID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" || utf8.RuneCountInString(id) > 80 {
		return "", false
	}
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return "", false
	}
	return id, true
}
