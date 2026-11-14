package engine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// authStatus mimics base.StatusError for a vendor 401: classified by the
// adapter, never by message text (#1546).
type authStatus struct {
	provider string
	msg      string
}

func (e authStatus) Error() string               { return e.msg }
func (e authStatus) Retryable() bool             { return false }
func (e authStatus) AuthFailure() (string, bool) { return e.provider, true }

func failTurnWith(t *testing.T, streamErr error) (protocol.EngineError, protocol.TurnCompleted, int) {
	t.Helper()
	prov := &scriptedProvider{steps: []streamStep{{err: streamErr}}}
	eng := engine.New(engine.Options{
		SessionID:          "session-auth",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var failure protocol.EngineError
	var completed protocol.TurnCompleted
	deadline := time.After(2 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.EngineError:
				failure = ev
			case protocol.TurnCompleted:
				completed = ev
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	return failure, completed, prov.callCount()
}

func TestProviderAuthFailureEmitsClassifiedEngineError(t *testing.T) {
	err := fmt.Errorf("stream: %w", authStatus{provider: "openai", msg: "openai: invalid_request_error: Incorrect API key provided"})
	failure, completed, calls := failTurnWith(t, err)
	if calls != 1 {
		t.Fatalf("Stream calls = %d, want 1 (auth failures are not retried)", calls)
	}
	if failure.Code != protocol.ErrorCodeProviderAuth || failure.Provider != "openai" {
		t.Fatalf("EngineError = %#v, want code provider_auth provider openai", failure)
	}
	if failure.Message == "" {
		t.Fatal("vendor message must stay available as secondary detail")
	}
	if completed.StopReason != "error" {
		t.Fatalf("stop reason = %q", completed.StopReason)
	}
}

func TestUnclassifiedProviderFailureKeepsGenericEngineError(t *testing.T) {
	// Plain text that merely mentions 401 is never classified: only the
	// adapter that saw the HTTP status may say "auth".
	failure, _, _ := failTurnWith(t, errors.New("upstream said 401 unauthorized"))
	if failure.Code != "" || failure.Provider != "" {
		t.Fatalf("EngineError = %#v, want unclassified", failure)
	}
}
