package elicit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/pkg/protocol"
)

type recorder struct {
	mu     sync.Mutex
	events []protocol.Event
}

func (r *recorder) emit(ev protocol.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []protocol.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Event(nil), r.events...)
}

func (r *recorder) waitRequested(t *testing.T) protocol.ElicitationRequested {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, ev := range r.snapshot() {
			if req, ok := ev.(protocol.ElicitationRequested); ok {
				return req
			}
		}
		select {
		case <-deadline:
			t.Fatal("no ElicitationRequested")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *recorder) resolved() []protocol.ElicitationResolved {
	var out []protocol.ElicitationResolved
	for _, ev := range r.snapshot() {
		if res, ok := ev.(protocol.ElicitationResolved); ok {
			out = append(out, res)
		}
	}
	return out
}

func TestAskRepliesByAction(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	cases := []struct {
		name        string
		reply       protocol.ElicitationReply
		wantAction  string
		wantContent string
	}{
		{"accept with object", protocol.ElicitationReply{Action: "accept", Content: json.RawMessage(`{"name":"ada"}`)}, "accept", `{"name":"ada"}`},
		{"accept without content sends empty object", protocol.ElicitationReply{Action: "accept"}, "accept", `{}`},
		{"accept with non-object content sends empty object", protocol.ElicitationReply{Action: "accept", Content: json.RawMessage(`"x"`)}, "accept", `{}`},
		{"decline drops content", protocol.ElicitationReply{Action: "decline", Content: json.RawMessage(`{"name":"x"}`)}, "decline", ""},
		{"cancel", protocol.ElicitationReply{Action: "cancel"}, "cancel", ""},
		{"unknown action is cancel", protocol.ElicitationReply{Action: "grant-everything"}, "cancel", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			svc := New(rec.emit)
			corr := protocol.Correlation{SessionID: "s1"}
			out := make(chan Response, 1)
			go func() {
				out <- svc.Ask(context.Background(), corr, Request{Server: "fixture", Message: "Who?", Mode: "form", RequestedSchema: schema})
			}()
			req := rec.waitRequested(t)
			if req.Server != "fixture" || req.Mode != "form" || string(req.RequestedSchema) != string(schema) || req.URL != "" {
				t.Fatalf("requested = %#v", req)
			}
			reply := tc.reply
			reply.RequestID = req.RequestID
			svc.Reply(reply)
			got := <-out
			if got.Action != tc.wantAction || string(got.Content) != tc.wantContent {
				t.Fatalf("response = %q %s, want %q %s", got.Action, got.Content, tc.wantAction, tc.wantContent)
			}
			res := rec.resolved()
			if len(res) != 1 || res[0].RequestID != req.RequestID || res[0].Action != tc.wantAction {
				t.Fatalf("resolved = %#v", res)
			}
		})
	}
}

func TestAskURLModeCarriesURLNotSchema(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit)
	out := make(chan Response, 1)
	go func() {
		out <- svc.Ask(context.Background(), protocol.Correlation{}, Request{
			Server: "pay", Message: "Confirm the payment", Mode: "url",
			URL: "https://example.test/confirm", RequestedSchema: json.RawMessage(`{"type":"object"}`),
		})
	}()
	req := rec.waitRequested(t)
	if req.Mode != "url" || req.URL != "https://example.test/confirm" || len(req.RequestedSchema) != 0 {
		t.Fatalf("requested = %#v", req)
	}
	svc.Reply(protocol.ElicitationReply{RequestID: req.RequestID, Action: "accept", Content: json.RawMessage(`{"x":1}`)})
	got := <-out
	if got.Action != "accept" || got.Content != nil {
		t.Fatalf("url accept = %#v, want accept with no content", got)
	}
}

func TestAskCancelSettlesOnce(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Response, 1)
	go func() { out <- svc.Ask(ctx, protocol.Correlation{SessionID: "s1"}, Request{Server: "x", Message: "m"}) }()
	req := rec.waitRequested(t)
	cancel()
	select {
	case got := <-out:
		if got.Action != "cancel" {
			t.Fatalf("action = %q, want cancel", got.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not return after cancel")
	}
	// A late reply is ignored: still exactly one Resolved.
	svc.Reply(protocol.ElicitationReply{RequestID: req.RequestID, Action: "accept"})
	res := rec.resolved()
	if len(res) != 1 || res[0].Action != "cancel" {
		t.Fatalf("resolved = %#v", res)
	}
}

func TestRequestIDsAreSessionScoped(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Ask(ctx, protocol.Correlation{SessionID: "child-7"}, Request{Server: "x"})
	req := rec.waitRequested(t)
	if req.RequestID != "child-7:elicit_1" {
		t.Fatalf("request id = %q", req.RequestID)
	}
	// Unknown ids are ignored.
	svc.Reply(protocol.ElicitationReply{RequestID: "other:elicit_1", Action: "accept"})
	if len(rec.resolved()) != 0 {
		t.Fatal("unknown id resolved something")
	}
}
