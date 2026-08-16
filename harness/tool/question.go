package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	questionMinCount   = 1
	questionMaxCount   = 4
	questionMinOptions = 2
	questionMaxOptions = 4
)

type questionTool struct{}

func NewQuestion() Tool { return questionTool{} }

func (questionTool) Name() string { return "question" }

func (questionTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencyUnsafe)
}

func (questionTool) Description() string {
	return `Ask the user one or more clarifying questions during execution.

Use this when you need preferences, decisions, or disambiguation before continuing.

Usage notes:
  - Provide 1-4 questions per call.
  - Each question may have no options (free-form answer) or 2-4 labeled options.
  - Prefer short option labels; put detail in the option description.
  - If you recommend a choice, list it first and mark the label with "(Recommended)".`
}

func (questionTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"questions": {
				"type": "array",
				"description": "Questions to ask the user (1-4)",
				"minItems": 1,
				"maxItems": 4,
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string", "description": "Stable identifier for this question"},
						"header": {"type": "string", "description": "Very short label for the question"},
						"question": {"type": "string", "description": "Full question text"},
						"options": {
							"type": "array",
							"description": "Optional choices (omit or empty for free-form; otherwise 2-4)",
							"items": {
								"type": "object",
								"properties": {
									"label": {"type": "string", "description": "Display text for the choice"},
									"description": {"type": "string", "description": "Explanation of the choice"}
								},
								"required": ["label"]
							}
						}
					},
					"required": ["question"]
				}
			}
		},
		"required": ["questions"]
	}`)
}

type questionOptionArgs struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type questionItemArgs struct {
	ID       string               `json:"id"`
	Header   string               `json:"header"`
	Question string               `json:"question"`
	Options  []questionOptionArgs `json:"options"`
}

type questionArgs struct {
	Questions []questionItemArgs `json:"questions"`
}

func (questionTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a questionArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	n := len(a.Questions)
	if n < questionMinCount || n > questionMaxCount {
		return Result{}, fmt.Errorf("questions must contain between %d and %d items", questionMinCount, questionMaxCount)
	}

	items := make([]QuestionItem, n)
	for i, q := range a.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return Result{}, fmt.Errorf("questions[%d].question is required", i)
		}
		optN := len(q.Options)
		if optN != 0 && (optN < questionMinOptions || optN > questionMaxOptions) {
			return Result{}, fmt.Errorf("questions[%d].options must be empty or contain %d-%d items", i, questionMinOptions, questionMaxOptions)
		}
		opts := make([]QuestionOption, optN)
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return Result{}, fmt.Errorf("questions[%d].options[%d].label is required", i, j)
			}
			opts[j] = QuestionOption{Label: o.Label, Description: o.Description}
		}
		id := q.ID
		if id == "" {
			id = fmt.Sprintf("q%d", i+1)
		}
		items[i] = QuestionItem{
			ID:       id,
			Header:   q.Header,
			Question: q.Question,
			Options:  opts,
		}
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "question",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.AskUser == nil {
		return Result{}, fmt.Errorf("question: AskUser is not configured")
	}

	resp, err := tc.AskUser(ctx, QuestionRequest{Questions: items})
	if err != nil {
		return Result{}, err
	}

	parts := make([]string, 0, len(items))
	for i, q := range items {
		answer := "Unanswered"
		if i < len(resp.Answers) && resp.Answers[i] != "" {
			answer = resp.Answers[i]
		}
		parts = append(parts, fmt.Sprintf("%q=%q", q.Question, answer))
	}
	formatted := strings.Join(parts, ", ")
	title := "Asked 1 question"
	if n > 1 {
		title = fmt.Sprintf("Asked %d questions", n)
	}
	meta, _ := json.Marshal(map[string]any{"answers": resp.Answers})
	return Result{
		Title:    title,
		Output:   fmt.Sprintf("User has answered your questions: %s. You can now continue with the user's answers in mind.", formatted),
		Metadata: meta,
	}, nil
}
