package permission

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestExplainLastMatchWins(t *testing.T) {
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
		{Name: LayerConfig, Rules: Ruleset{
			{Permission: "bash", Pattern: "git *", Action: Allow},
			{Permission: "bash", Pattern: "git push *", Action: Deny},
		}},
	}
	ex := ExplainLabeled("bash", "git push origin main", layers)
	if ex.Action != Deny {
		t.Fatalf("action = %s, want deny", ex.Action)
	}
	if ex.Matched == nil || ex.Matched.Layer != LayerConfig {
		t.Fatalf("matched = %+v", ex.Matched)
	}
	if ex.Matched.Pattern != "git push *" {
		t.Fatalf("matched pattern = %q", ex.Matched.Pattern)
	}
	if len(ex.Trail) < 2 {
		t.Fatalf("trail = %+v, want multiple matches", ex.Trail)
	}
}

func TestExplainDefaultAsk(t *testing.T) {
	ex := Explain("unknown_tool", "x")
	if ex.Action != Ask {
		t.Fatalf("action = %s, want ask", ex.Action)
	}
	if ex.Matched == nil || ex.Matched.Layer != LayerDefaultAction {
		t.Fatalf("matched = %+v", ex.Matched)
	}
}

func TestServiceExplainIncludesModeUpgrade(t *testing.T) {
	svc := New(nil, Defaults())
	svc.SetPermissionMode(protocol.PermissionModeYolo)
	ex := svc.Explain("bash", "rm -rf /tmp/x")
	if ex.Action != Allow {
		t.Fatalf("yolo bash = %s, want allow", ex.Action)
	}
	if !ex.ModeApplied {
		t.Fatal("expected modeApplied")
	}
	if ex.Matched == nil || ex.Matched.Layer != LayerModeUpgrade {
		t.Fatalf("matched = %+v", ex.Matched)
	}
}

func TestFormatExplanation(t *testing.T) {
	ex := ExplainLabeled("bash", "git status", []LabeledLayer{
		{Name: LayerConfig, Rules: Ruleset{{Permission: "bash", Pattern: "git *", Action: Allow}}},
	})
	s := FormatExplanation(ex)
	if !strings.Contains(s, "allow") || !strings.Contains(s, "config") {
		t.Fatalf("format = %q", s)
	}
}
