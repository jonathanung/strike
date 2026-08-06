package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// ToolHandler handles one tools/call. text is the tool content; isError marks a
// tool-level failure (JSON-RPC success with isError=true). err is a transport /
// internal failure (JSON-RPC error object).
type ToolHandler func(ctx context.Context, args json.RawMessage) (text string, isError bool, err error)

// ServerTool is one tool advertised by a tools-only MCP server.
type ServerTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handle      ToolHandler
}

// Server is a tools-only MCP server over newline-delimited JSON-RPC (stdio).
// It inverts the client in this package: hosts such as Claude Code or Codex
// connect as MCP clients and call advertised tools.
type Server struct {
	Name    string
	Version string
	Tools   []ServerTool

	writeMu sync.Mutex
}

// Serve reads JSON-RPC requests from in and writes responses to out until in
// EOF, ctx cancel, or a fatal I/O error. Logging must not use out (stdout is
// the MCP wire).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if s == nil {
		return fmt.Errorf("mcp server: nil receiver")
	}
	name := s.Name
	if name == "" {
		name = "strike"
	}
	version := s.Version
	if version == "" {
		version = "dev"
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	initialized := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return fmt.Errorf("mcp server: read: %w", err)
			}
			return nil
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := s.handleLine(ctx, out, line, name, version, &initialized); err != nil {
			return err
		}
	}
}

func (s *Server) handleLine(ctx context.Context, out io.Writer, line []byte, name, version string, initialized *bool) error {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		// Unparseable traffic: ignore (matches client tolerance).
		return nil
	}
	if msg.Method == "" {
		return nil
	}

	// Notifications have no id (absent or JSON null).
	isNotification := len(msg.ID) == 0 || string(msg.ID) == "null"
	if isNotification {
		switch msg.Method {
		case "notifications/initialized", "initialized":
			*initialized = true
		}
		return nil
	}

	switch msg.Method {
	case "initialize":
		*initialized = true
		return s.writeResult(out, msg.ID, initializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities: map[string]any{
				"tools": map[string]any{},
			},
			ServerInfo: implementationInfo{Name: name, Version: version},
		})
	case "ping":
		return s.writeResult(out, msg.ID, map[string]any{})
	case "tools/list":
		tools := make([]toolInfo, 0, len(s.Tools))
		for _, t := range s.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, toolInfo{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}
		return s.writeResult(out, msg.ID, listToolsResult{Tools: tools})
	case "tools/call":
		var p callToolParams
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return s.writeRPCError(out, msg.ID, -32602, "invalid tools/call params: "+err.Error())
			}
		}
		tool := s.lookupTool(p.Name)
		if tool == nil {
			return s.writeRPCError(out, msg.ID, -32601, fmt.Sprintf("unknown tool %q", p.Name))
		}
		if tool.Handle == nil {
			return s.writeRPCError(out, msg.ID, -32603, fmt.Sprintf("tool %q has no handler", p.Name))
		}
		args := p.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		text, isError, err := tool.Handle(ctx, args)
		if err != nil {
			return s.writeRPCError(out, msg.ID, -32000, err.Error())
		}
		res := callToolResult{
			Content: []contentBlock{{Type: "text", Text: text}},
			IsError: isError,
		}
		return s.writeResult(out, msg.ID, res)
	default:
		return s.writeRPCError(out, msg.ID, -32601, "method not found: "+msg.Method)
	}
}

func (s *Server) lookupTool(name string) *ServerTool {
	for i := range s.Tools {
		if s.Tools[i].Name == name {
			return &s.Tools[i]
		}
	}
	return nil
}

func (s *Server) writeResult(out io.Writer, id json.RawMessage, result any) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return s.writeRPCError(out, id, -32603, "encode result: "+err.Error())
	}
	return s.writeMessage(out, map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"result":  json.RawMessage(payload),
	})
}

func (s *Server) writeRPCError(out io.Writer, id json.RawMessage, code int, message string) error {
	return s.writeMessage(out, map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return json.RawMessage(id)
}

func (s *Server) writeMessage(out io.Writer, msg map[string]any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("mcp server: encode: %w", err)
	}
	data = append(data, '\n')
	if _, err := out.Write(data); err != nil {
		return fmt.Errorf("mcp server: write: %w", err)
	}
	return nil
}
