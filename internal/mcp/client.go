package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultInitTimeout = 30 * time.Second
	defaultCallTimeout = 120 * time.Second
	maxLineBytes       = 16 << 20 // 16 MiB per JSON-RPC message
)

// Client is a connected stdio MCP server process.
type Client struct {
	cfg ServerConfig

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	writeMu sync.Mutex
	pendMu  sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan rpcResponse

	readerDone chan struct{}
	waitDone   chan struct{}

	closed  atomic.Bool
	exitMu  sync.Mutex
	exitErr error
}

// startStdio launches the server subprocess and completes the MCP initialize handshake.
func startStdio(ctx context.Context, cfg ServerConfig) (*Client, error) {
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
		return nil, fmt.Errorf("mcp %s: stdin pipe: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp %s: stdout pipe: %w", cfg.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("mcp %s: stderr pipe: %w", cfg.Name, err)
	}

	c := &Client{
		cfg:        cfg,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		pending:    make(map[int64]chan rpcResponse),
		readerDone: make(chan struct{}),
		waitDone:   make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("mcp %s: start %q: %w", cfg.Name, cfg.Command, err)
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
		if i := indexByte(kv, '='); i > 0 {
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

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func (c *Client) initialize(ctx context.Context) error {
	var result initializeResult
	if err := c.call(ctx, "initialize", initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      implementationInfo{Name: "strike", Version: "1"},
	}, &result); err != nil {
		return fmt.Errorf("mcp %s: initialize: %w", c.cfg.Name, err)
	}
	if err := c.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("mcp %s: initialized notify: %w", c.cfg.Name, err)
	}
	return nil
}

// ListTools returns tools advertised by the server.
func (c *Client) ListTools(ctx context.Context) ([]toolInfo, error) {
	if c.Closed() {
		return nil, c.deadErr()
	}
	var result listToolsResult
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: tools/list: %w", c.cfg.Name, err)
	}
	return result.Tools, nil
}

// CallTool invokes a server tool by its MCP name (not the strike namespace).
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (callToolResult, error) {
	if c.Closed() {
		return callToolResult{}, c.deadErr()
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

// Name returns the config server name.
func (c *Client) Name() string { return c.cfg.Name }

// Closed reports whether the client is shut down or the process exited.
func (c *Client) Closed() bool { return c.closed.Load() }

func (c *Client) deadErr() error {
	c.exitMu.Lock()
	err := c.exitErr
	c.exitMu.Unlock()
	if err != nil {
		return fmt.Errorf("mcp server %q unavailable: %w", c.cfg.Name, err)
	}
	return fmt.Errorf("mcp server %q unavailable", c.cfg.Name)
}

// Close terminates the subprocess gracefully (close stdin, wait, then kill).
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		// Already closing/closed: wait for process exit.
		<-c.waitDone
		return nil
	}
	_ = c.stdin.Close()

	select {
	case <-c.waitDone:
	case <-time.After(3 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.waitDone
	}

	c.failPending(fmt.Errorf("mcp server %q closed", c.cfg.Name))
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
	c.failPending(fmt.Errorf("mcp server %q exited", c.cfg.Name))
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
	sc := bufio.NewScanner(c.stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.pendMu.Unlock()
		if ok {
			select {
			case ch <- resp:
			default:
			}
		}
	}
	c.failPending(fmt.Errorf("mcp server %q stdout closed", c.cfg.Name))
}

func (c *Client) failPending(err error) {
	c.pendMu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan rpcResponse)
	c.pendMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcResponse{Error: &rpcError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c.Closed() {
		return c.deadErr()
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	c.pendMu.Lock()
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
		if result == nil || len(resp.Result) == 0 {
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
	return c.writeJSON(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

func (c *Client) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.Closed() {
		return c.deadErr()
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}
