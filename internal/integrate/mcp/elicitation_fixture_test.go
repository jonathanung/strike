package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// runElicitFixture is a real stdio MCP server (run as the test helper
// process) whose tools pause mid tools/call and send elicitation/create to
// the client, then finish from the answer (#1353):
//
//   - ask_name: form mode; replies "hello <name>", "declined", or "cancelled".
//   - confirm_link: url mode; replies "confirmed" on accept.
//
// It refuses to elicit ("no-elicitation-capability") unless initialize
// declared the elicitation capability, and it uses a string request id so
// the client's id handling is exercised.
func runElicitFixture() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	declared := false
	next := func() (map[string]json.RawMessage, bool) {
		for sc.Scan() {
			var msg map[string]json.RawMessage
			if json.Unmarshal(sc.Bytes(), &msg) == nil {
				return msg, true
			}
		}
		return nil, false
	}
	send := func(v any) {
		data, _ := json.Marshal(v)
		fmt.Printf("%s\n", data)
	}
	text := func(id json.RawMessage, s string) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": s}},
		}})
	}
	seq := 0
	elicit := func(params map[string]any) (action string, content map[string]any) {
		seq++
		reqID := fmt.Sprintf("elicit-%d", seq)
		send(map[string]any{"jsonrpc": "2.0", "id": reqID, "method": "elicitation/create", "params": params})
		for {
			msg, ok := next()
			if !ok {
				return "", nil
			}
			var id string
			if json.Unmarshal(msg["id"], &id) != nil || id != reqID {
				continue
			}
			var res struct {
				Action  string         `json:"action"`
				Content map[string]any `json:"content"`
			}
			_ = json.Unmarshal(msg["result"], &res)
			return res.Action, res.Content
		}
	}
	for {
		msg, ok := next()
		if !ok {
			return
		}
		id, hasID := msg["id"]
		var method string
		_ = json.Unmarshal(msg["method"], &method)
		if !hasID || method == "" {
			continue
		}
		switch method {
		case "initialize":
			var p struct {
				Capabilities map[string]json.RawMessage `json:"capabilities"`
			}
			_ = json.Unmarshal(msg["params"], &p)
			_, declared = p.Capabilities["elicitation"]
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "elicit-fixture", "version": "0.0.1"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": []map[string]any{
				{"name": "ask_name", "description": "asks the user for a name", "inputSchema": map[string]any{"type": "object"}},
				{"name": "confirm_link", "description": "asks the user to confirm on a page", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			var p callToolParams
			_ = json.Unmarshal(msg["params"], &p)
			if !declared {
				text(id, "no-elicitation-capability")
				continue
			}
			switch p.Name {
			case "ask_name":
				action, content := elicit(map[string]any{
					"message": "Which name should the greeting use?",
					"requestedSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":  map[string]any{"type": "string", "title": "Name", "default": "world"},
							"loud":  map[string]any{"type": "boolean", "title": "Shout it"},
							"color": map[string]any{"type": "string", "enum": []string{"red", "blue"}},
						},
						"required": []string{"name"},
					},
				})
				switch action {
				case "accept":
					text(id, fmt.Sprintf("hello %v", content["name"]))
				case "decline":
					text(id, "declined")
				default:
					text(id, "cancelled")
				}
			case "confirm_link":
				action, _ := elicit(map[string]any{
					"mode":          "url",
					"message":       "Confirm the payment on the provider's page.",
					"url":           "https://example.test/confirm?id=1",
					"elicitationId": "pay-1",
				})
				if action == "accept" {
					text(id, "confirmed")
				} else {
					text(id, "not confirmed: "+action)
				}
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "unknown tool"}})
			}
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
}
