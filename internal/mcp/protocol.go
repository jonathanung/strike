package mcp

import "encoding/json"

// ProtocolVersion is the MCP revision this client speaks (stdio tools-only).
const ProtocolVersion = "2024-11-05"

// json-rpc 2.0 wire shapes (newline-delimited on stdio).

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities"`
	ClientInfo      implementationInfo `json:"clientInfo"`
}

type implementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities"`
	ServerInfo      implementationInfo `json:"serverInfo"`
}

type listToolsResult struct {
	Tools []toolInfo `json:"tools"`
}

type toolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type callToolResult struct {
	Content           []contentBlock  `json:"content"`
	IsError           bool            `json:"isError,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
