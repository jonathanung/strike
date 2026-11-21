package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

type sharpenModel struct {
	got    SideChatPrompt
	out    SideChatCompletion
	err    error
	called bool
}

func (m *sharpenModel) Answer(_ context.Context, in SideChatPrompt) (SideChatCompletion, error) {
	m.called = true
	m.got = in
	if m.err != nil {
		return SideChatCompletion{}, m.err
	}
	return m.out, nil
}

// listFiles returns every file under dir, relative, so a test can prove a
// call wrote nothing (no session JSONL, no side-chat sidecar).
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPromptSharpenRewritesWithoutTranscriptWrites(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-sharpen", "/work/repo", nil, ops)
	defer live.Close()
	model := &sharpenModel{out: SideChatCompletion{
		Text:         "  Fix the flaky TestRetry in harness/engine; report the root cause.  ",
		Provider:     "anthropic",
		Model:        "claude-x",
		InputTokens:  120,
		OutputTokens: 30,
	}}
	dir := t.TempDir()
	srv, err := New(Options{SessionDir: dir, Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}
	before := listFiles(t, dir)

	res := postSide(t, srv, "/v1/prompt/sharpen", `{"text":"  fix the flaky thing  "}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	var got sharpenResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Original != "fix the flaky thing" {
		t.Fatalf("original = %q", got.Original)
	}
	if got.Sharpened != "Fix the flaky TestRetry in harness/engine; report the root cause." {
		t.Fatalf("sharpened = %q", got.Sharpened)
	}
	if got.Provider != "anthropic" || got.Model != "claude-x" || got.InputTokens != 120 || got.OutputTokens != 30 {
		t.Fatalf("response = %#v", got)
	}
	if model.got.System != sharpenSystem {
		t.Fatalf("system = %q", model.got.System)
	}
	if model.got.Transcript != "" || len(model.got.History) != 0 {
		t.Fatalf("sharpen must not send transcript or history: %#v", model.got)
	}
	if !strings.Contains(model.got.Question, "fix the flaky thing") || !strings.Contains(model.got.Question, "Workspace: /work/repo") {
		t.Fatalf("question = %q", model.got.Question)
	}
	select {
	case op := <-ops:
		t.Fatalf("sharpen submitted %T", op)
	default:
	}
	after := listFiles(t, dir)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("sharpen wrote files: before %v after %v", before, after)
	}
}

func TestPromptSharpenErrors(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		model    *sharpenModel
		nilModel bool
		want     int
		wantText string
		called   bool
	}{
		{name: "empty draft", body: `{"text":"   "}`, model: &sharpenModel{}, want: http.StatusBadRequest, wantText: "draft is empty"},
		{name: "unknown field", body: `{"text":"x","type":"user.input"}`, model: &sharpenModel{}, want: http.StatusBadRequest, wantText: "invalid sharpen request"},
		{name: "too long", body: `{"text":"` + strings.Repeat("a", sharpenMaxRunes+1) + `"}`, model: &sharpenModel{}, want: http.StatusRequestEntityTooLarge, wantText: "too long"},
		{name: "no model", body: `{"text":"x"}`, nilModel: true, want: http.StatusServiceUnavailable, wantText: "not configured"},
		{name: "model error", body: `{"text":"x"}`, model: &sharpenModel{err: errors.New("upstream 500")}, want: http.StatusBadGateway, wantText: "upstream 500", called: true},
		{name: "timeout", body: `{"text":"x"}`, model: &sharpenModel{err: context.DeadlineExceeded}, want: http.StatusGatewayTimeout, wantText: "sharpen failed", called: true},
		{name: "empty answer", body: `{"text":"x"}`, model: &sharpenModel{out: SideChatCompletion{Text: "  \n"}}, want: http.StatusBadGateway, wantText: "no text", called: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			live := NewLive("sess-sharpen-err", t.TempDir(), nil, make(chan protocol.Op, 1))
			defer live.Close()
			opts := Options{SessionDir: t.TempDir(), Live: live}
			if !tc.nilModel {
				opts.SideChat = tc.model
			}
			srv, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			res := postSide(t, srv, "/v1/prompt/sharpen", tc.body)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", res.Code, tc.want, res.Body.String())
			}
			if !strings.Contains(res.Body.String(), tc.wantText) {
				t.Fatalf("body = %s, want %q", res.Body.String(), tc.wantText)
			}
			if tc.model != nil && tc.model.called != tc.called {
				t.Fatalf("model called = %v, want %v", tc.model.called, tc.called)
			}
		})
	}
}

func TestPromptSharpenNoLiveSession(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir(), SideChat: &sharpenModel{}})
	if err != nil {
		t.Fatal(err)
	}
	res := postSide(t, srv, "/v1/prompt/sharpen", `{"text":"x"}`)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
}

func TestSharpenQuestionWrapsDraft(t *testing.T) {
	cases := []struct {
		draft, cwd string
		want       string
	}{
		{draft: "run tests", cwd: "", want: "Draft prompt to sharpen:\n\nrun tests"},
		{draft: "do x", cwd: " /repo ", want: "Workspace: /repo\n\nDraft prompt to sharpen:\n\ndo x"},
	}
	for _, tc := range cases {
		if got := sharpenQuestion(tc.draft, tc.cwd); got != tc.want {
			t.Fatalf("sharpenQuestion(%q, %q) = %q, want %q", tc.draft, tc.cwd, got, tc.want)
		}
	}
}
