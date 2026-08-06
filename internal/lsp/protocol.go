// Package lsp is a Language Server Protocol client: JSON-RPC 2.0 over stdio
// (Content-Length framing), initialize handshake, document sync, and
// publishDiagnostics collection. Crash isolation mirrors internal/mcp — a
// dead language server degrades to no diagnostics and never takes down the
// session.
package lsp

import "encoding/json"

// LSP protocol version string sent in initialize.
const ProtocolVersion = "3.17.0"

// json-rpc 2.0 wire shapes (Content-Length framed on stdio).

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponseOut struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

// inboundMessage is any JSON-RPC message from the server.
type inboundMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// --- initialize ---

type initializeParams struct {
	ProcessID             *int               `json:"processId"`
	ClientInfo            implementationInfo `json:"clientInfo"`
	RootURI               string             `json:"rootUri,omitempty"`
	RootPath              string             `json:"rootPath,omitempty"`
	Capabilities          clientCapabilities `json:"capabilities"`
	WorkspaceFolders      any                `json:"workspaceFolders"`
	InitializationOptions any                `json:"initializationOptions,omitempty"`
}

type implementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type clientCapabilities struct {
	TextDocument textDocumentClientCapabilities `json:"textDocument,omitempty"`
	Workspace    workspaceClientCapabilities    `json:"workspace,omitempty"`
}

type textDocumentClientCapabilities struct {
	Synchronization    *syncCapabilities        `json:"synchronization,omitempty"`
	PublishDiagnostics *publishDiagCapabilities `json:"publishDiagnostics,omitempty"`
}

type syncCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	WillSave            bool `json:"willSave"`
	WillSaveWaitUntil   bool `json:"willSaveWaitUntil"`
	DidSave             bool `json:"didSave"`
}

type publishDiagCapabilities struct {
	RelatedInformation bool `json:"relatedInformation"`
	VersionSupport     bool `json:"versionSupport"`
}

type workspaceClientCapabilities struct {
	WorkspaceFolders bool `json:"workspaceFolders"`
	Configuration    bool `json:"configuration"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   implementationInfo `json:"serverInfo"`
}

type serverCapabilities struct {
	TextDocumentSync json.RawMessage `json:"textDocumentSync,omitempty"`
}

// --- textDocument sync ---

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []textDocumentContentChange     `json:"contentChanges"`
}

type textDocumentContentChange struct {
	Text string `json:"text"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// --- diagnostics ---

// DiagnosticSeverity matches LSP severity values.
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

// Position is a zero-based line/character offset (LSP).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is one publishDiagnostics entry (public for callers).
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}
