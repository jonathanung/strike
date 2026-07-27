package question

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestAskReplyResolvedOrder(t *testing.T) {
	events := make(chan protocol.Event, 4)
	svc := New(func(ev protocol.Event) { events <- ev })
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1", ProviderRequestID: "p1"}
	prompts := []protocol.QuestionPrompt{{ID: "q1", Question: "Pick one?"}}

	errCh := make(chan error, 1)
	ansCh := make(chan []string, 1)
	go func() {
		answers, err := svc.Ask(context.Background(), corr, prompts)
		ansCh <- answers
		errCh <- err
	}()

	var asked protocol.QuestionAsked
	select {
	case ev := <-events:
		var ok bool
		asked, ok = ev.(protocol.QuestionAsked)
		if !ok {
			t.Fatalf("first event = %T, want QuestionAsked", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionAsked")
	}
	if asked.RequestID == "" {
		t.Fatal("empty requestId")
	}
	if asked.Correlation != corr {
		t.Errorf("correlation = %#v, want %#v", asked.Correlation, corr)
	}
	if len(asked.Questions) != 1 || asked.Questions[0].Question != "Pick one?" {
		t.Errorf("questions = %#v", asked.Questions)
	}

	svc.Reply(protocol.QuestionReply{RequestID: asked.RequestID, Answers: []string{"yes"}})

	select {
	case ev := <-events:
		resolved, ok := ev.(protocol.QuestionResolved)
		if !ok {
			t.Fatalf("second event = %T, want QuestionResolved", ev)
		}
		if resolved.RequestID != asked.RequestID {
			t.Errorf("resolved id = %q, want %q", resolved.RequestID, asked.RequestID)
		}
		if resolved.Correlation != corr {
			t.Errorf("resolved correlation = %#v", resolved.Correlation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionResolved")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask return")
	}
	answers := <-ansCh
	if len(answers) != 1 || answers[0] != "yes" {
		t.Errorf("answers = %#v, want [yes]", answers)
	}
}

func TestAskCancelEmitsResolved(t *testing.T) {
	events := make(chan protocol.Event, 4)
	svc := New(func(ev protocol.Event) { events <- ev })
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Ask(ctx, protocol.Correlation{SessionID: "s"}, []protocol.QuestionPrompt{
			{ID: "q", Question: "hang?"},
		})
		errCh <- err
	}()

	select {
	case ev := <-events:
		if _, ok := ev.(protocol.QuestionAsked); !ok {
			t.Fatalf("first = %T, want QuestionAsked", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionAsked")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask cancel")
	}

	select {
	case ev := <-events:
		if _, ok := ev.(protocol.QuestionResolved); !ok {
			t.Fatalf("second = %T, want QuestionResolved", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionResolved on cancel")
	}
}

func TestAskEmptyAnswersRejected(t *testing.T) {
	events := make(chan protocol.Event, 4)
	svc := New(func(ev protocol.Event) { events <- ev })

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Ask(context.Background(), protocol.Correlation{}, []protocol.QuestionPrompt{
			{ID: "q", Question: "dismiss me?"},
		})
		errCh <- err
	}()

	var id string
	select {
	case ev := <-events:
		asked := ev.(protocol.QuestionAsked)
		id = asked.RequestID
	case <-time.After(2 * time.Second):
		t.Fatal("timed out for QuestionAsked")
	}

	svc.Reply(protocol.QuestionReply{RequestID: id, Answers: nil})

	select {
	case err := <-errCh:
		var rej *RejectedError
		if !errors.As(err, &rej) {
			t.Fatalf("err = %v (%T), want RejectedError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out for Ask result")
	}

	// Drain Resolved so it does not leak into other tests if shared.
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("missing QuestionResolved after empty answers")
	}
}

func TestAskAnswerLenMismatch(t *testing.T) {
	events := make(chan protocol.Event, 4)
	svc := New(func(ev protocol.Event) { events <- ev })

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Ask(context.Background(), protocol.Correlation{}, []protocol.QuestionPrompt{
			{ID: "a", Question: "one?"},
			{ID: "b", Question: "two?"},
		})
		errCh <- err
	}()

	var id string
	select {
	case ev := <-events:
		id = ev.(protocol.QuestionAsked).RequestID
	case <-time.After(2 * time.Second):
		t.Fatal("timed out for QuestionAsked")
	}

	svc.Reply(protocol.QuestionReply{RequestID: id, Answers: []string{"only-one"}})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected length mismatch error")
		}
		if !strings.Contains(err.Error(), "1") || !strings.Contains(err.Error(), "2") {
			t.Errorf("err = %v, want mention of got/want counts", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out for Ask result")
	}
}

func TestAskMultiReplySuccess(t *testing.T) {
	events := make(chan protocol.Event, 4)
	svc := New(func(ev protocol.Event) { events <- ev })
	corr := protocol.Correlation{SessionID: "s-multi", TurnID: "t1"}
	prompts := []protocol.QuestionPrompt{
		{ID: "a", Question: "First?", Options: []protocol.QuestionOption{{Label: "1"}, {Label: "2"}}},
		{ID: "b", Header: "Notes", Question: "Second?"},
		{ID: "c", Question: "Third?", Options: []protocol.QuestionOption{{Label: "x"}, {Label: "y"}}},
	}

	errCh := make(chan error, 1)
	ansCh := make(chan []string, 1)
	go func() {
		answers, err := svc.Ask(context.Background(), corr, prompts)
		ansCh <- answers
		errCh <- err
	}()

	var asked protocol.QuestionAsked
	select {
	case ev := <-events:
		var ok bool
		asked, ok = ev.(protocol.QuestionAsked)
		if !ok {
			t.Fatalf("first event = %T, want QuestionAsked", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionAsked")
	}
	if len(asked.Questions) != 3 {
		t.Fatalf("asked questions = %d, want 3", len(asked.Questions))
	}
	for i, p := range prompts {
		if asked.Questions[i].ID != p.ID || asked.Questions[i].Question != p.Question {
			t.Errorf("question[%d] = %#v, want %#v", i, asked.Questions[i], p)
		}
	}

	want := []string{"2", "freeform notes", "x"}
	svc.Reply(protocol.QuestionReply{RequestID: asked.RequestID, Answers: want})

	select {
	case ev := <-events:
		if _, ok := ev.(protocol.QuestionResolved); !ok {
			t.Fatalf("second event = %T, want QuestionResolved", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QuestionResolved")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Ask return")
	}
	got := <-ansCh
	if len(got) != len(want) {
		t.Fatalf("answers = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("answers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConcurrentAsks(t *testing.T) {
	var mu sync.Mutex
	var events []protocol.Event
	svc := New(func(ev protocol.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	const n = 5
	type result struct {
		answers []string
		err     error
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			answers, err := svc.Ask(context.Background(), protocol.Correlation{TurnID: string(rune('a' + i))}, []protocol.QuestionPrompt{
				{ID: "q", Question: "concurrent?"},
			})
			results <- result{answers, err}
		}(i)
	}

	// Wait until all asks are pending.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		var asked []protocol.QuestionAsked
		for _, ev := range events {
			if a, ok := ev.(protocol.QuestionAsked); ok {
				asked = append(asked, a)
			}
		}
		mu.Unlock()
		if len(asked) >= n {
			for i, a := range asked {
				svc.Reply(protocol.QuestionReply{
					RequestID: a.RequestID,
					Answers:   []string{a.RequestID + "-ans"},
				})
				_ = i
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only saw %d asks", len(asked))
		case <-time.After(5 * time.Millisecond):
		}
	}

	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("ask %d: %v", i, r.err)
			}
			if len(r.answers) != 1 || r.answers[0] == "" {
				t.Errorf("ask %d answers = %#v", i, r.answers)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for result %d", i)
		}
	}
}

func TestReplyUnknownRequestIDNoOp(t *testing.T) {
	var called int
	svc := New(func(protocol.Event) { called++ })
	// Must not panic or emit.
	svc.Reply(protocol.QuestionReply{RequestID: "missing", Answers: []string{"x"}})
	if called != 0 {
		t.Errorf("emit called %d times on unknown reply", called)
	}
}
