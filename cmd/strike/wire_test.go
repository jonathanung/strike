package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestRunSessionClosesStoreAfterEngineCleanupAndFinalAppend(t *testing.T) {
	engineEvents := make(chan protocol.Event)
	canceled := make(chan struct{})
	finishCleanup := make(chan struct{})
	engineReturned := make(chan struct{})
	appendStarted := make(chan struct{})
	finishAppend := make(chan struct{})
	appendCompleted := make(chan struct{})
	closeCalled := make(chan struct{})
	terminal := protocol.TurnCompleted{StopReason: "canceled"}

	var mu sync.Mutex
	var appended []protocol.Event
	var closeBeforeEngineReturn, closeBeforeAppendCompletion bool
	store := &fakeSessionStore{
		append: func(event protocol.Event) error {
			close(appendStarted)
			<-finishAppend
			mu.Lock()
			appended = append(appended, event)
			mu.Unlock()
			close(appendCompleted)
			return nil
		},
		close: func() error {
			select {
			case <-engineReturned:
			default:
				closeBeforeEngineReturn = true
			}
			select {
			case <-appendCompleted:
			default:
				closeBeforeAppendCompletion = true
			}
			close(closeCalled)
			return nil
		},
	}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		<-finishCleanup
		engineEvents <- terminal
		close(engineEvents)
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return nil
		})
	}()

	waitSignal(t, canceled, "engine cancellation")
	close(finishCleanup)
	waitSignal(t, appendStarted, "terminal event append start")
	waitSignal(t, engineReturned, "engine return")
	close(finishAppend)

	if err := waitResult(t, done, "runSession return"); err != nil {
		t.Fatalf("runSession() error = %v, want nil", err)
	}
	waitSignal(t, closeCalled, "store close")
	if closeBeforeEngineReturn {
		t.Error("store.Close occurred before the engine completed cleanup and returned")
	}
	if closeBeforeAppendCompletion {
		t.Error("store.Close occurred before the terminal event Append completed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(appended) != 1 || appended[0] != terminal {
		t.Errorf("appended events = %#v, want terminal event %#v", appended, terminal)
	}
}

func TestRunSessionDrainsEventsAfterFrontendAbandonsForwardedStream(t *testing.T) {
	const eventCount = 4096
	engineEvents := make(chan protocol.Event)
	canceled := make(chan struct{})
	store := &fakeSessionStore{}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		for i := 0; i < eventCount; i++ {
			engineEvents <- protocol.TextDelta{Text: "event"}
		}
		close(engineEvents)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return nil
		})
	}()

	waitSignal(t, canceled, "engine cancellation after frontend return")
	if err := waitResult(t, done, "runSession draining abandoned frontend events"); err != nil {
		t.Fatalf("runSession() error = %v, want nil", err)
	}
	if got := store.appendCount(); got != eventCount {
		t.Errorf("Append call count = %d, want all %d events drained", got, eventCount)
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

func TestRunSessionAppendErrorContinuesDrainingBeforeClose(t *testing.T) {
	firstAppendErr := errors.New("first append failure")
	laterAppendErr := errors.New("later append failure")
	engineEvents := make(chan protocol.Event)
	engineReturned := make(chan struct{})
	const eventCount = 3

	var closeBeforeJoin bool
	store := &fakeSessionStore{}
	appendCall := 0
	store.append = func(protocol.Event) error {
		appendCall++
		switch appendCall {
		case 1:
			return firstAppendErr
		case 2:
			return laterAppendErr
		default:
			return nil
		}
	}
	store.close = func() error {
		select {
		case <-engineReturned:
		default:
			closeBeforeJoin = true
		}
		if store.appendCount() != eventCount {
			closeBeforeJoin = true
		}
		return nil
	}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		for i := 0; i < eventCount; i++ {
			engineEvents <- protocol.TextDelta{Text: "event"}
		}
		close(engineEvents)
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return nil
		})
	}()
	err := waitResult(t, done, "runSession after append errors")
	if !errors.Is(err, firstAppendErr) {
		t.Errorf("runSession() error = %v, want first append error", err)
	}
	if errors.Is(err, laterAppendErr) {
		t.Errorf("runSession() error = %v, want only the first append error preserved", err)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "append") {
		t.Errorf("runSession() error = %v, want append context", err)
	}
	if got := store.appendCount(); got != eventCount {
		t.Errorf("Append call count = %d, want %d despite errors", got, eventCount)
	}
	if closeBeforeJoin {
		t.Error("store.Close occurred before engine return and tee drain completion")
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

func TestRunSessionSurfacesFrontendAndCloseErrorsAfterShutdown(t *testing.T) {
	frontendErr := errors.New("frontend failure")
	closeErr := errors.New("close failure")
	engineEvents := make(chan protocol.Event)
	engineReturned := make(chan struct{})
	store := &fakeSessionStore{close: func() error { return closeErr }}
	engineRun := func(ctx context.Context) {
		<-ctx.Done()
		close(engineEvents)
		close(engineReturned)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), engineRun, engineEvents, store, func(<-chan protocol.Event) error {
			return frontendErr
		})
	}()
	err := waitResult(t, done, "runSession after frontend error")
	if !errors.Is(err, frontendErr) {
		t.Errorf("runSession() error = %v, want joined frontend error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Errorf("runSession() error = %v, want joined close error", err)
	}
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		if !strings.Contains(lowerErr, "frontend") {
			t.Errorf("runSession() error = %v, want frontend context", err)
		}
		if !strings.Contains(lowerErr, "clos") {
			t.Errorf("runSession() error = %v, want close context", err)
		}
	}
	select {
	case <-engineReturned:
	default:
		t.Error("runSession returned before the engine")
	}
	if got := store.closeCount(); got != 1 {
		t.Errorf("Close call count = %d, want 1", got)
	}
}

type fakeSessionStore struct {
	mu       sync.Mutex
	appended []protocol.Event
	closes   int
	append   func(protocol.Event) error
	close    func() error
}

func (s *fakeSessionStore) Append(event protocol.Event) error {
	s.mu.Lock()
	s.appended = append(s.appended, event)
	s.mu.Unlock()
	if s.append != nil {
		return s.append(event)
	}
	return nil
}

func (s *fakeSessionStore) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	if s.close != nil {
		return s.close()
	}
	return nil
}

func (s *fakeSessionStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appended)
}

func (s *fakeSessionStore) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitResult(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func TestWithReplayPrependsHistory(t *testing.T) {
	live := make(chan protocol.Event, 1)
	history := []protocol.Event{
		protocol.UserMessage{Text: "past"},
		protocol.TextDelta{Text: "reply"},
	}
	out := withReplay(history, live)
	live <- protocol.UserMessage{Text: "live"}
	close(live)

	var got []string
	for ev := range out {
		switch e := ev.(type) {
		case protocol.UserMessage:
			got = append(got, "u:"+e.Text)
		case protocol.TextDelta:
			got = append(got, "t:"+e.Text)
		}
	}
	want := []string{"u:past", "t:reply", "u:live"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestWithReplayEmptyPassthrough(t *testing.T) {
	live := make(chan protocol.Event, 1)
	if withReplay(nil, live) != live {
		t.Fatal("empty history should return live channel")
	}
	close(live)
}
