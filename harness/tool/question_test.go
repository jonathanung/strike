package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestQuestionValidationCount(t *testing.T) {
	q := NewQuestion()
	tc := allowAll(t.TempDir())
	tc.AskUser = func(context.Context, QuestionRequest) (QuestionResponse, error) {
		t.Fatal("AskUser should not run on invalid count")
		return QuestionResponse{}, nil
	}

	cases := []struct {
		name string
		n    int
	}{
		{"zero", 0},
		{"five", 5},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			questions := make([]map[string]any, tt.n)
			for i := range questions {
				questions[i] = map[string]any{"question": "q?"}
			}
			_, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
				"questions": questions,
			}), tc)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "questions") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestQuestionValidationOptionsOneInvalid(t *testing.T) {
	q := NewQuestion()
	tc := allowAll(t.TempDir())
	tc.AskUser = func(context.Context, QuestionRequest) (QuestionResponse, error) {
		t.Fatal("AskUser should not run")
		return QuestionResponse{}, nil
	}
	_, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "pick?",
			"options":  []map[string]any{{"label": "only-one"}},
		}},
	}), tc)
	if err == nil {
		t.Fatal("expected error for single option")
	}
	if !strings.Contains(err.Error(), "options") {
		t.Errorf("err = %v", err)
	}
}

func TestQuestionFreeformSuccess(t *testing.T) {
	q := NewQuestion()
	tc := allowAll(t.TempDir())
	var saw QuestionRequest
	tc.AskUser = func(_ context.Context, req QuestionRequest) (QuestionResponse, error) {
		saw = req
		return QuestionResponse{Answers: []string{"typed answer"}}, nil
	}
	res, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"id":       "pref",
			"header":   "Pref",
			"question": "What do you prefer?",
		}},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(saw.Questions) != 1 {
		t.Fatalf("AskUser questions = %d", len(saw.Questions))
	}
	if saw.Questions[0].ID != "pref" || saw.Questions[0].Question != "What do you prefer?" {
		t.Errorf("request = %#v", saw.Questions[0])
	}
	if len(saw.Questions[0].Options) != 0 {
		t.Errorf("expected freeform (no options), got %#v", saw.Questions[0].Options)
	}
	if !strings.Contains(res.Output, "typed answer") {
		t.Errorf("output = %q", res.Output)
	}
	if res.Title != "Asked 1 question" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestQuestionMultiSuccess(t *testing.T) {
	q := NewQuestion()
	tc := allowAll(t.TempDir())
	var saw QuestionRequest
	tc.AskUser = func(_ context.Context, req QuestionRequest) (QuestionResponse, error) {
		saw = req
		return QuestionResponse{Answers: []string{"Go", "tests"}}, nil
	}
	res, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{
			{
				"id":       "lang",
				"header":   "Lang",
				"question": "Which language?",
				"options": []map[string]any{
					{"label": "Go", "description": "gopher"},
					{"label": "Rust", "description": "crab"},
				},
			},
			{
				"question": "Any notes?",
			},
		},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(saw.Questions) != 2 {
		t.Fatalf("AskUser questions = %d, want 2", len(saw.Questions))
	}
	if saw.Questions[0].ID != "lang" || saw.Questions[0].Question != "Which language?" {
		t.Errorf("q0 = %#v", saw.Questions[0])
	}
	if len(saw.Questions[0].Options) != 2 || saw.Questions[0].Options[0].Label != "Go" {
		t.Errorf("q0 options = %#v", saw.Questions[0].Options)
	}
	if saw.Questions[1].ID != "q2" { // default id when omitted
		t.Errorf("q1 id = %q, want q2", saw.Questions[1].ID)
	}
	if len(saw.Questions[1].Options) != 0 {
		t.Errorf("q1 should be freeform, got %#v", saw.Questions[1].Options)
	}
	if res.Title != "Asked 2 questions" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "Go") || !strings.Contains(res.Output, "tests") {
		t.Errorf("output missing both answers: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Which language?") || !strings.Contains(res.Output, "Any notes?") {
		t.Errorf("output missing question text: %q", res.Output)
	}
}

func TestQuestionNilAskUser(t *testing.T) {
	q := NewQuestion()
	tc := allowAll(t.TempDir())
	// AskUser left nil
	_, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{"question": "hi?"}},
	}), tc)
	if err == nil {
		t.Fatal("expected error when AskUser is nil")
	}
	if !strings.Contains(err.Error(), "AskUser") {
		t.Errorf("err = %v", err)
	}
}

func TestQuestionPermissionDenied(t *testing.T) {
	q := NewQuestion()
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		AskUser: func(context.Context, QuestionRequest) (QuestionResponse, error) {
			t.Fatal("AskUser must not run after deny")
			return QuestionResponse{}, nil
		},
	}
	_, err := q.Execute(context.Background(), mustJSON(t, map[string]any{
		"questions": []map[string]any{{"question": "hi?"}},
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("err = %v", err)
	}
}
