package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestBuildResidueExtractsMarkedDecisionWithSourceIDs(t *testing.T) {
	dropped := []provider.Message{
		{Role: provider.RoleUser, Text: "Please fix auth.\nDECISION: use OAuth PKCE for the CLI login flow"},
		{Role: provider.RoleAssistant, Text: "Understood.\nFACT: token store lives in internal/auth/store.go\nOPEN: should we support device flow too?"},
		{Role: provider.RoleUser, Text: "also note assumption: CI is green on main"},
	}
	// Unmarked assumption line should not be extracted without a marker.
	r := buildCompactionResidue(dropped, 0, protocol.CompactionStrategyTrim, protocol.CompactionReasonManual, "", nil, nil)
	if r == nil {
		t.Fatal("expected residue")
	}
	if r.SchemaVersion != protocol.CompactionResidueSchemaVersion {
		t.Fatalf("schema = %q", r.SchemaVersion)
	}
	if len(r.Decisions) != 1 {
		t.Fatalf("decisions = %#v", r.Decisions)
	}
	d := r.Decisions[0]
	if !strings.Contains(d.Text, "OAuth PKCE") {
		t.Fatalf("decision text = %q", d.Text)
	}
	if !containsStr(d.SourceIDs, "hist:0") {
		t.Fatalf("decision sources = %v, want hist:0", d.SourceIDs)
	}
	if d.Confidence == "" {
		t.Fatal("decision missing confidence")
	}
	if len(r.Facts) != 1 || !strings.Contains(r.Facts[0].Text, "internal/auth/store.go") {
		t.Fatalf("facts = %#v", r.Facts)
	}
	if !containsStr(r.Facts[0].SourceIDs, "hist:1") {
		t.Fatalf("fact sources = %v", r.Facts[0].SourceIDs)
	}
	if len(r.Facts[0].FileRefs) == 0 {
		t.Fatal("expected file ref on fact")
	}
	if len(r.OpenQuestions) != 1 || !strings.Contains(r.OpenQuestions[0].Text, "device flow") {
		t.Fatalf("open = %#v", r.OpenQuestions)
	}
}

func TestBuildResidueKeepsLedgerDecision(t *testing.T) {
	dropped := []provider.Message{
		{Role: provider.RoleUser, Text: "old chatter without markers"},
		{Role: provider.RoleAssistant, Text: "ok"},
	}
	entries := []LedgerEntry{{
		ID:         "led1",
		Kind:       LedgerKindDecision,
		Statement:  "we will use bubbletea v2 charm.land imports only",
		Confidence: "high",
		Status:     LedgerStatusActive,
		EvidenceRefs: []string{
			"msg:turn-3",
		},
		ScopePaths: []string{"internal/tui"},
	}}
	r := buildCompactionResidue(dropped, 0, protocol.CompactionStrategyTrim, protocol.CompactionReasonThreshold, "", nil, entries)
	if r == nil {
		t.Fatal("expected residue from ledger")
	}
	if len(r.Decisions) != 1 {
		t.Fatalf("decisions = %#v", r.Decisions)
	}
	d := r.Decisions[0]
	if d.LedgerID != "led1" {
		t.Fatalf("ledger id = %q", d.LedgerID)
	}
	if !containsStr(d.SourceIDs, "ledger:led1") {
		t.Fatalf("sources = %v", d.SourceIDs)
	}
	if !strings.Contains(d.Text, "bubbletea") {
		t.Fatalf("text = %q", d.Text)
	}
	// Marked decision must not be silently discarded even when chat has no markers.
	skel := RebuildPromptSkeleton(r)
	if !strings.Contains(skel, "bubbletea") {
		t.Fatalf("rebuild missing ledger decision: %q", skel)
	}
}

func TestBuildResidueRecordsPinsAndSummary(t *testing.T) {
	r := buildCompactionResidue(nil, 0, protocol.CompactionStrategySummarize, protocol.CompactionReasonManual,
		"did the thing", []string{protocol.PromptLayerMemory, protocol.PromptLayerLedger}, nil)
	if r == nil {
		t.Fatal("expected residue for pins+summary")
	}
	if r.Summary != "did the thing" {
		t.Fatalf("summary = %q", r.Summary)
	}
	if len(r.PinnedKinds) != 2 {
		t.Fatalf("pins = %v", r.PinnedKinds)
	}
	skel := RebuildPromptSkeleton(r)
	if !strings.Contains(skel, "did the thing") {
		t.Fatalf("skeleton missing summary: %q", skel)
	}
	if !strings.Contains(skel, protocol.PromptLayerMemory) {
		t.Fatalf("skeleton missing pin: %q", skel)
	}
}

func TestResidueCompactMarkerAndRebuildRoundTrip(t *testing.T) {
	dropped := []provider.Message{
		{Role: provider.RoleAssistant, Text: "[decision] ship residue schema v1"},
	}
	r := buildCompactionResidue(dropped, 0, protocol.CompactionStrategyTrim, protocol.CompactionReasonManual, "", nil, nil)
	marker := residueCompactMarker(3, r)
	if !strings.HasPrefix(marker, compactMarkerPrefix) {
		t.Fatalf("marker prefix: %q", marker)
	}
	if !strings.Contains(marker, "ship residue schema v1") {
		t.Fatalf("marker missing decision: %q", marker)
	}
	// Re-extract from marker body (repeated compaction).
	r2 := &protocol.CompactionResidue{SchemaVersion: protocol.CompactionResidueSchemaVersion}
	extractFromResidueMarkerBody(r2, marker, "hist:0")
	if len(r2.Decisions) != 1 {
		t.Fatalf("re-extract decisions = %#v", r2.Decisions)
	}
}

func TestResidueForMarkerOmitsLedgerWhenLayerActive(t *testing.T) {
	r := &protocol.CompactionResidue{
		SchemaVersion: protocol.CompactionResidueSchemaVersion,
		Decisions: []protocol.ResidueItem{
			{ID: "l1", Kind: protocol.ResidueKindDecision, Text: "from ledger", LedgerID: "abc", SourceIDs: []string{"ledger:abc"}},
			{ID: "c1", Kind: protocol.ResidueKindDecision, Text: "from chat", SourceIDs: []string{"hist:0"}},
		},
		Facts: []protocol.ResidueItem{
			{ID: "f1", Kind: protocol.ResidueKindFact, Text: "a fact", SourceIDs: []string{"hist:1"}},
		},
	}
	got := residueForMarker(r, true)
	if got == nil {
		t.Fatal("expected marker residue")
	}
	if len(got.Decisions) != 1 || got.Decisions[0].Text != "from chat" {
		t.Fatalf("decisions = %#v", got.Decisions)
	}
	if len(got.Facts) != 1 {
		t.Fatalf("facts = %#v", got.Facts)
	}
	// Full residue unchanged for event/export.
	if len(r.Decisions) != 2 {
		t.Fatalf("source residue mutated: %#v", r.Decisions)
	}
	// When ledger layer is off, keep ledger rows in the marker.
	keep := residueForMarker(r, false)
	if keep == nil || len(keep.Decisions) != 2 {
		t.Fatalf("ledger inactive marker = %#v", keep)
	}
	// Ledger-only residue → nil marker when layer active (system still has it).
	onlyLed := &protocol.CompactionResidue{
		SchemaVersion: protocol.CompactionResidueSchemaVersion,
		Decisions:     []protocol.ResidueItem{{ID: "l", Text: "only", LedgerID: "x"}},
	}
	if residueForMarker(onlyLed, true) != nil {
		t.Fatal("expected nil marker residue when only ledger items remain")
	}
}

func TestBuildResidueEmptyWithoutContent(t *testing.T) {
	r := buildCompactionResidue([]provider.Message{
		{Role: provider.RoleUser, Text: "hello"},
		{Role: provider.RoleAssistant, Text: "hi there"},
	}, 0, protocol.CompactionStrategyTrim, protocol.CompactionReasonManual, "", nil, nil)
	if r != nil {
		t.Fatalf("expected nil residue, got %#v", r)
	}
}

func TestNormalizeResidueKind(t *testing.T) {
	cases := map[string]string{
		"DECISION":      protocol.ResidueKindDecision,
		"open question": protocol.ResidueKindOpenQuestion,
		"TODO":          protocol.ResidueKindOpenQuestion,
		"assumption":    protocol.ResidueKindAssumption,
		"fact":          protocol.ResidueKindFact,
	}
	for in, want := range cases {
		if got := normalizeResidueKind(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestBuildResidueStaleAssumption(t *testing.T) {
	entries := []LedgerEntry{{
		ID:        "led-stale",
		Kind:      LedgerKindAssumption,
		Statement: "file still says old",
		Status:    LedgerStatusActive,
		Freshness: "stale",
	}}
	r := buildCompactionResidue(nil, 0, protocol.CompactionStrategyTrim, protocol.CompactionReasonManual, "", nil, entries)
	if r == nil || len(r.Decisions) != 1 {
		t.Fatalf("residue = %#v", r)
	}
	if r.Decisions[0].Freshness != "stale" {
		t.Fatalf("freshness = %q", r.Decisions[0].Freshness)
	}
}
