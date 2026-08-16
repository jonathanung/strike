package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestSoftBudgetAllowsFinalization(t *testing.T) {
	for _, kind := range []string{
		"wall_clock", "tokens", "cost_usd", "tool_calls", "dangerous_tools", "stall", "loop",
	} {
		if !softBudgetAllowsFinalization(kind) {
			t.Fatalf("kind %q should allow finalization", kind)
		}
	}
	for _, kind := range []string{"", "session_cost", "trust", "cancel"} {
		if softBudgetAllowsFinalization(kind) {
			t.Fatalf("kind %q must not allow finalization", kind)
		}
	}
}

func TestClassifyHandoffQuality(t *testing.T) {
	t.Run("complete structured", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:  "reviewed auth",
			Findings: []string{"csrf gap"},
		}
		if q := classifyHandoffQuality(h, true); q != protocol.HandoffQualityComplete {
			t.Fatalf("got %q", q)
		}
	})
	t.Run("partial from files only", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:      "task failed",
			FilesChanged: []string{"a.go"},
			Incomplete:   true,
		}
		if q := classifyHandoffQuality(h, false); q != protocol.HandoffQualityPartial {
			t.Fatalf("got %q", q)
		}
	})
	t.Run("partial from findings without parse", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:    "tool-call budget exhausted (26/25)",
			Findings:   []string{"note from earlier prose"},
			Incomplete: true,
		}
		if q := classifyHandoffQuality(h, false); q != protocol.HandoffQualityPartial {
			t.Fatalf("got %q", q)
		}
	})
	t.Run("unavailable generic failure", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:    "tool-call budget exhausted (26/25)",
			Blockers:   []string{"tool-call budget exhausted (26/25)"},
			Incomplete: true,
		}
		if q := classifyHandoffQuality(h, false); q != protocol.HandoffQualityUnavailable {
			t.Fatalf("got %q", q)
		}
	})
	t.Run("parsed incomplete with substance is partial", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:    "stopped early",
			Findings:   []string{"x"},
			Incomplete: true,
		}
		if q := classifyHandoffQuality(h, true); q != protocol.HandoffQualityPartial {
			t.Fatalf("got %q", q)
		}
	})
}

func TestBudgetFinalizationPrompt(t *testing.T) {
	p := budgetFinalizationPrompt("tool_calls", "tool-call budget exhausted (3/2)")
	if !strings.Contains(p, "Budget finalization") {
		t.Fatalf("missing header: %s", p)
	}
	if !strings.Contains(p, "tool_calls") {
		t.Fatalf("missing kind: %s", p)
	}
	if !strings.Contains(p, "Do **not** call any tools") {
		t.Fatalf("missing no-tools: %s", p)
	}
	if !strings.Contains(p, `"findings"`) {
		t.Fatalf("missing schema: %s", p)
	}
}

func TestMergeArtifactRefsIntoHandoff(t *testing.T) {
	h := protocol.CompletionHandoff{
		ArtifactRefs: []protocol.ArtifactRef{{ID: "a1", Version: 1}},
	}
	mergeArtifactRefsIntoHandoff(&h, []protocol.ArtifactRef{
		{ID: "a1", Version: 2, Type: "findings"},
		{ID: "a2", Version: 1, Type: "patch"},
		{ID: "", Version: 1},
	})
	if len(h.ArtifactRefs) != 2 {
		t.Fatalf("refs=%#v", h.ArtifactRefs)
	}
	// Existing id not duplicated; new id appended.
	if h.ArtifactRefs[0].ID != "a1" || h.ArtifactRefs[1].ID != "a2" {
		t.Fatalf("order/ids=%#v", h.ArtifactRefs)
	}
}

func TestBuildCompletionHandoffParsedQuality(t *testing.T) {
	raw := `{"summary":"partial review","findings":["csrf"],"files_changed":[],"blockers":["budget"],"incomplete":true}`
	h, ok := buildCompletionHandoffParsed(protocol.ChildStatusFailed, raw, []string{"tracked.go"})
	if !ok {
		t.Fatal("expected parse")
	}
	if !h.Incomplete {
		t.Fatal("model incomplete flag lost")
	}
	if h.Quality != protocol.HandoffQualityPartial {
		t.Fatalf("quality=%q", h.Quality)
	}
	if len(h.FilesChanged) != 1 || h.FilesChanged[0] != "tracked.go" {
		t.Fatalf("files=%v", h.FilesChanged)
	}
	if len(h.Findings) != 1 || h.Findings[0] != "csrf" {
		t.Fatalf("findings=%v", h.Findings)
	}
}
