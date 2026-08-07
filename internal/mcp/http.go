package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	headerSessionID = "Mcp-Session-Id"
	acceptMCP       = "application/json, text/event-stream"
)

// HTTPClient is a streamable-HTTP MCP session (POST JSON-RPC; JSON or SSE responses).
type HTTPClient struct {
	cfg    ServerConfig
	hc     *http.Client
	base   string
	nextID atomic.Int64

	mu        sync.Mutex
	sessionID string
	closed    atomic.Bool
	exitMu    sync.Mutex
	exitErr   error

	caps     ServerCaps
	oauth    *oauthSession
	notifyMu sync.Mutex
	onNotify func(method string, params json.RawMessage)
}

func startHTTP(ctx context.Context, cfg ServerConfig) (*HTTPClient, error) {
	if err := validateServerConfig(cfg); err != nil {
		return nil, err
	}
	base := strings.TrimSpace(cfg.URL)
	c := &HTTPClient{
		cfg:  cfg,
		hc:   &http.Client{},
		base: base,
	}
	if cfg.OAuth != nil {
		oa, err := newOAuthSession(ctx, cfg)
		if err != nil {
			return nil, err
		}
		c.oauth = oa
	}
	initCtx, cancel := context.WithTimeout(ctx, defaultInitTimeout)
	defer cancel()
	if err := c.initialize(initCtx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *HTTPClient) initialize(ctx context.Context) error {
	var result initializeResult
	if err := c.call(ctx, "initialize", initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    clientCapabilities(),
		ClientInfo:      implementationInfo{Name: "strike", Version: "1"},
	}, &result); err != nil {
		return fmt.Errorf("mcp %s: initialize: %w", c.cfg.Name, err)
	}
	c.caps = ParseServerCaps(result.Capabilities)
	if !c.caps.Tools && !c.caps.Prompts && !c.caps.Resources {
		c.caps.Tools = true
	}
	if err := c.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("mcp %s: initialized notify: %w", c.cfg.Name, err)
	}
	return nil
}

// Caps returns negotiated server capabilities.
func (c *HTTPClient) Caps() ServerCaps { return c.caps }

// OnNotification sets the notification handler (HTTP path is pull-based;
// notifications may arrive on SSE responses when supported).
func (c *HTTPClient) OnNotification(fn func(method string, params json.RawMessage)) {
	c.notifyMu.Lock()
	c.onNotify = fn
	c.notifyMu.Unlock()
}

func (c *HTTPClient) dispatchNotify(method string, params json.RawMessage) {
	c.notifyMu.Lock()
	fn := c.onNotify
	c.notifyMu.Unlock()
	if fn != nil {
		fn(method, params)
	}
}

// ListTools returns tools advertised by the server.
func (c *HTTPClient) ListTools(ctx context.Context) ([]toolInfo, error) {
	if c.Closed() {
		return nil, c.deadErr()
	}
	if !c.caps.Tools {
		return nil, nil
	}
	var result listToolsResult
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: tools/list: %w", c.cfg.Name, err)
	}
	return result.Tools, nil
}

// CallTool invokes a server tool by its MCP name (not the strike namespace).
func (c *HTTPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (callToolResult, error) {
	if c.Closed() {
		return callToolResult{}, c.deadErr()
	}
	if !c.caps.Tools {
		return callToolResult{}, fmt.Errorf("mcp %s: server has no tools capability", c.cfg.Name)
	}
	if args == nil {
		args = json.RawMessage(`{}`)
	}
	var result callToolResult
	if err := c.call(ctx, "tools/call", callToolParams{Name: name, Arguments: args}, &result); err != nil {
		return callToolResult{}, fmt.Errorf("mcp %s: tools/call %s: %w", c.cfg.Name, name, err)
	}
	return result, nil
}

// ListPrompts returns prompts when advertised.
func (c *HTTPClient) ListPrompts(ctx context.Context) ([]promptInfo, error) {
	if c.Closed() {
		return nil, c.deadErr()
	}
	if !c.caps.Prompts {
		return nil, nil
	}
	var result listPromptsResult
	if err := c.call(ctx, "prompts/list", map[string]any{}, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: prompts/list: %w", c.cfg.Name, err)
	}
	return result.Prompts, nil
}

// GetPrompt fetches one prompt.
func (c *HTTPClient) GetPrompt(ctx context.Context, name string, args map[string]string) (getPromptResult, error) {
	if c.Closed() {
		return getPromptResult{}, c.deadErr()
	}
	if !c.caps.Prompts {
		return getPromptResult{}, fmt.Errorf("mcp %s: server has no prompts capability", c.cfg.Name)
	}
	var result getPromptResult
	if err := c.call(ctx, "prompts/get", getPromptParams{Name: name, Arguments: args}, &result); err != nil {
		return getPromptResult{}, fmt.Errorf("mcp %s: prompts/get %s: %w", c.cfg.Name, name, err)
	}
	return result, nil
}

// ListResources returns resources when advertised.
func (c *HTTPClient) ListResources(ctx context.Context) ([]resourceInfo, error) {
	if c.Closed() {
		return nil, c.deadErr()
	}
	if !c.caps.Resources {
		return nil, nil
	}
	var result listResourcesResult
	if err := c.call(ctx, "resources/list", map[string]any{}, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: resources/list: %w", c.cfg.Name, err)
	}
	return result.Resources, nil
}

// ReadResource reads one resource by URI.
func (c *HTTPClient) ReadResource(ctx context.Context, uri string) (readResourceResult, error) {
	if c.Closed() {
		return readResourceResult{}, c.deadErr()
	}
	if !c.caps.Resources {
		return readResourceResult{}, fmt.Errorf("mcp %s: server has no resources capability", c.cfg.Name)
	}
	var result readResourceResult
	if err := c.call(ctx, "resources/read", readResourceParams{URI: uri}, &result); err != nil {
		return readResourceResult{}, fmt.Errorf("mcp %s: resources/read: %w", c.cfg.Name, err)
	}
	return result, nil
}

// Name returns the config server name.
func (c *HTTPClient) Name() string { return c.cfg.Name }

// Closed reports whether the client is shut down.
func (c *HTTPClient) Closed() bool { return c.closed.Load() }

func (c *HTTPClient) deadErr() error {
	c.exitMu.Lock()
	err := c.exitErr
	c.exitMu.Unlock()
	if err != nil {
		return fmt.Errorf("mcp server %q unavailable: %w", c.cfg.Name, err)
	}
	return fmt.Errorf("mcp server %q unavailable", c.cfg.Name)
}

// Close ends the HTTP session (best-effort DELETE) and marks the client dead.
func (c *HTTPClient) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.exitMu.Lock()
	if c.exitErr == nil {
		c.exitErr = fmt.Errorf("closed")
	}
	c.exitMu.Unlock()

	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base, nil)
	if err != nil {
		return nil
	}
	c.applyHeaders(req)
	req.Header.Set(headerSessionID, sid)
	resp, err := c.hc.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	return nil
}

func (c *HTTPClient) call(ctx context.Context, method string, params any, result any) error {
	if c.Closed() {
		return c.deadErr()
	}
	id := c.nextID.Add(1)
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	resp, err := c.doPOST(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.markDead(fmt.Errorf("session not found"))
		return c.deadErr()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatusErr(resp)
	}
	if sid := resp.Header.Get(headerSessionID); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	ct := resp.Header.Get("Content-Type")
	var rpcResp rpcResponse
	if strings.Contains(ct, "text/event-stream") {
		rpcResp, err = readSSEJSONRPC(resp.Body, id)
	} else {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxLineBytes))
		if readErr != nil {
			return readErr
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			return fmt.Errorf("empty response")
		}
		err = json.Unmarshal(raw, &rpcResp)
	}
	if err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if result == nil || len(rpcResp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(rpcResp.Result, result); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

func (c *HTTPClient) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Closed() {
		return c.deadErr()
	}
	body, err := json.Marshal(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	resp, err := c.doPOST(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 202 Accepted is preferred for notifications; 2xx with empty body is fine.
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return httpStatusErr(resp)
}

func (c *HTTPClient) doPOST(ctx context.Context, body []byte) (*http.Response, error) {
	resp, err := c.doPOSTOnce(ctx, body)
	if err != nil {
		return nil, err
	}
	// One refresh+retry on 401 when OAuth is configured.
	if resp.StatusCode == http.StatusUnauthorized && c.oauth != nil {
		_ = resp.Body.Close()
		if rerr := c.oauth.refreshTokens(ctx); rerr != nil {
			return nil, fmt.Errorf("mcp %s: oauth refresh: %w", c.cfg.Name, rerr)
		}
		return c.doPOSTOnce(ctx, body)
	}
	return resp, nil
}

func (c *HTTPClient) doPOSTOnce(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptMCP)
	c.applyHeaders(req)
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set(headerSessionID, sid)
	}
	return c.hc.Do(req)
}

func (c *HTTPClient) applyHeaders(req *http.Request) {
	for k, v := range c.cfg.Headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		req.Header.Set(k, v)
	}
}

func (c *HTTPClient) markDead(err error) {
	c.closed.Store(true)
	c.exitMu.Lock()
	if c.exitErr == nil {
		c.exitErr = err
	}
	c.exitMu.Unlock()
}

func httpStatusErr(resp *http.Response) error {
	// Do not include response body: may contain tokens or auth challenges.
	return fmt.Errorf("http %s", resp.Status)
}

// readSSEJSONRPC reads an SSE stream until a JSON-RPC response with wantID arrives.
func readSSEJSONRPC(r io.Reader, wantID int64) (rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var data []string
	tryFlush := func() (rpcResponse, bool) {
		if len(data) == 0 {
			return rpcResponse{}, false
		}
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		data = nil
		if payload == "" || payload == "[DONE]" {
			return rpcResponse{}, false
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return rpcResponse{}, false
		}
		if resp.Result == nil && resp.Error == nil {
			return rpcResponse{}, false
		}
		if resp.ID != wantID {
			return rpcResponse{}, false
		}
		return resp, true
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if resp, ok := tryFlush(); ok {
				return resp, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if resp, ok := tryFlush(); ok {
		return resp, nil
	}
	if err := sc.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, fmt.Errorf("sse closed without response")
}
