package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestSchedulerQueueEventsProjectRootAndChild(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.turnRunning = true

	// Root queues on model.
	_ = m.applyEvent(protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "root"},
		RequestID:   "r1",
		Pools:       []string{"model"},
		Label:       "model",
	})
	if len(m.queuePools) != 1 || m.queuePools[0] != "model" || m.queueLabel != "model" {
		t.Fatalf("root queue = pools=%v label=%q", m.queuePools, m.queueLabel)
	}
	if m.agentState() != theme.AgentStateWorking {
		t.Fatalf("agentState=%v want working while queued", m.agentState())
	}

	// Child queues on process.
	m.children = []childActivity{{sessionID: "child-1", status: "running", agent: "explore"}}
	_ = m.applyEvent(protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "root", Depth: 1},
		RequestID:   "r2",
		Pools:       []string{"process"},
		Label:       "bash",
	})
	if len(m.children[0].queuePools) != 1 || m.children[0].queueLabel != "bash" {
		t.Fatalf("child queue = %+v", m.children[0])
	}

	// Admitted clears matching request only.
	_ = m.applyEvent(protocol.SchedulerAdmitted{
		Correlation: protocol.Correlation{SessionID: "root"},
		RequestID:   "r1",
		Pools:       []string{"model"},
		Label:       "model",
		WaitMs:      12,
	})
	if len(m.queuePools) != 0 || m.queueLabel != "" {
		t.Fatalf("root still queued: pools=%v label=%q", m.queuePools, m.queueLabel)
	}
	if len(m.children[0].queuePools) == 0 {
		t.Fatal("child queue cleared by root admit")
	}

	_ = m.applyEvent(protocol.SchedulerCanceled{
		Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "root", Depth: 1},
		RequestID:   "r2",
		Pools:       []string{"process"},
		Label:       "bash",
		Reason:      protocol.SchedulerReasonCanceled,
	})
	if len(m.children[0].queuePools) != 0 {
		t.Fatalf("child queue after cancel: %+v", m.children[0])
	}
}

func TestSchedulerQueueCancelCannotReviveAdmitted(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "s"
	_ = m.applyEvent(protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "s"},
		RequestID:   "x",
		Pools:       []string{"model"},
		Label:       "model",
	})
	_ = m.applyEvent(protocol.SchedulerCanceled{
		Correlation: protocol.Correlation{SessionID: "s"},
		RequestID:   "x",
		Pools:       []string{"model"},
		Label:       "model",
		Reason:      protocol.SchedulerReasonCanceled,
	})
	if len(m.queuePools) != 0 {
		t.Fatalf("queue after cancel: %v", m.queuePools)
	}
	// Stale admitted for same request must not re-queue.
	_ = m.applyEvent(protocol.SchedulerAdmitted{
		Correlation: protocol.Correlation{SessionID: "s"},
		RequestID:   "x",
		Pools:       []string{"model"},
		Label:       "model",
	})
	if len(m.queuePools) != 0 {
		t.Fatalf("stale admitted re-set queue: %v", m.queuePools)
	}
}

func TestSchedulerQueueReplayTransitions(t *testing.T) {
	// Pure projection: apply JSONL-ordered events and reconstruct states.
	type step struct {
		ev   protocol.Event
		want string // "" | "queued:model" | "clear"
	}
	steps := []step{
		{protocol.SchedulerQueued{Correlation: protocol.Correlation{SessionID: "s"}, RequestID: "a", Pools: []string{"model"}, Label: "model"}, "queued:model"},
		{protocol.SchedulerAdmitted{Correlation: protocol.Correlation{SessionID: "s"}, RequestID: "a", Pools: []string{"model"}, Label: "model", WaitMs: 5}, "clear"},
		{protocol.SchedulerQueued{Correlation: protocol.Correlation{SessionID: "s"}, RequestID: "b", Pools: []string{"process"}, Label: "bash"}, "queued:bash"},
		{protocol.SchedulerCanceled{Correlation: protocol.Correlation{SessionID: "s"}, RequestID: "b", Pools: []string{"process"}, Label: "bash", Reason: protocol.SchedulerReasonCanceled}, "clear"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "s"
	for i, st := range steps {
		_ = m.applyEvent(st.ev)
		got := ""
		if len(m.queuePools) > 0 {
			got = "queued:" + m.queueLabel
		} else {
			got = "clear"
		}
		if got != st.want {
			t.Fatalf("step %d: got %q want %q", i, got, st.want)
		}
	}
}

func TestActivityAndAgentsShowQueuePool(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.queueLabel = "model"
	m.queuePools = []string{"model"}
	m.children = []childActivity{{
		sessionID:  "c1",
		status:     "running",
		agent:      "explore",
		queuePools: []string{"process", "build"},
		queueLabel: "bash:build",
	}}

	entries := m.activityEntries()
	foundRoot, foundChild := false, false
	for _, e := range entries {
		if e.Kind == activityQueue && strings.Contains(e.Status, "queued") {
			foundRoot = true
		}
		if e.Kind == activityChild && strings.Contains(e.Status, "queued") && strings.Contains(e.Status, "bash") {
			foundChild = true
		}
	}
	// Flat feed may omit named children when tree mode is on; force no tree.
	m.children = []childActivity{{
		sessionID:  "child", // ephemeral id → flat feed
		status:     "running",
		agent:      "explore",
		queuePools: []string{"process"},
		queueLabel: "bash",
	}}
	entries = m.activityEntries()
	for _, e := range entries {
		if e.Kind == activityQueue {
			foundRoot = true
		}
		if e.Kind == activityChild && strings.Contains(e.Status, "queued") {
			foundChild = true
		}
	}
	if !foundRoot {
		t.Fatalf("activity missing root queue row: %+v", entries)
	}
	if !foundChild {
		t.Fatalf("activity missing child queue status: %+v", entries)
	}

	// Agents tree detail.
	detail := childQueueDetail(childActivity{queueLabel: "bash:build", queuePools: []string{"process", "build"}})
	if detail != "queued: bash:build" {
		t.Fatalf("childQueueDetail=%q", detail)
	}
	if queueDetailLabel("model") != "queued: model" {
		t.Fatalf("queueDetailLabel model")
	}

	// Constrained layout: activity pane body includes queue chip.
	m.width, m.height = 80, 24
	body := ansi.Strip(m.activityPaneBody(40, 12))
	if !strings.Contains(body, "queued") && !strings.Contains(body, "model") && !strings.Contains(body, "bash") {
		t.Fatalf("activity body missing queue: %q", body)
	}
}

func TestMultiRootQueueCorrelationInPanes(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.roots = map[string]*rootPane{
		"root-b": {sessionID: "root-b", toolByID: map[string]*toolCell{}},
	}
	applyEventToPane(m.roots["root-b"], protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "root-b"},
		RequestID:   "rb",
		Pools:       []string{"model"},
		Label:       "model",
	})
	_ = m.applyEvent(protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "root-a"},
		RequestID:   "ra",
		Pools:       []string{"process"},
		Label:       "bash",
	})
	if m.queueLabel != "bash" {
		t.Fatalf("active root label=%q", m.queueLabel)
	}
	if m.roots["root-b"].queueLabel != "model" {
		t.Fatalf("background root label=%q", m.roots["root-b"].queueLabel)
	}
	if m.rootQueueLabel("root-b") != "model" || m.rootQueueLabel("root-a") != "bash" {
		t.Fatalf("rootQueueLabel a=%q b=%q", m.rootQueueLabel("root-a"), m.rootQueueLabel("root-b"))
	}
}
