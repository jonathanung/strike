package mcp

import "encoding/json"

// ProtocolVersion is the MCP revision this client speaks.
const ProtocolVersion = "2024-11-05"

// Content size limits for external MCP payloads (prompts/resources/tool text).
const (
	MaxContentBytes     = 1 << 20 // 1 MiB per text field
	MaxContentBlocks    = 64
	MaxResourceURIRunes = 2048
	MaxPromptArgs       = 32
)

// json-rpc 2.0 wire shapes (newline-delimited on stdio).

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"` // some servers put id:null; we detect method-only
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	// Method set on notifications that arrive on the response stream.
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
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

// ServerCaps is the negotiated server capability snapshot after initialize.
type ServerCaps struct {
	Tools                bool           `json:"tools"`
	ToolsListChanged     bool           `json:"toolsListChanged,omitempty"`
	Prompts              bool           `json:"prompts"`
	PromptsListChanged   bool           `json:"promptsListChanged,omitempty"`
	Resources            bool           `json:"resources"`
	ResourcesListChanged bool           `json:"resourcesListChanged,omitempty"`
	ResourcesSubscribe   bool           `json:"resourcesSubscribe,omitempty"`
	Logging              bool           `json:"logging,omitempty"`
	Raw                  map[string]any `json:"-"`
}

// ParseServerCaps extracts capability flags from initialize result.
func ParseServerCaps(raw map[string]any) ServerCaps {
	c := ServerCaps{Raw: raw}
	if raw == nil {
		return c
	}
	if t, ok := raw["tools"]; ok && t != nil {
		c.Tools = true
		if m, ok := t.(map[string]any); ok {
			c.ToolsListChanged = truthy(m["listChanged"])
		}
	}
	if p, ok := raw["prompts"]; ok && p != nil {
		c.Prompts = true
		if m, ok := p.(map[string]any); ok {
			c.PromptsListChanged = truthy(m["listChanged"])
		}
	}
	if r, ok := raw["resources"]; ok && r != nil {
		c.Resources = true
		if m, ok := r.(map[string]any); ok {
			c.ResourcesListChanged = truthy(m["listChanged"])
			c.ResourcesSubscribe = truthy(m["subscribe"])
		}
	}
	if l, ok := raw["logging"]; ok && l != nil {
		c.Logging = true
	}
	return c
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

// clientCapabilities advertises what Strike supports during initialize.
func clientCapabilities() map[string]any {
	return map[string]any{
		"roots":    map[string]any{"listChanged": false},
		"sampling": map[string]any{},
	}
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
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// Name is used by resource_link / embedded resource variants.
	Name string `json:"name,omitempty"`
}

// Prompts

type listPromptsResult struct {
	Prompts []promptInfo `json:"prompts"`
}

type promptInfo struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []promptArgument `json:"arguments,omitempty"`
}

type promptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type getPromptParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

type getPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []promptMessage `json:"messages"`
}

type promptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or content block / array
}

// Resources

type listResourcesResult struct {
	Resources []resourceInfo `json:"resources"`
}

type resourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type readResourceParams struct {
	URI string `json:"uri"`
}

type readResourceResult struct {
	Contents []resourceContents `json:"contents"`
}

type resourceContents struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64; we surface type only when oversized
}

// Catalog notification method names (MCP).
const (
	notifyToolsListChanged     = "notifications/tools/list_changed"
	notifyPromptsListChanged   = "notifications/prompts/list_changed"
	notifyResourcesListChanged = "notifications/resources/list_changed"
)
