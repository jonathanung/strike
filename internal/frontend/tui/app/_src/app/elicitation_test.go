package tui

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

func TestElicitationCancelCmdRepliesCancel(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	cmd := elicitationCancelCmd(ops, "s1:elicit_1")
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	cmd()
	got, ok := (<-ops).(protocol.ElicitationReply)
	if !ok || got.RequestID != "s1:elicit_1" || got.Action != protocol.ElicitationCancel {
		t.Fatalf("reply = %#v", got)
	}
	if elicitationCancelCmd(nil, "x") != nil || elicitationCancelCmd(ops, "") != nil {
		t.Fatal("expected nil cmd without ops or id")
	}
}

func TestElicitationCancelNoticeNamesServer(t *testing.T) {
	if got := elicitationCancelNotice("billing"); !strings.HasPrefix(got, "billing asked for input") {
		t.Fatalf("notice = %q", got)
	}
	if got := elicitationCancelNotice(" "); !strings.HasPrefix(got, "An MCP server") {
		t.Fatalf("notice = %q", got)
	}
}
