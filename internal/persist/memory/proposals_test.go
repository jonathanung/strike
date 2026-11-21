package memory

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProposalLifecycle(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	p, err := s.Propose(Proposal{Key: " test-cmd ", Value: "use make test-fast, token " + secret, Session: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.ID, "prop-") || p.Key != "test-cmd" || p.Scope != "project" || p.Session != "sess-1" || p.CreatedAt.IsZero() {
		t.Fatalf("proposal = %+v", p)
	}
	if strings.Contains(p.Value, secret) {
		t.Fatalf("secret survived in value: %q", p.Value)
	}
	raw, err := os.ReadFile(s.ProposalsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret written to proposals file")
	}
	// A pending proposal is not a memory entry and never auto-loads.
	if entries, _ := s.List(""); len(entries) != 0 {
		t.Fatalf("proposal leaked into entries: %+v", entries)
	}

	// Durable across reopen.
	_ = s.Close()
	s, err = Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Proposals()
	if err != nil || len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("after reopen = %+v err=%v", list, err)
	}

	edited, err := s.EditProposal(p.ID, "test-fast", "run make test-fast "+secret, "global")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Key != "test-fast" || edited.Scope != "global" || edited.ID != p.ID || edited.Session != "sess-1" || strings.Contains(edited.Value, secret) {
		t.Fatalf("edited = %+v", edited)
	}

	taken, err := s.TakeProposal(p.ID)
	if err != nil || taken.Key != "test-fast" {
		t.Fatalf("take = %+v err=%v", taken, err)
	}
	if list, _ := s.Proposals(); len(list) != 0 {
		t.Fatalf("still pending: %+v", list)
	}
	if err := s.RestoreProposal(taken); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Proposals(); len(list) != 1 || list[0].ID != p.ID {
		t.Fatalf("restore = %+v", list)
	}
}

func TestProposalValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cases := []struct {
		name string
		in   Proposal
	}{
		{"empty key", Proposal{Key: " ", Value: "x"}},
		{"empty value", Proposal{Key: "k", Value: "  "}},
		{"bad scope", Proposal{Key: "k", Value: "x", Scope: "team"}},
		{"newline key", Proposal{Key: "a\nb", Value: "x"}},
		{"too long", Proposal{Key: "k", Value: strings.Repeat("x", maxValueLen+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Propose(tc.in); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := s.EditProposal("prop-missing", "k", "v", ""); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("edit missing err=%v", err)
	}
	if _, err := s.TakeProposal("prop-missing"); !errors.Is(err, ErrProposalNotFound) {
		t.Fatalf("take missing err=%v", err)
	}
}

func TestRecordInjectedUnionPerSession(t *testing.T) {
	root := t.TempDir()
	project, err := Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	global, err := Open(root, GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close(); _ = global.Close() })

	if err := project.RecordInjected("s1", []Entry{{Key: "a", Value: "first", Tags: []string{TagLesson}}}); err != nil {
		t.Fatal(err)
	}
	// A repeat keeps the first text; a new key and the global scope add.
	if err := project.RecordInjected("s1", []Entry{{Key: "a", Value: "changed"}, {Key: "b", Value: "second"}}); err != nil {
		t.Fatal(err)
	}
	if err := global.RecordInjected("s1", []Entry{{Key: "a", Value: "global a"}}); err != nil {
		t.Fatal(err)
	}
	if err := project.RecordInjected("", []Entry{{Key: "z", Value: "no session"}}); err != nil {
		t.Fatal(err)
	}
	got, err := global.Injected("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("injected = %+v", got)
	}
	byID := map[string]Injection{}
	for _, e := range got {
		byID[e.Scope+"/"+e.Key] = e
	}
	if byID["project/a"].Value != "first" || byID["project/b"].Value != "second" || byID["global/a"].Value != "global a" {
		t.Fatalf("injected = %+v", byID)
	}
	if got[0].Key != "a" || got[0].Scope != "project" {
		t.Fatalf("first injection should lead: %+v", got)
	}
	if none, err := project.Injected("s2"); err != nil || len(none) != 0 {
		t.Fatalf("unknown session = %+v err=%v", none, err)
	}
}

func TestAutoLoadSelectionMatchesLayer(t *testing.T) {
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Put("lesson-a", "approved lesson", []string{TagLesson}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("note", "on demand", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(Proposal{Key: "pending", Value: "pending text"}); err != nil {
		t.Fatal(err)
	}
	sel, omitted, err := AutoLoadSelection(s)
	if err != nil || omitted != 0 || len(sel) != 1 || sel[0].Key != "lesson-a" {
		t.Fatalf("selection = %+v omitted=%d err=%v", sel, omitted, err)
	}
	text, _, err := AutoLoadLayer(s)
	if err != nil || !strings.Contains(text, "approved lesson") || strings.Contains(text, "pending text") {
		t.Fatalf("layer = %q err=%v", text, err)
	}
}
