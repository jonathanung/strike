package acp

import "encoding/json"

// ProtocolVersion is the stable ACP wire version we speak.
// See https://agentclientprotocol.com/protocol/overview
const ProtocolVersion = 1

// Stop reasons (session/prompt result).
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

// ContentBlock is a minimal ACP content block (text + resource_link baseline).
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	// Resource embeds full resource content when type=resource.
	Resource *EmbeddedResource `json:"resource,omitempty"`
	Data     string            `json:"data,omitempty"` // image/audio base64
}

// EmbeddedResource is the body of a content block with type "resource".
type EmbeddedResource struct {
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// InitializeParams is the client → agent initialize request body.
type InitializeParams struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ClientCapabilities map[string]any  `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation `json:"clientInfo,omitempty"`
}

// Implementation identifies a client or agent binary.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// InitializeResult is the agent → client initialize response.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []any             `json:"authMethods"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

// AgentCapabilities advertised during initialize.
type AgentCapabilities struct {
	LoadSession         bool               `json:"loadSession"`
	PromptCapabilities  PromptCapabilities `json:"promptCapabilities"`
	McpCapabilities     McpCapabilities    `json:"mcpCapabilities"`
	SessionCapabilities map[string]any     `json:"sessionCapabilities,omitempty"`
}

// LoadSessionParams is session/load request body (ACP subset).
type LoadSessionParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
}

// LoadSessionResult is session/load response body.
type LoadSessionResult struct {
	SessionID string `json:"sessionId"`
}

// PromptCapabilities lists optional prompt content types.
type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// McpCapabilities lists optional MCP transports (stdio is always required).
type McpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// NewSessionParams is session/new request body.
type NewSessionParams struct {
	Cwd                   string          `json:"cwd"`
	McpServers            json.RawMessage `json:"mcpServers"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
}

// NewSessionResult is session/new response body.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
}

// PromptParams is session/prompt request body.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptResult is session/prompt response body.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// CancelParams is session/cancel notification body.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionUpdateNotification is session/update params.
type SessionUpdateNotification struct {
	SessionID string         `json:"sessionId"`
	Update    map[string]any `json:"update"`
}

// RequestPermissionParams is session/request_permission request body (agent → client).
type RequestPermissionParams struct {
	SessionID string          `json:"sessionId"`
	ToolCall  map[string]any  `json:"toolCall"`
	Options   []PermissionOpt `json:"options"`
}

// PermissionOpt is one choice on a permission request.
type PermissionOpt struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// RequestPermissionResult is the client response to session/request_permission.
type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is cancelled or selected.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // cancelled | selected
	OptionID string `json:"optionId,omitempty"`
}
