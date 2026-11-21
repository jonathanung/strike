package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/product/config"
)

// TestWorkflowDraftSavePreservesPhaseGates guards #1857: saving a draft must
// keep each phase's exit gate on disk and in the catalog. The catalog sync
// used to decode canonical JSON (exit: {type, command}) into the host
// document shape (gate / gateCommand), lose every gate, and force-rewrite the
// file with all gates reset to agent.
func TestWorkflowDraftSavePreservesPhaseGates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir := t.TempDir()
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services: &host.Services{
			Workflows:      local.NewWorkflowsWithOpts(nil, nil, local.WorkflowsOpts{WorkDir: workDir}),
			WorkflowDrafts: local.NewWorkflowDrafts(workDir),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	draft := `{
  "schemaVersion": 1,
  "name": "gated",
  "phases": [
    {"name": "approve", "exit": {"type": "user"}},
    {"name": "verify", "exit": {"type": "check", "command": "make test"}},
    {"name": "wrap", "exit": {"type": "agent"}}
  ]
}`
	body, err := json.Marshal(map[string]any{"scope": "project", "confirm": true, "json": draft})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/workflow-drafts/save", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("draft save = %d %s", res.Code, res.Body.String())
	}
	var saved host.WorkflowDraftSave
	if err := json.Unmarshal(res.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode save: %v (%s)", err, res.Body.String())
	}
	if saved.Path == "" {
		t.Fatalf("save returned no path: %s", res.Body.String())
	}

	want := []struct {
		name, gate, command string
	}{
		{"approve", "user", ""},
		{"verify", "check", "make test"},
		{"wrap", "agent", ""},
	}

	onDisk, err := config.LoadWorkflowFile(saved.Path)
	if err != nil {
		t.Fatalf("load saved file: %v", err)
	}
	if len(onDisk.Phases) != len(want) {
		t.Fatalf("disk phases = %d, want %d", len(onDisk.Phases), len(want))
	}
	for i, w := range want {
		p := onDisk.Phases[i]
		if p.Name != w.name || string(p.Exit.Type) != w.gate || p.Exit.Command != w.command {
			t.Errorf("disk phase %d = {%s %s %q}, want {%s %s %q}", i, p.Name, p.Exit.Type, p.Exit.Command, w.name, w.gate, w.command)
		}
	}

	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/workflows/gated", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET workflow = %d %s", get.Code, get.Body.String())
	}
	var sum host.WorkflowSummary
	if err := json.Unmarshal(get.Body.Bytes(), &sum); err != nil {
		t.Fatalf("decode summary: %v (%s)", err, get.Body.String())
	}
	if len(sum.Phases) != len(want) {
		t.Fatalf("catalog phases = %d, want %d (%s)", len(sum.Phases), len(want), get.Body.String())
	}
	for i, w := range want {
		p := sum.Phases[i]
		if p.Name != w.name || p.Gate != w.gate || p.GateCommand != w.command {
			t.Errorf("catalog phase %d = {%s %s %q}, want {%s %s %q}", i, p.Name, p.Gate, p.GateCommand, w.name, w.gate, w.command)
		}
	}
}

func TestCanonicalWorkflowToDocumentMapsExitGate(t *testing.T) {
	tests := []struct {
		name        string
		exit        config.ExitGate
		wantGate    string
		wantCommand string
	}{
		{"user", config.ExitGate{Type: config.GateUser}, "user", ""},
		{"check", config.ExitGate{Type: config.GateCheck, Command: "go test ./..."}, "check", "go test ./..."},
		{"agent", config.ExitGate{Type: config.GateAgent}, "agent", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := canonicalWorkflowToDocument(config.Workflow{
				Name:   "w",
				Phases: []config.Phase{{Name: "p", Exit: tt.exit}},
			})
			if len(doc.Phases) != 1 {
				t.Fatalf("phases = %d", len(doc.Phases))
			}
			if got := doc.Phases[0]; got.Gate != tt.wantGate || got.GateCommand != tt.wantCommand {
				t.Errorf("gate = %q %q, want %q %q", got.Gate, got.GateCommand, tt.wantGate, tt.wantCommand)
			}
		})
	}
}
