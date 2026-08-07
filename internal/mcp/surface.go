package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// Host-facing typed tools for prompts and resources (one pair per server).
// Names: mcp_<server>_list_prompts, mcp_<server>_get_prompt,
// mcp_<server>_list_resources, mcp_<server>_read_resource.

func registerSurfaceTools(reg *tool.Registry, client session, caps ServerCaps) []string {
	if reg == nil || client == nil {
		return nil
	}
	var names []string
	server := client.Name()
	if caps.Prompts {
		for _, t := range []tool.Tool{
			newListPromptsTool(client),
			newGetPromptTool(client),
		} {
			reg.Register(t)
			names = append(names, t.Name())
		}
	}
	if caps.Resources {
		for _, t := range []tool.Tool{
			newListResourcesTool(client),
			newReadResourceTool(client),
		} {
			reg.Register(t)
			names = append(names, t.Name())
		}
	}
	_ = server
	return names
}

type listPromptsTool struct {
	client session
	name   string
}

func newListPromptsTool(client session) tool.Tool {
	return &listPromptsTool{
		client: client,
		name:   NamespaceTool(client.Name(), "list_prompts"),
	}
}

func (t *listPromptsTool) Name() string { return t.name }
func (t *listPromptsTool) Description() string {
	return fmt.Sprintf("[mcp:%s] List prompt templates advertised by this MCP server.", t.client.Name())
}
func (t *listPromptsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *listPromptsTool) Contract() tool.Contract {
	return tool.Contract{Version: tool.ContractVersion, SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafeRetry}
}
func (t *listPromptsTool) Execute(ctx context.Context, _ json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := askMCP(ctx, tc, t.client.Name(), "prompts/*"); err != nil {
		return tool.Result{}, err
	}
	if t.client.Closed() {
		return tool.Result{}, mapMCPError(t.client.deadErr())
	}
	listCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	prompts, err := t.client.ListPrompts(listCtx)
	if err != nil {
		return tool.Result{}, mapMCPError(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "prompts (%d) from mcp:%s\n", len(prompts), t.client.Name())
	for _, p := range prompts {
		fmt.Fprintf(&b, "- %s", p.Name)
		if p.Description != "" {
			fmt.Fprintf(&b, ": %s", BoundText(p.Description))
		}
		if len(p.Arguments) > 0 {
			args := make([]string, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				n := a.Name
				if a.Required {
					n += "*"
				}
				args = append(args, n)
			}
			fmt.Fprintf(&b, " args=[%s]", strings.Join(args, ", "))
		}
		b.WriteByte('\n')
	}
	return tool.Result{
		Title:    t.name,
		Output:   strings.TrimRight(b.String(), "\n"),
		Metadata: ProvenanceMeta(t.client.Name(), "prompts", "list"),
	}, nil
}

type getPromptTool struct {
	client session
	name   string
}

func newGetPromptTool(client session) tool.Tool {
	return &getPromptTool{client: client, name: NamespaceTool(client.Name(), "get_prompt")}
}

func (t *getPromptTool) Name() string { return t.name }
func (t *getPromptTool) Description() string {
	return fmt.Sprintf("[mcp:%s] Fetch a named prompt template (optional arguments object).", t.client.Name())
}
func (t *getPromptTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"name":{"type":"string","description":"Prompt name from list_prompts"},
			"arguments":{"type":"object","additionalProperties":{"type":"string"},"description":"Prompt argument values"}
		},
		"required":["name"]
	}`)
}
func (t *getPromptTool) Contract() tool.Contract {
	return tool.Contract{Version: tool.ContractVersion, SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafeRetry}
}
func (t *getPromptTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, tool.ErrInvalidArgs("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(a.Name) == "" {
		return tool.Result{}, tool.ErrInvalidArgs("name is required")
	}
	if len(a.Arguments) > MaxPromptArgs {
		return tool.Result{}, tool.ErrInvalidArgs(fmt.Sprintf("at most %d arguments", MaxPromptArgs))
	}
	if err := askMCP(ctx, tc, t.client.Name(), "prompts/"+a.Name); err != nil {
		return tool.Result{}, err
	}
	if t.client.Closed() {
		return tool.Result{}, mapMCPError(t.client.deadErr())
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	res, err := t.client.GetPrompt(callCtx, a.Name, a.Arguments)
	if err != nil {
		return tool.Result{}, mapMCPError(err)
	}
	out := FormatPromptResult(t.client.Name(), a.Name, res)
	return tool.Result{
		Title:    t.name,
		Output:   out,
		Metadata: ProvenanceMeta(t.client.Name(), "prompt", a.Name),
	}, nil
}

type listResourcesTool struct {
	client session
	name   string
}

func newListResourcesTool(client session) tool.Tool {
	return &listResourcesTool{client: client, name: NamespaceTool(client.Name(), "list_resources")}
}

func (t *listResourcesTool) Name() string { return t.name }
func (t *listResourcesTool) Description() string {
	return fmt.Sprintf("[mcp:%s] List resources advertised by this MCP server.", t.client.Name())
}
func (t *listResourcesTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *listResourcesTool) Contract() tool.Contract {
	return tool.Contract{Version: tool.ContractVersion, SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafeRetry}
}
func (t *listResourcesTool) Execute(ctx context.Context, _ json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := askMCP(ctx, tc, t.client.Name(), "resources/*"); err != nil {
		return tool.Result{}, err
	}
	if t.client.Closed() {
		return tool.Result{}, mapMCPError(t.client.deadErr())
	}
	listCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	resources, err := t.client.ListResources(listCtx)
	if err != nil {
		return tool.Result{}, mapMCPError(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resources (%d) from mcp:%s\n", len(resources), t.client.Name())
	for _, r := range resources {
		fmt.Fprintf(&b, "- %s", BoundText(r.URI))
		if r.Name != "" {
			fmt.Fprintf(&b, " (%s)", BoundText(r.Name))
		}
		if r.Description != "" {
			fmt.Fprintf(&b, ": %s", BoundText(r.Description))
		}
		b.WriteByte('\n')
	}
	return tool.Result{
		Title:    t.name,
		Output:   strings.TrimRight(b.String(), "\n"),
		Metadata: ProvenanceMeta(t.client.Name(), "resources", "list"),
	}, nil
}

type readResourceTool struct {
	client session
	name   string
}

func newReadResourceTool(client session) tool.Tool {
	return &readResourceTool{client: client, name: NamespaceTool(client.Name(), "read_resource")}
}

func (t *readResourceTool) Name() string { return t.name }
func (t *readResourceTool) Description() string {
	return fmt.Sprintf("[mcp:%s] Read a resource by URI from list_resources.", t.client.Name())
}
func (t *readResourceTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"uri":{"type":"string","description":"Resource URI"}
		},
		"required":["uri"]
	}`)
}
func (t *readResourceTool) Contract() tool.Contract {
	return tool.Contract{Version: tool.ContractVersion, SideEffect: tool.SideEffectRead, Idempotency: tool.IdempotencySafeRetry}
}
func (t *readResourceTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, tool.ErrInvalidArgs("invalid arguments: " + err.Error())
	}
	uri := strings.TrimSpace(a.URI)
	if uri == "" {
		return tool.Result{}, tool.ErrInvalidArgs("uri is required")
	}
	if utf8.RuneCountInString(uri) > MaxResourceURIRunes {
		return tool.Result{}, tool.ErrInvalidArgs("uri too long")
	}
	if err := askMCP(ctx, tc, t.client.Name(), "resources/"+uri); err != nil {
		return tool.Result{}, err
	}
	if t.client.Closed() {
		return tool.Result{}, mapMCPError(t.client.deadErr())
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	res, err := t.client.ReadResource(callCtx, uri)
	if err != nil {
		return tool.Result{}, mapMCPError(err)
	}
	out := FormatResourceResult(t.client.Name(), res)
	return tool.Result{
		Title:    t.name,
		Output:   out,
		Metadata: ProvenanceMeta(t.client.Name(), "resource", uri),
	}, nil
}

func askMCP(ctx context.Context, tc *tool.Context, server, pattern string) error {
	if tc == nil || tc.Ask == nil {
		return tool.ErrInternal("mcp: permission ask unavailable")
	}
	return tc.Ask(ctx, tool.AskRequest{
		Permission: Permission,
		Patterns:   []string{server + "/" + pattern},
		Always:     []string{server + "/*", server + "/" + pattern},
	})
}
