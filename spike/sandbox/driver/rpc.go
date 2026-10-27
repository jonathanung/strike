package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
)

type rpcResult struct {
	Text       string
	StopReason string
}

// echoTurn speaks one initialize plus one user.input on a virtio-serial
// socket and returns the concatenated text.delta payload.
func echoTurn(ctx context.Context, conn net.Conn, prompt string, onInit func()) (rpcResult, error) {
	r := bufio.NewReader(conn)
	if err := writeRPC(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	}); err != nil {
		return rpcResult{}, err
	}
	if err := waitID(ctx, conn, r, 1); err != nil {
		return rpcResult{}, fmt.Errorf("initialize: %w", err)
	}
	if onInit != nil {
		onInit()
	}
	if err := writeRPC(conn, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "user.input",
		"params":  map[string]any{"text": prompt},
	}); err != nil {
		return rpcResult{}, err
	}
	var text strings.Builder
	var stop string
	sawInput := false
	for {
		msg, err := readRPC(ctx, conn, r)
		if err != nil {
			return rpcResult{}, err
		}
		if idMatch(msg, 2) {
			sawInput = true
			if msg["error"] != nil {
				return rpcResult{}, fmt.Errorf("user.input error: %v", msg["error"])
			}
			continue
		}
		method, _ := msg["method"].(string)
		if method != "event" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		typ, _ := params["type"].(string)
		data, _ := params["data"].(map[string]any)
		switch typ {
		case "text.delta":
			if s, ok := data["text"].(string); ok {
				text.WriteString(s)
			}
		case "turn.completed":
			if s, ok := data["stopReason"].(string); ok {
				stop = s
			}
			if !sawInput {
				return rpcResult{}, fmt.Errorf("turn completed before user.input was accepted")
			}
			return rpcResult{Text: text.String(), StopReason: stop}, nil
		}
	}
}

func echoAccepted(text, prompt string) bool {
	return strings.Contains(text, "You said: "+prompt)
}

func writeRPC(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func readRPC(ctx context.Context, conn net.Conn, r *bufio.Reader) (map[string]any, error) {
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(dl); err != nil {
			return nil, err
		}
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, fmt.Errorf("rpc json: %w", err)
	}
	return msg, nil
}

func waitID(ctx context.Context, conn net.Conn, r *bufio.Reader, id int) error {
	for {
		msg, err := readRPC(ctx, conn, r)
		if err != nil {
			return err
		}
		if !idMatch(msg, id) {
			continue
		}
		if msg["error"] != nil {
			return fmt.Errorf("%v", msg["error"])
		}
		return nil
	}
}

func idMatch(msg map[string]any, id int) bool {
	switch v := msg["id"].(type) {
	case float64:
		return int(v) == id
	default:
		return false
	}
}

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
