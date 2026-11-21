package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/tool"
)

func startElicitFixture(t *testing.T) *Client {
	t.Helper()
	cmd, args, env := helperCommand(t, "elicit")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, ServerConfig{Name: "fixture", Command: cmd, Args: args, Env: env})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c, ok := s.(*Client)
	if !ok {
		t.Fatalf("stdio session is %T", s)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestElicitationFormRoundTripAgainstFixture(t *testing.T) {
	cases := []struct {
		name   string
		answer ElicitResult
		want   string
	}{
		{"accept resumes the same call", ElicitResult{Action: "accept", Content: json.RawMessage(`{"name":"ada"}`)}, "hello ada"},
		{"decline is distinct", ElicitResult{Action: "decline"}, "declined"},
		{"cancel is distinct", ElicitResult{Action: "cancel"}, "cancelled"},
		{"unknown action is sent as cancel", ElicitResult{Action: "approve"}, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := startElicitFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			seenCh := make(chan ElicitParams, 1)
			res, err := c.CallToolElicit(ctx, "ask_name", nil, func(_ context.Context, p ElicitParams) ElicitResult {
				seenCh <- p
				return tc.answer
			})
			if err != nil {
				t.Fatalf("CallToolElicit: %v", err)
			}
			seen := <-seenCh
			if got := formatContent(res.Content); got != tc.want {
				t.Fatalf("result = %q, want %q", got, tc.want)
			}
			if seen.Mode != "form" || seen.Message != "Which name should the greeting use?" {
				t.Fatalf("params = %#v", seen)
			}
			if !strings.Contains(string(seen.RequestedSchema), `"name"`) {
				t.Fatalf("schema = %s", seen.RequestedSchema)
			}
		})
	}
}

func TestElicitationWithoutHandlerCancels(t *testing.T) {
	c := startElicitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, "ask_name", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := formatContent(res.Content); got != "cancelled" {
		t.Fatalf("result = %q, want cancelled (no call owns the request)", got)
	}
}

func TestElicitationURLModeAgainstFixture(t *testing.T) {
	c := startElicitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seenCh := make(chan ElicitParams, 1)
	res, err := c.CallToolElicit(ctx, "confirm_link", nil, func(_ context.Context, p ElicitParams) ElicitResult {
		seenCh <- p
		// Content on url mode is dropped before it reaches the server.
		return ElicitResult{Action: "accept", Content: json.RawMessage(`{"x":1}`)}
	})
	if err != nil {
		t.Fatalf("CallToolElicit: %v", err)
	}
	if got := formatContent(res.Content); got != "confirmed" {
		t.Fatalf("result = %q", got)
	}
	seen := <-seenCh
	if seen.Mode != "url" || seen.URL != "https://example.test/confirm?id=1" || len(seen.RequestedSchema) != 0 {
		t.Fatalf("params = %#v", seen)
	}
}

func TestBridgeRoutesElicitationThroughToolContext(t *testing.T) {
	c := startElicitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridge := newBridge(c, toolInfo{Name: "ask_name"})
	gotCh := make(chan tool.ElicitRequest, 1)
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return nil },
		Elicit: func(_ context.Context, req tool.ElicitRequest) tool.ElicitResponse {
			gotCh <- req
			return tool.ElicitResponse{Action: "accept", Content: json.RawMessage(`{"name":"grace"}`)}
		},
	}
	res, err := bridge.Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "hello grace" {
		t.Fatalf("output = %q", res.Output)
	}
	got := <-gotCh
	if got.Server != "fixture" || got.Mode != "form" || got.Message == "" {
		t.Fatalf("elicit request = %#v", got)
	}
}

func TestBridgeWithoutElicitCancelsInsteadOfHanging(t *testing.T) {
	c := startElicitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridge := newBridge(c, toolInfo{Name: "ask_name"})
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return nil },
	}
	res, err := bridge.Execute(ctx, json.RawMessage(`{}`), tc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "cancelled" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestClientCapabilitiesDeclareElicitationOnlyWhenServed(t *testing.T) {
	if _, ok := clientCapabilities(true)["elicitation"]; !ok {
		t.Fatal("stdio caps lack elicitation")
	}
	if _, ok := clientCapabilities(false)["elicitation"]; ok {
		t.Fatal("http caps must not declare elicitation until requests are routed")
	}
}

func TestNormalizeElicit(t *testing.T) {
	long := strings.Repeat("x", maxElicitMessageRunes+50)
	cases := []struct {
		name   string
		in     ElicitParams
		ok     bool
		mode   string
		schema string
	}{
		{"absent mode is form with empty schema", ElicitParams{Message: "m"}, true, "form", `{"type":"object","properties":{}}`},
		{"form keeps object schema", ElicitParams{Message: "m", RequestedSchema: json.RawMessage(`{"type":"object"}`)}, true, "form", `{"type":"object"}`},
		{"form rejects non-object schema", ElicitParams{RequestedSchema: json.RawMessage(`[1]`)}, false, "form", ""},
		{"form rejects oversized schema", ElicitParams{RequestedSchema: json.RawMessage(`{"a":"` + strings.Repeat("y", maxElicitSchemaBytes) + `"}`)}, false, "form", ""},
		{"url needs http(s)", ElicitParams{Mode: "url", URL: "javascript:alert(1)"}, false, "url", ""},
		{"url drops schema", ElicitParams{Mode: "url", URL: "https://x.test", RequestedSchema: json.RawMessage(`{}`)}, true, "url", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeElicit(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Mode != tc.mode || string(got.RequestedSchema) != tc.schema {
				t.Fatalf("got %#v", got)
			}
		})
	}
	got, _ := normalizeElicit(ElicitParams{Message: long})
	if n := len([]rune(got.Message)); n != maxElicitMessageRunes+1 {
		t.Fatalf("message runes = %d, want clamp + ellipsis", n)
	}
}

func TestServerRequestResponses(t *testing.T) {
	reg := &elicitors{}
	ping := serverRequestResponse(context.Background(), reg, rpcProbe{ID: json.RawMessage(`7`), Method: "ping"})
	if ping.Error != nil || string(ping.ID) != "7" {
		t.Fatalf("ping = %#v", ping)
	}
	unknown := serverRequestResponse(context.Background(), reg, rpcProbe{ID: json.RawMessage(`"a"`), Method: "sampling/createMessage"})
	if unknown.Error == nil || unknown.Error.Code != -32601 {
		t.Fatalf("unknown = %#v", unknown)
	}
	none := serverRequestResponse(context.Background(), reg, rpcProbe{ID: json.RawMessage(`1`), Method: methodElicitationCreate, Params: json.RawMessage(`{"message":"m"}`)})
	if r, ok := none.Result.(ElicitResult); !ok || r.Action != "cancel" {
		t.Fatalf("no handler = %#v", none.Result)
	}
}

func TestElicitorsRouteToNewestCall(t *testing.T) {
	reg := &elicitors{}
	first := reg.push(func(context.Context, ElicitParams) ElicitResult { return ElicitResult{Action: "decline"} })
	second := reg.push(func(context.Context, ElicitParams) ElicitResult { return ElicitResult{Action: "accept"} })
	if got := answerElicit(context.Background(), reg, json.RawMessage(`{"message":"m"}`)); got.Action != "accept" || string(got.Content) != "{}" {
		t.Fatalf("newest = %#v", got)
	}
	second()
	if got := answerElicit(context.Background(), reg, json.RawMessage(`{"message":"m"}`)); got.Action != "decline" || got.Content != nil {
		t.Fatalf("after remove = %#v", got)
	}
	first()
	if reg.current() != nil {
		t.Fatal("handlers left after removal")
	}
}

func TestRPCProbeIsRequest(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"jsonrpc":"2.0","id":3,"method":"elicitation/create"}`, true},
		{`{"jsonrpc":"2.0","id":"x","method":"ping"}`, true},
		{`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`, false},
		{`{"jsonrpc":"2.0","id":null,"method":"notifications/x"}`, false},
		{`{"jsonrpc":"2.0","id":3,"result":{}}`, false},
	}
	for _, tc := range cases {
		var p rpcProbe
		if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
			t.Fatal(err)
		}
		if got := p.isRequest(); got != tc.want {
			t.Fatalf("%s: isRequest = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
