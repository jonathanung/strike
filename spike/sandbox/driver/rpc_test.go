package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestEchoTurn(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeStrike(server)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := echoTurn(ctx, client, echoPrompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !echoAccepted(got.Text, echoPrompt) {
		t.Fatalf("text %q", got.Text)
	}
	if got.StopReason != "end_turn" {
		t.Fatalf("stop %q", got.StopReason)
	}
	<-done
}

func TestEchoAccepted(t *testing.T) {
	if echoAccepted("nope", echoPrompt) {
		t.Fatal("accepted garbage")
	}
	if !echoAccepted("You said: phase0 hello\n\nTip: start a message", echoPrompt) {
		t.Fatal("rejected echo text")
	}
}

func fakeStrike(conn net.Conn) {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			return
		}
		id, _ := msg["id"].(float64)
		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			_ = writeRPC(conn, map[string]any{
				"jsonrpc": "2.0",
				"method":  "rpc.ready",
				"params":  map[string]any{"sessionId": "phase0"},
			})
			_ = writeRPC(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"ok": true},
			})
		case "user.input":
			params, _ := msg["params"].(map[string]any)
			text, _ := params["text"].(string)
			_ = writeRPC(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"ok": true},
			})
			_ = writeRPC(conn, eventMsg("text.delta", map[string]any{"text": "You said: " + text}))
			_ = writeRPC(conn, eventMsg("turn.completed", map[string]any{"stopReason": "end_turn"}))
			return
		}
	}
}

func eventMsg(typ string, data map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "event",
		"params": map[string]any{
			"type": typ,
			"v":    "1.18.0",
			"data": data,
		},
	}
}
