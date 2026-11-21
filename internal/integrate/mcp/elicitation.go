package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// MCP elicitation (#1353): mid tools/call the server may send an
// elicitation/create request. Strike routes it to the tool call that is in
// flight on this connection, the engine asks the user, and the answer goes
// back as the request's result: {action: accept|decline|cancel, content}.
// An answer is data for a call that is already permitted; it never grants a
// permission.

const methodElicitationCreate = "elicitation/create"

// Elicitation request limits for untrusted server input.
const (
	maxElicitMessageRunes = 2000
	maxElicitSchemaBytes  = 64 << 10
	maxElicitURLRunes     = 2048
)

// ElicitParams is a decoded elicitation/create request. Mode is "form"
// (RequestedSchema) or "url" (URL); absent mode means form.
type ElicitParams struct {
	Mode            string          `json:"mode,omitempty"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	URL             string          `json:"url,omitempty"`
}

// ElicitResult is the elicitation/create response.
type ElicitResult struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content,omitempty"`
}

// ElicitHandler answers one elicitation for the call that registered it.
type ElicitHandler func(ctx context.Context, p ElicitParams) ElicitResult

// elicitors routes server elicitations to the most recent in-flight call
// that registered a handler. MCP stdio carries no call id on the request,
// so the newest waiting call is the best owner; with no call in flight the
// request is cancelled rather than shown without context.
type elicitors struct {
	mu   sync.Mutex
	next int
	list []elicitorEntry
}

type elicitorEntry struct {
	id int
	fn ElicitHandler
}

func (e *elicitors) push(fn ElicitHandler) (remove func()) {
	e.mu.Lock()
	e.next++
	id := e.next
	e.list = append(e.list, elicitorEntry{id: id, fn: fn})
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for i, ent := range e.list {
			if ent.id == id {
				e.list = append(e.list[:i], e.list[i+1:]...)
				return
			}
		}
	}
}

func (e *elicitors) current() ElicitHandler {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.list) == 0 {
		return nil
	}
	return e.list[len(e.list)-1].fn
}

// normalizeElicit clamps untrusted fields and fixes the mode. ok is false
// when the request cannot be shown faithfully (bad URL scheme, oversized or
// non-object schema); the caller answers cancel.
func normalizeElicit(p ElicitParams) (ElicitParams, bool) {
	p.Message = clampRunes(strings.TrimSpace(p.Message), maxElicitMessageRunes)
	switch strings.TrimSpace(p.Mode) {
	case "url":
		p.Mode = "url"
		p.RequestedSchema = nil
		u := strings.TrimSpace(p.URL)
		if len([]rune(u)) > maxElicitURLRunes {
			return p, false
		}
		lower := strings.ToLower(u)
		if !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "http://") {
			return p, false
		}
		p.URL = u
	default:
		p.Mode = "form"
		p.URL = ""
		if len(p.RequestedSchema) > maxElicitSchemaBytes {
			return p, false
		}
		if len(p.RequestedSchema) == 0 {
			p.RequestedSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(p.RequestedSchema, &obj); err != nil {
			return p, false
		}
	}
	return p, true
}

func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// answerElicit decodes, routes and answers one elicitation/create.
func answerElicit(ctx context.Context, reg *elicitors, raw json.RawMessage) ElicitResult {
	var p ElicitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return ElicitResult{Action: "cancel"}
	}
	p, ok := normalizeElicit(p)
	if !ok {
		return ElicitResult{Action: "cancel"}
	}
	fn := reg.current()
	if fn == nil {
		return ElicitResult{Action: "cancel"}
	}
	res := fn(ctx, p)
	switch res.Action {
	case "accept":
		if p.Mode != "form" {
			res.Content = nil
		} else if len(res.Content) == 0 {
			res.Content = json.RawMessage(`{}`)
		}
	case "decline":
		res.Content = nil
	default:
		res = ElicitResult{Action: "cancel"}
	}
	return res
}

// rpcProbe reads only what is needed to tell a server request (method + id)
// from a response or notification. IDs may be numbers or strings.
type rpcProbe struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (p rpcProbe) isRequest() bool {
	id := strings.TrimSpace(string(p.ID))
	return p.Method != "" && id != "" && id != "null"
}

type rpcServerResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// serverRequestResponse answers one server→client request. Only
// elicitation/create and ping are served; anything else is method-not-found
// so the server is never left waiting.
func serverRequestResponse(ctx context.Context, reg *elicitors, probe rpcProbe) rpcServerResponse {
	out := rpcServerResponse{JSONRPC: "2.0", ID: probe.ID}
	switch probe.Method {
	case methodElicitationCreate:
		out.Result = answerElicit(ctx, reg, probe.Params)
	case "ping":
		out.Result = map[string]any{}
	default:
		out.Error = &rpcError{Code: -32601, Message: "method not found"}
	}
	return out
}
