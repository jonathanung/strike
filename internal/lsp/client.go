package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultInitTimeout = 30 * time.Second
	maxMessageBytes    = 32 << 20 // 32 MiB per JSON-RPC message
)

// Client is a connected stdio language server process.
type Client struct {
	cfg ServerConfig

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex
	pendMu  sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan inboundMessage

	// openDocs tracks document versions by URI for didOpen/didChange.
	docMu    sync.Mutex
	openDocs map[string]int // uri -> version

	// diagnostics stores latest publishDiagnostics per URI.
	diagMu       sync.Mutex
	diagnostics  map[string][]Diagnostic
	onDiagnostic func(uri string, diags []Diagnostic) // optional; set by Manager

	readerDone chan struct{}
	waitDone   chan struct{}

	closed  atomic.Bool
	exitMu  sync.Mutex
	exitErr error
}

// Start launches the language server subprocess and completes initialize.
func Start(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if err := validateServerConfig(cfg); err != nil {
		return nil, err
	}
	// Detach from parent cancel so the process lives for the session; Close kills it.
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	cmd.Env = buildEnv(cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp %s: stdin pipe: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("lsp %s: stdout pipe: %w", cfg.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("lsp %s: stderr pipe: %w", cfg.Name, err)
	}

	c := &Client{
		cfg:         cfg,
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		stderr:      stderr,
		pending:     make(map[int64]chan inboundMessage),
		openDocs:    make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
		readerDone:  make(chan struct{}),
		waitDone:    make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("lsp %s: start %q: %w", cfg.Name, cfg.Command, err)
	}

	go c.drainStderr()
	go c.readLoop()
	go c.waitProcess()

	initCtx, cancel := context.WithTimeout(ctx, defaultInitTimeout)
	defer cancel()
	if err := c.initialize(initCtx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func buildEnv(overlay map[string]string) []string {
	base := os.Environ()
	if len(overlay) == 0 {
		return base
	}
	seen := make(map[string]string, len(base)+len(overlay))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			seen[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range overlay {
		if k == "" {
			continue
		}
		seen[k] = v
	}
	out := make([]string, 0, len(seen))
	for k, v := range seen {
		out = append(out, k+"="+v)
	}
	return out
}

func (c *Client) initialize(ctx context.Context) error {
	rootURI := ""
	rootPath := ""
	if c.cfg.RootDir != "" {
		rootPath = c.cfg.RootDir
		rootURI = PathToURI(c.cfg.RootDir)
	}
	pid := os.Getpid()
	var result initializeResult
	if err := c.call(ctx, "initialize", initializeParams{
		ProcessID: &pid,
		ClientInfo: implementationInfo{
			Name:    "strike",
			Version: "1",
		},
		RootURI:  rootURI,
		RootPath: rootPath,
		Capabilities: clientCapabilities{
			TextDocument: textDocumentClientCapabilities{
				Synchronization: &syncCapabilities{
					DynamicRegistration: false,
					DidSave:             false,
				},
				PublishDiagnostics: &publishDiagCapabilities{
					RelatedInformation: true,
					VersionSupport:     true,
				},
			},
			Workspace: workspaceClientCapabilities{
				WorkspaceFolders: false,
				Configuration:    true,
			},
		},
		WorkspaceFolders: nil,
	}, &result); err != nil {
		return fmt.Errorf("lsp %s: initialize: %w", c.cfg.Name, err)
	}
	if err := c.notify(ctx, "initialized", map[string]any{}); err != nil {
		return fmt.Errorf("lsp %s: initialized notify: %w", c.cfg.Name, err)
	}
	return nil
}

// Name returns the config server name.
func (c *Client) Name() string { return c.cfg.Name }

// Closed reports whether the client is shut down or the process exited.
func (c *Client) Closed() bool { return c.closed.Load() }

func (c *Client) deadErr() error {
	c.exitMu.Lock()
	err := c.exitErr
	c.exitMu.Unlock()
	if err != nil {
		return fmt.Errorf("lsp server %q unavailable: %w", c.cfg.Name, err)
	}
	return fmt.Errorf("lsp server %q unavailable", c.cfg.Name)
}

// DidOpenOrChange sends textDocument/didOpen or didChange for path with full content.
// No-op (nil error) when the client is dead — crash isolation.
func (c *Client) DidOpenOrChange(ctx context.Context, path, content string) error {
	if c == nil || c.Closed() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	uri := PathToURI(path)
	lang := languageID(path)

	c.docMu.Lock()
	ver, open := c.openDocs[uri]
	if open {
		ver++
		c.openDocs[uri] = ver
	} else {
		ver = 1
		c.openDocs[uri] = ver
	}
	c.docMu.Unlock()

	if open {
		return c.notify(ctx, "textDocument/didChange", didChangeParams{
			TextDocument: versionedTextDocumentIdentifier{URI: uri, Version: ver},
			ContentChanges: []textDocumentContentChange{
				{Text: content},
			},
		})
	}
	return c.notify(ctx, "textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{
			URI:        uri,
			LanguageID: lang,
			Version:    ver,
			Text:       content,
		},
	})
}

// DidClose sends textDocument/didClose and drops local diagnostics for path.
// No-op when dead or the document was never opened.
func (c *Client) DidClose(ctx context.Context, path string) error {
	if c == nil || c.Closed() {
		return nil
	}
	uri := PathToURI(path)
	c.docMu.Lock()
	_, open := c.openDocs[uri]
	if open {
		delete(c.openDocs, uri)
	}
	c.docMu.Unlock()
	if !open {
		c.clearDiagnostics(uri)
		return nil
	}
	err := c.notify(ctx, "textDocument/didClose", didCloseParams{
		TextDocument: textDocumentIdentifier{URI: uri},
	})
	c.clearDiagnostics(uri)
	return err
}

// Diagnostics returns a copy of the latest diagnostics for path (by file URI).
func (c *Client) Diagnostics(path string) []Diagnostic {
	if c == nil {
		return nil
	}
	uri := PathToURI(path)
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	src := c.diagnostics[uri]
	if len(src) == 0 {
		return nil
	}
	out := make([]Diagnostic, len(src))
	copy(out, src)
	return out
}

// AllDiagnostics returns a copy of all stored diagnostics keyed by file path.
func (c *Client) AllDiagnostics() map[string][]Diagnostic {
	if c == nil {
		return nil
	}
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	if len(c.diagnostics) == 0 {
		return nil
	}
	out := make(map[string][]Diagnostic, len(c.diagnostics))
	for uri, diags := range c.diagnostics {
		cp := make([]Diagnostic, len(diags))
		copy(cp, diags)
		out[URIToPath(uri)] = cp
	}
	return out
}

func (c *Client) clearDiagnostics(uri string) {
	c.diagMu.Lock()
	delete(c.diagnostics, uri)
	c.diagMu.Unlock()
	if c.onDiagnostic != nil {
		c.onDiagnostic(uri, nil)
	}
}

func (c *Client) storeDiagnostics(uri string, diags []Diagnostic) {
	c.diagMu.Lock()
	if len(diags) == 0 {
		delete(c.diagnostics, uri)
	} else {
		cp := make([]Diagnostic, len(diags))
		copy(cp, diags)
		c.diagnostics[uri] = cp
	}
	c.diagMu.Unlock()
	if c.onDiagnostic != nil {
		c.onDiagnostic(uri, diags)
	}
}

// Close terminates the language server (shutdown → exit, then kill if needed).
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if !c.closed.CompareAndSwap(false, true) {
		<-c.waitDone
		return nil
	}

	// Best-effort LSP shutdown handshake; ignore errors (process may be dead).
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = c.callUnlocked(shutCtx, "shutdown", nil, nil)
	cancel()
	_ = c.writeJSON(rpcNotification{JSONRPC: "2.0", Method: "exit"})

	_ = c.stdin.Close()

	select {
	case <-c.waitDone:
	case <-time.After(3 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.waitDone
	}

	c.failPending(fmt.Errorf("lsp server %q closed", c.cfg.Name))
	<-c.readerDone

	c.exitMu.Lock()
	if c.exitErr == nil {
		c.exitErr = fmt.Errorf("closed")
	}
	c.exitMu.Unlock()
	return nil
}

func (c *Client) waitProcess() {
	err := c.cmd.Wait()
	c.closed.Store(true)
	c.exitMu.Lock()
	if c.exitErr == nil {
		if err != nil {
			c.exitErr = err
		} else {
			c.exitErr = fmt.Errorf("exited")
		}
	}
	c.exitMu.Unlock()
	c.failPending(fmt.Errorf("lsp server %q exited", c.cfg.Name))
	_ = c.stdin.Close()
	close(c.waitDone)
}

func (c *Client) drainStderr() {
	defer c.stderr.Close()
	buf := make([]byte, 4096)
	for {
		_, err := c.stderr.Read(buf)
		if err != nil {
			return
		}
	}
}

func (c *Client) readLoop() {
	defer close(c.readerDone)
	r := bufio.NewReaderSize(c.stdout, 64*1024)
	for {
		body, err := readFrame(r)
		if err != nil {
			c.failPending(fmt.Errorf("lsp server %q stdout closed", c.cfg.Name))
			return
		}
		if len(body) == 0 {
			continue
		}
		var msg inboundMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		c.dispatch(msg)
	}
}

func (c *Client) dispatch(msg inboundMessage) {
	// Response to our request: has id, no method.
	if msg.Method == "" && len(msg.ID) > 0 {
		id, ok := parseID(msg.ID)
		if !ok {
			return
		}
		c.pendMu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.pendMu.Unlock()
		if ok {
			select {
			case ch <- msg:
			default:
			}
		}
		return
	}

	// Server request: method + id — reply so the server does not hang.
	if msg.Method != "" && len(msg.ID) > 0 {
		c.handleServerRequest(msg)
		return
	}

	// Notification.
	if msg.Method != "" {
		c.handleNotification(msg)
	}
}

func (c *Client) handleNotification(msg inboundMessage) {
	switch msg.Method {
	case "textDocument/publishDiagnostics":
		var p publishDiagnosticsParams
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return
		}
		c.storeDiagnostics(p.URI, p.Diagnostics)
	default:
		// Ignore window/logMessage, telemetry, etc.
	}
}

func (c *Client) handleServerRequest(msg inboundMessage) {
	var id any
	_ = json.Unmarshal(msg.ID, &id)

	var result any
	var rpcErr *rpcError
	switch msg.Method {
	case "workspace/configuration":
		// Return one null config item per requested item when possible.
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		n := len(params.Items)
		if n == 0 {
			n = 1
		}
		arr := make([]any, n)
		result = arr
	case "workspace/workspaceFolders":
		result = nil
	case "client/registerCapability", "client/unregisterCapability":
		result = map[string]any{}
	case "window/workDoneProgress/create":
		result = nil
	case "window/showMessageRequest":
		result = nil
	default:
		// Method not found — keep the server moving.
		rpcErr = &rpcError{Code: -32601, Message: "method not found: " + msg.Method}
	}
	_ = c.writeJSON(rpcResponseOut{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	})
}

func parseID(raw json.RawMessage) (int64, bool) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), true
	}
	return 0, false
}

func (c *Client) failPending(err error) {
	c.pendMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan inboundMessage)
	c.pendMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- inboundMessage{Error: &rpcError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c.Closed() {
		return c.deadErr()
	}
	return c.callUnlocked(ctx, method, params, result)
}

// callUnlocked sends a request even when closed is set (shutdown path).
func (c *Client) callUnlocked(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan inboundMessage, 1)
	c.pendMu.Lock()
	if c.pending == nil {
		c.pendMu.Unlock()
		return c.deadErr()
	}
	c.pending[id] = ch
	c.pendMu.Unlock()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.writeJSON(req); err != nil {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if result == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
		return nil
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Closed() {
		return c.deadErr()
	}
	return c.writeJSON(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (c *Client) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// readFrame reads one LSP Content-Length framed message body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "content-length:") {
			v := strings.TrimSpace(line[len("Content-Length:"):])
			// header key is case-insensitive; re-slice after colon
			if i := strings.IndexByte(line, ':'); i >= 0 {
				v = strings.TrimSpace(line[i+1:])
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("invalid Content-Length %q", v)
			}
			if n > maxMessageBytes {
				return nil, fmt.Errorf("Content-Length %d exceeds limit", n)
			}
			contentLength = n
		}
		// Ignore Content-Type and other headers.
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}
	if contentLength == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
