package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestAssessFreshnessTable(t *testing.T) {
	dir := t.TempDir()
	rel := writeWorkFile(t, dir, "internal/auth/store.go", "package auth\nfunc Login() {}\n")
	pin, err := SnapshotPathPin(dir, rel)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir, "freshness")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	pinned, err := s.Append(AppendInput{
		Kind:          KindAssumption,
		Statement:     "auth uses Login helper",
		EvidencePins:  []EvidencePin{pin},
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	unpinned, err := s.Append(AppendInput{
		Kind:          KindAssumption,
		Statement:     "legacy unpinned assumption",
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := s.Append(AppendInput{
		Kind:          KindDecision,
		Statement:     "keep bubbletea v2",
		EvidencePins:  []EvidencePin{pin},
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	constraint, err := s.Append(AppendInput{
		Kind:          KindConstraint,
		Statement:     "no force push",
		EvidencePins:  []EvidencePin{pin},
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := s.Append(AppendInput{
		Kind:          KindAssumption,
		Statement:     "old helper name",
		EvidencePins:  []EvidencePin{pin},
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := s.Supersede(prior.ID, AppendInput{
		Kind:          KindAssumption,
		Statement:     "new helper name",
		EvidencePins:  []EvidencePin{pin},
		AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unchanged", func(t *testing.T) {
		fr := AssessFreshness(pinned, dir)
		if fr.State != FreshValidated {
			t.Fatalf("got %#v", fr)
		}
	})

	t.Run("changed", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("package auth\nfunc LoginV2() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fr := AssessFreshness(pinned, dir)
		if fr.State != FreshStale || !strings.Contains(fr.Reason, "changed") {
			t.Fatalf("got %#v", fr)
		}
		if len(fr.ChangedEvidence) == 0 || !strings.Contains(fr.ChangedEvidence[0], "hash-mismatch") {
			t.Fatalf("changed evidence = %#v", fr.ChangedEvidence)
		}
		// Restore for later subtests that share dir.
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("package auth\nfunc Login() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(dir, rel)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		fr := AssessFreshness(pinned, dir)
		if fr.State != FreshStale || !strings.Contains(fr.Reason, "missing") {
			t.Fatalf("got %#v", fr)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("legacy_unpinned", func(t *testing.T) {
		fr := AssessFreshness(unpinned, dir)
		if fr.State != FreshUnpinned {
			t.Fatalf("got %#v", fr)
		}
	})

	t.Run("superseded_not_active", func(t *testing.T) {
		got, ok, err := s.Get(prior.ID)
		if err != nil || !ok {
			t.Fatal(err)
		}
		if got.Status != StatusSuperseded {
			t.Fatalf("status = %s", got.Status)
		}
		fr := AssessFreshness(got, dir)
		if fr.State != FreshNotApplicable {
			t.Fatalf("superseded freshness = %#v", fr)
		}
		active, err := s.ActiveSlice("", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range active {
			if e.ID == prior.ID {
				t.Fatalf("superseded entry in active slice: %#v", e)
			}
		}
		if AssessFreshness(replacement, dir).State != FreshValidated {
			t.Fatalf("replacement should stay validated")
		}
	})

	t.Run("decisions_and_constraints_not_autostale", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if fr := AssessFreshness(decision, dir); fr.State != FreshNotApplicable {
			t.Fatalf("decision = %#v", fr)
		}
		if fr := AssessFreshness(constraint, dir); fr.State != FreshNotApplicable {
			t.Fatalf("constraint = %#v", fr)
		}
		if fr := AssessFreshness(pinned, dir); fr.State != FreshStale {
			t.Fatalf("assumption should stale = %#v", fr)
		}
	})
}

func TestLegacyUnpinnedJSONStillLoads(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "legacy-json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Append(AppendInput{
		Kind: KindAssumption, Statement: "before pins existed", AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "evidence_pins") {
		t.Fatalf("legacy row should omit empty pins: %s", raw)
	}
	var doc struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != fileVersion {
		t.Fatalf("version = %d", doc.Version)
	}
	s2, err := Open(root, "legacy-json")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.Get(e.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(got.EvidencePins) != 0 {
		t.Fatalf("pins = %#v", got.EvidencePins)
	}
	if fr := AssessFreshness(got, root); fr.State != FreshUnpinned {
		t.Fatalf("freshness = %#v", fr)
	}
}

func TestAutoLoadOmitsStaleFromValidated(t *testing.T) {
	dir := t.TempDir()
	rel := writeWorkFile(t, dir, "pkg/m/x.go", "package m\n")
	pin, err := SnapshotPathPin(dir, rel)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Append(AppendInput{
		Kind: KindAssumption, Statement: "module M is pinned",
		EvidencePins: []EvidencePin{pin}, AuthorSession: "s",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(AppendInput{
		Kind: KindDecision, Statement: "ship the pin feature", AuthorSession: "s",
	}); err != nil {
		t.Fatal(err)
	}

	text, omitted, err := AutoLoadLayer(s, "", "", dir)
	if err != nil || omitted != 0 {
		t.Fatalf("err=%v omitted=%d", err, omitted)
	}
	if !strings.Contains(text, "module M is pinned") || strings.Contains(text, "Stale assumptions") {
		t.Fatalf("validated layer = %q", text)
	}

	if err := os.WriteFile(filepath.Join(dir, rel), []byte("package m\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleText, _, err := AutoLoadLayer(s, "", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staleText, "Stale assumptions (not currently validated)") {
		t.Fatalf("missing stale section: %q", staleText)
	}
	if !strings.Contains(staleText, "module M is pinned") || !strings.Contains(staleText, "stale:") {
		t.Fatalf("stale entry not described: %q", staleText)
	}
	if !strings.Contains(staleText, "ship the pin feature") {
		t.Fatalf("decision dropped: %q", staleText)
	}
	// Decision must appear before the stale heading so it is not under "not validated".
	if strings.Index(staleText, "ship the pin feature") > strings.Index(staleText, "Stale assumptions") {
		t.Fatalf("decision presented under stale section: %q", staleText)
	}
}

func TestRevalidateRefreshesPins(t *testing.T) {
	dir := t.TempDir()
	rel := writeWorkFile(t, dir, "a.go", "one")
	pin, err := SnapshotPathPin(dir, rel)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e, err := s.Append(AppendInput{
		Kind: KindAssumption, Statement: "file says one",
		EvidencePins: []EvidencePin{pin}, AuthorSession: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fr := AssessFreshness(e, dir); fr.State != FreshStale {
		t.Fatalf("pre = %#v", fr)
	}
	fresh, err := SnapshotPathPin(dir, rel)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Revalidate(e.ID, []EvidencePin{fresh})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive || got.Statement != e.Statement {
		t.Fatalf("history mutated: %#v", got)
	}
	if fr := AssessFreshness(got, dir); fr.State != FreshValidated {
		t.Fatalf("post = %#v", fr)
	}
	// Invalidate still works after revalidate (existing lifecycle).
	if _, err := s.Invalidate(e.ID, InvalidateInput{Reason: "no longer true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revalidate(e.ID, []EvidencePin{fresh}); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("revalidate invalidated: %v", err)
	}
}

func TestPinPathEscape(t *testing.T) {
	dir := t.TempDir()
	fr := AssessFreshness(Entry{
		Kind:   KindAssumption,
		Status: StatusActive,
		EvidencePins: []EvidencePin{{
			Kind: PinKindPath,
			Path: "../outside.go",
			Hash: "sha256:" + strings.Repeat("ab", 32),
		}},
	}, dir)
	if fr.State != FreshStale {
		t.Fatalf("escape = %#v", fr)
	}
}

func TestSymbolPin(t *testing.T) {
	dir := t.TempDir()
	rel := writeWorkFile(t, dir, "pkg/x.go", "package x\nfunc KeepMe() {}\n")
	e := Entry{
		Kind:   KindAssumption,
		Status: StatusActive,
		EvidencePins: []EvidencePin{{
			Kind:   PinKindSymbol,
			Path:   rel,
			Symbol: "KeepMe",
		}},
	}
	if fr := AssessFreshness(e, dir); fr.State != FreshValidated {
		t.Fatalf("symbol present = %#v", fr)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("package x\nfunc Other() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fr := AssessFreshness(e, dir); fr.State != FreshStale || !strings.Contains(strings.Join(fr.ChangedEvidence, " "), "symbol missing") {
		t.Fatalf("symbol gone = %#v", fr)
	}
}
