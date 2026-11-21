package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/memory"
)

func TestLessonProposeIsPendingAndRedacted(t *testing.T) {
	store := openMemory(t)
	tc := allowAll(t.TempDir())
	tc.SessionID = "sess-9"
	tl := NewLessonPropose(store, true)
	if tl.Name() != "lesson_propose" || len(tl.Schema()) == 0 || tl.Description() == "" {
		t.Fatalf("tool metadata: %q", tl.Name())
	}
	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	res, err := tl.Execute(context.Background(), mustJSON(t, map[string]any{
		"key": "lesson-test-cmd", "value": "use make test-fast " + secret,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "pending") || strings.Contains(res.Output, secret) {
		t.Fatalf("output = %q", res.Output)
	}
	list, err := store.Proposals()
	if err != nil || len(list) != 1 {
		t.Fatalf("proposals = %+v err=%v", list, err)
	}
	if list[0].Session != "sess-9" || list[0].Scope != "project" || strings.Contains(list[0].Value, secret) {
		t.Fatalf("proposal = %+v", list[0])
	}
	if entries, _ := store.List(""); len(entries) != 0 {
		t.Fatalf("propose stored memory: %+v", entries)
	}
}

func TestLessonProposeRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		trusted bool
		args    map[string]any
		want    string
	}{
		{"incognito", memory.ModeIncognito, true, map[string]any{"key": "k", "value": "v"}, "incognito"},
		{"temporary", memory.ModeTemporary, true, map[string]any{"key": "k", "value": "v"}, "temporary"},
		{"untrusted", memory.ModeNormal, false, map[string]any{"key": "k", "value": "v"}, "untrusted workspace"},
		{"no key", memory.ModeNormal, true, map[string]any{"value": "v"}, "key is required"},
		{"no value", memory.ModeNormal, true, map[string]any{"key": "k"}, "text is required"},
		{"bad scope", memory.ModeNormal, true, map[string]any{"key": "k", "value": "v", "scope": "team"}, "scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openMemory(t)
			ctx := allowAll(t.TempDir())
			ctx.MemoryMode = tc.mode
			_, err := NewLessonPropose(store, tc.trusted).Execute(context.Background(), mustJSON(t, tc.args), ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if list, _ := store.Proposals(); len(list) != 0 {
				t.Fatalf("refused call stored a proposal: %+v", list)
			}
		})
	}
}
