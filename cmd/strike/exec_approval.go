package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// Approval control channel wire types (NDJSON, one object per line).
// Controllers receive *.request lines and must reply with matching *.reply.

const (
	approvalTypePermReq  = "permission.request"
	approvalTypePermRep  = "permission.reply"
	approvalTypeQuestReq = "question.request"
	approvalTypeQuestRep = "question.reply"

	defaultApprovalTimeout = 60 * time.Second
)

// approvalRequest is written to the controller (secrets already redacted).
type approvalRequest struct {
	Type       string                    `json:"type"`
	RequestID  string                    `json:"requestId"`
	SessionID  string                    `json:"sessionId,omitempty"`
	Permission string                    `json:"permission,omitempty"`
	Patterns   []string                  `json:"patterns,omitempty"`
	Questions  []protocol.QuestionPrompt `json:"questions,omitempty"`
}

// approvalReply is read from the controller.
type approvalReply struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	// Decision is once|reject|always|project for permission replies.
	Decision string `json:"decision,omitempty"`
	// Message is optional reject feedback (permission) or free text.
	Message string `json:"message,omitempty"`
	// Answers is required for question.reply (one per prompt).
	Answers []string `json:"answers,omitempty"`
	// Durable must be true to allow always/project grants. Fail closed otherwise.
	Durable bool `json:"durable,omitempty"`
}

// approvalController is a bidirectional NDJSON policy channel for one-shot exec.
type approvalController struct {
	r       *bufio.Scanner
	w       io.Writer
	writeMu sync.Mutex
	timeout time.Duration
	// closer releases the underlying connection/files.
	closer func() error

	mu     sync.Mutex
	closed bool
}

// openApprovalController dials a unix socket at path, or opens path as a
// bidirectional file (FIFO). Empty path returns nil (legacy auto-reject).
func openApprovalController(path string, timeout time.Duration) (*approvalController, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	// Prefer unix socket (controller listens).
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err == nil {
		return newApprovalController(conn, conn, func() error { return conn.Close() }, timeout), nil
	}
	// Fall back to open file/FIFO (O_RDWR).
	f, ferr := os.OpenFile(path, os.O_RDWR, 0)
	if ferr != nil {
		return nil, fmt.Errorf("approval control %q: dial unix: %v; open file: %w", path, err, ferr)
	}
	return newApprovalController(f, f, f.Close, timeout), nil
}

func newApprovalController(r io.Reader, w io.Writer, closer func() error, timeout time.Duration) *approvalController {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	return &approvalController{r: sc, w: w, closer: closer, timeout: timeout}
}

func (c *approvalController) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.closer != nil {
		return c.closer()
	}
	return nil
}

func (c *approvalController) dead() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// resolvePermission asks the controller; fail-closed on timeout/disconnect/malformed.
func (c *approvalController) resolvePermission(ctx context.Context, asked protocol.PermissionAsked, sessionID string) protocol.PermissionReply {
	reject := protocol.PermissionReply{
		RequestID: asked.RequestID,
		Decision:  protocol.DecisionReject,
		Message:   headlessPermissionReject,
	}
	if c == nil || c.dead() {
		reject.Message = "approval control unavailable; permission denied (fail closed)"
		return reject
	}
	req := approvalRequest{
		Type:       approvalTypePermReq,
		RequestID:  asked.RequestID,
		SessionID:  sessionID,
		Permission: asked.Permission,
		Patterns:   redactStrings(asked.Patterns),
	}
	if err := c.writeRequest(ctx, req); err != nil {
		reject.Message = "approval control write failed; permission denied (fail closed)"
		return reject
	}
	rep, err := c.readReply(ctx, asked.RequestID, approvalTypePermRep)
	if err != nil {
		reject.Message = "approval control: " + err.Error() + " (fail closed)"
		return reject
	}
	dec := protocol.Decision(strings.ToLower(strings.TrimSpace(rep.Decision)))
	switch dec {
	case protocol.DecisionOnce, protocol.DecisionReject:
		// ok
	case protocol.DecisionAlways, protocol.DecisionProject:
		if !rep.Durable {
			reject.Message = "durable grant requires explicit durable=true; permission denied (fail closed)"
			return reject
		}
	default:
		reject.Message = fmt.Sprintf("malformed approval reply decision %q (fail closed)", rep.Decision)
		return reject
	}
	msg := redact.String(rep.Message)
	return protocol.PermissionReply{
		RequestID: asked.RequestID,
		Decision:  dec,
		Message:   msg,
	}
}

// resolveQuestion asks the controller; empty answers on fail-closed.
func (c *approvalController) resolveQuestion(ctx context.Context, asked protocol.QuestionAsked, sessionID string) protocol.QuestionReply {
	empty := protocol.QuestionReply{RequestID: asked.RequestID}
	if c == nil || c.dead() {
		return empty
	}
	qs := make([]protocol.QuestionPrompt, len(asked.Questions))
	for i, q := range asked.Questions {
		qs[i] = protocol.QuestionPrompt{
			ID:       q.ID,
			Header:   redact.String(q.Header),
			Question: redact.String(q.Question),
			Options:  redactQuestionOptions(q.Options),
		}
	}
	req := approvalRequest{
		Type:      approvalTypeQuestReq,
		RequestID: asked.RequestID,
		SessionID: sessionID,
		Questions: qs,
	}
	if err := c.writeRequest(ctx, req); err != nil {
		return empty
	}
	rep, err := c.readReply(ctx, asked.RequestID, approvalTypeQuestRep)
	if err != nil {
		return empty
	}
	answers := make([]string, len(rep.Answers))
	for i, a := range rep.Answers {
		answers[i] = redact.String(a)
	}
	return protocol.QuestionReply{RequestID: asked.RequestID, Answers: answers}
}

func (c *approvalController) writeRequest(ctx context.Context, req approvalRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	type writeRes struct{ err error }
	ch := make(chan writeRes, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		_, err := c.w.Write(data)
		ch <- writeRes{err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("timeout writing approval request")
	case res := <-ch:
		return res.err
	}
}

func (c *approvalController) readReply(ctx context.Context, wantID, wantType string) (approvalReply, error) {
	type result struct {
		rep approvalReply
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for c.r.Scan() {
			line := strings.TrimSpace(c.r.Text())
			if line == "" {
				continue
			}
			var rep approvalReply
			if err := json.Unmarshal([]byte(line), &rep); err != nil {
				ch <- result{err: fmt.Errorf("malformed reply: %w", err)}
				return
			}
			// Ignore unrelated lines (keep scanning until match or error type).
			if rep.RequestID != "" && rep.RequestID != wantID {
				continue
			}
			if rep.Type != "" && rep.Type != wantType {
				ch <- result{err: fmt.Errorf("unexpected reply type %q", rep.Type)}
				return
			}
			if strings.TrimSpace(rep.RequestID) == "" {
				ch <- result{err: errors.New("malformed reply: missing requestId")}
				return
			}
			if rep.RequestID != wantID {
				ch <- result{err: fmt.Errorf("reply requestId %q != %q", rep.RequestID, wantID)}
				return
			}
			ch <- result{rep: rep}
			return
		}
		if err := c.r.Err(); err != nil {
			ch <- result{err: fmt.Errorf("disconnected: %w", err)}
			return
		}
		ch <- result{err: errors.New("disconnected")}
	}()

	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return approvalReply{}, ctx.Err()
	case <-timer.C:
		return approvalReply{}, errors.New("timeout waiting for approval reply")
	case res := <-ch:
		return res.rep, res.err
	}
}

func redactStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact.String(s)
	}
	return out
}

func redactQuestionOptions(in []protocol.QuestionOption) []protocol.QuestionOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.QuestionOption, len(in))
	for i, o := range in {
		out[i] = protocol.QuestionOption{
			Label:       redact.String(o.Label),
			Description: redact.String(o.Description),
		}
	}
	return out
}
