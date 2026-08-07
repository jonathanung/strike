package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

func TestApprovalControllerAllowOnce(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	var got approvalRequest
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := json.NewDecoder(client)
		if err := sc.Decode(&got); err != nil {
			t.Errorf("decode req: %v", err)
			return
		}
		rep := approvalReply{
			Type:      approvalTypePermRep,
			RequestID: got.RequestID,
			Decision:  string(protocol.DecisionOnce),
		}
		enc := json.NewEncoder(client)
		_ = enc.Encode(rep)
	}()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID:  "p1",
		Permission: "bash",
		Patterns:   []string{"echo hi"},
	}, "sess")
	wg.Wait()
	if reply.Decision != protocol.DecisionOnce || reply.RequestID != "p1" {
		t.Fatalf("reply = %+v", reply)
	}
	if got.Type != approvalTypePermReq || got.Permission != "bash" {
		t.Fatalf("req = %+v", got)
	}
}

func TestApprovalControllerRejectWithFeedback(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		var req approvalRequest
		_ = json.NewDecoder(client).Decode(&req)
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: req.RequestID,
			Decision:  string(protocol.DecisionReject),
			Message:   "not this path",
		})
	}()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p2", Permission: "edit", Patterns: []string{"a.go"},
	}, "")
	if reply.Decision != protocol.DecisionReject || reply.Message != "not this path" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestApprovalControllerTimeoutFailClosed(t *testing.T) {
	ctrl, _ := pipeApproval(t)
	defer ctrl.Close()
	ctrl.timeout = 30 * time.Millisecond

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p3", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionReject {
		t.Fatalf("decision = %s", reply.Decision)
	}
	if !strings.Contains(reply.Message, "timeout") && !strings.Contains(reply.Message, "fail closed") {
		t.Fatalf("message = %q", reply.Message)
	}
}

func TestApprovalControllerDisconnectFailClosed(t *testing.T) {
	ctrl, client := pipeApproval(t)
	_ = client.Close() // disconnect before reply

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p4", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionReject {
		t.Fatalf("decision = %s", reply.Decision)
	}
	if !strings.Contains(reply.Message, "fail closed") && !strings.Contains(reply.Message, "disconnect") {
		t.Fatalf("message = %q", reply.Message)
	}
	_ = ctrl.Close()
}

func TestApprovalControllerMalformedReplyFailClosed(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		// Wait for request then send garbage.
		buf := make([]byte, 4096)
		_, _ = client.Read(buf)
		_, _ = io.WriteString(client, "not-json\n")
	}()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p5", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionReject {
		t.Fatalf("decision = %s", reply.Decision)
	}
	if !strings.Contains(reply.Message, "malformed") && !strings.Contains(reply.Message, "fail closed") {
		t.Fatalf("message = %q", reply.Message)
	}
}

func TestApprovalControllerDurableRequiresFlag(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		var req approvalRequest
		_ = json.NewDecoder(client).Decode(&req)
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: req.RequestID,
			Decision:  string(protocol.DecisionAlways),
			// Durable omitted → reject
		})
	}()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p6", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionReject {
		t.Fatalf("decision = %s, want reject without durable", reply.Decision)
	}
	if !strings.Contains(reply.Message, "durable") {
		t.Fatalf("message = %q", reply.Message)
	}
}

func TestApprovalControllerDurableAllow(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		var req approvalRequest
		_ = json.NewDecoder(client).Decode(&req)
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: req.RequestID,
			Decision:  string(protocol.DecisionAlways),
			Durable:   true,
		})
	}()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "p7", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionAlways {
		t.Fatalf("decision = %s", reply.Decision)
	}
}

func TestApprovalControllerSecretRedaction(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	secret := "sk-ant-api03-ABCDEFGHIJKLMNOP"
	var got approvalRequest
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = json.NewDecoder(client).Decode(&got)
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: got.RequestID,
			Decision:  string(protocol.DecisionReject),
		})
	}()

	_ = ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID:  "p8",
		Permission: "bash",
		Patterns:   []string{"export KEY=" + secret},
	}, "")
	wg.Wait()
	joined := strings.Join(got.Patterns, " ")
	if strings.Contains(joined, secret) {
		t.Fatalf("secret leaked in request: %q", joined)
	}
	if !strings.Contains(joined, redact.Placeholder) && joined == "export KEY="+secret {
		t.Fatalf("expected redaction in %q", joined)
	}
}

func TestApprovalControllerQuestion(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		var req approvalRequest
		_ = json.NewDecoder(client).Decode(&req)
		if req.Type != approvalTypeQuestReq {
			t.Errorf("type = %s", req.Type)
		}
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypeQuestRep,
			RequestID: req.RequestID,
			Answers:   []string{"yes"},
		})
	}()

	reply := ctrl.resolveQuestion(context.Background(), protocol.QuestionAsked{
		RequestID: "q1",
		Questions: []protocol.QuestionPrompt{{Question: "continue?"}},
	}, "")
	if len(reply.Answers) != 1 || reply.Answers[0] != "yes" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestHeadlessFrontendUsesApprovalController(t *testing.T) {
	ctrl, client := pipeApproval(t)
	defer ctrl.Close()

	go func() {
		var req approvalRequest
		_ = json.NewDecoder(client).Decode(&req)
		_ = json.NewEncoder(client).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: req.RequestID,
			Decision:  string(protocol.DecisionOnce),
		})
	}()

	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(context.Background(), ops, events, "hi", &stdout, io.Discard, headlessOpts{
			approvals: ctrl,
		})
	}()

	// Drain UserInput
	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("no user input")
	}

	events <- protocol.PermissionAsked{RequestID: "ask-1", Permission: "bash", Patterns: []string{"true"}}
	select {
	case op := <-ops:
		rep, ok := op.(protocol.PermissionReply)
		if !ok || rep.Decision != protocol.DecisionOnce {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("no permission reply")
	}

	events <- protocol.TextDelta{Text: "ok"}
	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	close(events)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestOpenApprovalControllerUnix(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "appr.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req approvalRequest
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(approvalReply{
			Type:      approvalTypePermRep,
			RequestID: req.RequestID,
			Decision:  string(protocol.DecisionReject),
			Message:   "nope",
		})
	}()

	ctrl, err := openApprovalController(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()

	reply := ctrl.resolvePermission(context.Background(), protocol.PermissionAsked{
		RequestID: "u1", Permission: "bash", Patterns: []string{"x"},
	}, "")
	if reply.Decision != protocol.DecisionReject || reply.Message != "nope" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestParseExecArgsApprovalFlags(t *testing.T) {
	parsed, err := parseExecArgs([]string{"--approval-control=/tmp/a.sock", "--approval-timeout=5s", "hi"}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.approvalControl != "/tmp/a.sock" {
		t.Fatalf("control = %q", parsed.approvalControl)
	}
	if parsed.approvalTimeout != 5*time.Second {
		t.Fatalf("timeout = %v", parsed.approvalTimeout)
	}
}

// pipeApproval returns a controller and the peer end (net.Conn) for tests.
func pipeApproval(t *testing.T) (*approvalController, net.Conn) {
	t.Helper()
	c1, c2 := net.Pipe()
	ctrl := newApprovalController(c1, c1, c1.Close, time.Second)
	t.Cleanup(func() {
		_ = ctrl.Close()
		_ = c2.Close()
	})
	return ctrl, c2
}

// Ensure os is referenced when building on platforms that need TempDir only.
var _ = os.TempDir
