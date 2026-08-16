package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestContractVocabulary(t *testing.T) {
	t.Parallel()
	for _, se := range []SideEffect{
		SideEffectNone, SideEffectRead, SideEffectWorkspaceMutative,
		SideEffectProcess, SideEffectNetwork, SideEffectExternal,
	} {
		if !ValidSideEffect(se) {
			t.Errorf("ValidSideEffect(%q) = false", se)
		}
	}
	if ValidSideEffect("mutate") {
		t.Error("unknown side effect accepted")
	}
	for _, id := range []Idempotency{IdempotencySafeRetry, IdempotencyConditional, IdempotencyUnsafe} {
		if !ValidIdempotency(id) {
			t.Errorf("ValidIdempotency(%q) = false", id)
		}
	}
	for _, c := range []ErrorCode{
		CodePermissionDenied, CodeInvalidArgs, CodePreconditionFailed,
		CodeCanceled, CodeTimeout, CodeTransient, CodeInternal, CodeBlocked,
		CodeSandboxDenied, CodeContentGuardDenied, CodeNetworkDenied,
	} {
		if !ValidErrorCode(c) {
			t.Errorf("ValidErrorCode(%q) = false", c)
		}
	}
}

func TestContractValidate(t *testing.T) {
	t.Parallel()
	ok := staticContract(SideEffectRead, IdempotencySafeRetry)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Contract{Version: 0, SideEffect: SideEffectRead, Idempotency: IdempotencySafeRetry}).Validate(); err == nil {
		t.Fatal("expected version error")
	}
	if err := (Contract{Version: 1, SideEffect: "nope", Idempotency: IdempotencySafeRetry}).Validate(); err == nil {
		t.Fatal("expected sideEffect error")
	}
}

func TestLookupContractDefault(t *testing.T) {
	t.Parallel()
	if c := LookupContract(nil); c != DefaultContract() {
		t.Fatalf("nil = %+v", c)
	}
	// Anonymous tool without Contractor.
	type bare struct{}
	// Can't implement Tool without methods — use a stub.
	stub := stubTool{name: "plugin_x"}
	c := LookupContract(stub)
	if c.SideEffect != SideEffectExternal || c.Idempotency != IdempotencyConditional {
		t.Fatalf("default = %+v", c)
	}
}

type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "" }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) Execute(context.Context, json.RawMessage, *Context) (Result, error) {
	return Result{}, nil
}

func TestMutativeToolContracts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool Tool
		se   SideEffect
		id   Idempotency
	}{
		{NewEdit(), SideEffectWorkspaceMutative, IdempotencyConditional},
		{NewWrite(), SideEffectWorkspaceMutative, IdempotencyConditional},
		{NewApplyPatch(), SideEffectWorkspaceMutative, IdempotencyConditional},
		{NewMove(), SideEffectWorkspaceMutative, IdempotencyConditional},
		{NewDelete(), SideEffectWorkspaceMutative, IdempotencyConditional},
		{NewBash(), SideEffectProcess, IdempotencyUnsafe},
	}
	for _, tc := range cases {
		c := LookupContract(tc.tool)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tc.tool.Name(), err)
		}
		if c.SideEffect != tc.se || c.Idempotency != tc.id || c.Version != ContractVersion {
			t.Errorf("%s contract = %+v, want se=%s id=%s v=%d", tc.tool.Name(), c, tc.se, tc.id, ContractVersion)
		}
	}
}

func TestOneToolPerSideEffectClass(t *testing.T) {
	t.Parallel()
	// Acceptance: at least one tool per side-effect class.
	reps := map[SideEffect]Tool{
		SideEffectNone:              NewSleep(),
		SideEffectRead:              NewRead(),
		SideEffectWorkspaceMutative: NewEdit(),
		SideEffectProcess:           NewBash(),
		SideEffectNetwork:           NewWebFetch(),
		SideEffectExternal:          NewAgentMessage(),
	}
	for se, tl := range reps {
		c := LookupContract(tl)
		if c.SideEffect != se {
			t.Errorf("%s: sideEffect=%s, want %s", tl.Name(), c.SideEffect, se)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tl.Name(), err)
		}
	}
}

func TestRegistryContracts(t *testing.T) {
	t.Parallel()
	r := NewRegistry(NewRead(), NewEdit(), NewBash(), NewWebFetch(), NewSleep())
	all := r.Contracts()
	if len(all) != 5 {
		t.Fatalf("len=%d", len(all))
	}
	c, ok := r.Contract("edit")
	if !ok || c.SideEffect != SideEffectWorkspaceMutative {
		t.Fatalf("edit = %+v ok=%v", c, ok)
	}
	if _, ok := r.Contract("missing"); ok {
		t.Fatal("missing should be absent")
	}
	// Every registered contract validates.
	for name, c := range all {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestClassifyStructuredAndFallback(t *testing.T) {
	t.Parallel()
	if Classify(nil) != nil {
		t.Fatal("nil")
	}
	inv := ErrInvalidArgs("bad json")
	got := Classify(inv)
	if got != inv || got.Code != CodeInvalidArgs {
		t.Fatalf("structured = %+v", got)
	}
	// Unknown → internal, never panic.
	unk := Classify(errors.New("something weird"))
	if unk.Code != CodeInternal || unk.Retryable || unk.Message != "something weird" {
		t.Fatalf("fallback = %+v", unk)
	}
	// Heuristic invalid arguments.
	h := Classify(errors.New("invalid arguments: eof"))
	if h.Code != CodeInvalidArgs {
		t.Fatalf("heuristic = %+v", h)
	}
	// Workspace escape → precondition.
	esc := Classify(&WorkspaceEscapeError{Path: "../x", Root: "/w"})
	if esc.Code != CodePreconditionFailed {
		t.Fatalf("escape = %+v", esc)
	}
	// Context canceled / deadline.
	if c := Classify(context.Canceled); c.Code != CodeCanceled {
		t.Fatalf("canceled = %+v", c)
	}
	if c := Classify(context.DeadlineExceeded); c.Code != CodeTimeout || !c.Retryable {
		t.Fatalf("deadline = %+v", c)
	}
}

func TestEditInvalidArgsVsPermissionShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Invalid JSON → invalid_args.
	_, err := NewEdit().Execute(context.Background(), json.RawMessage(`{`), allowAll(dir))
	var te *CodedError
	if !errors.As(err, &te) || te.Code != CodeInvalidArgs {
		t.Fatalf("bad json err = %v (%T)", err, err)
	}
	// Permission deny passes through as non-CodedError (engine classifies).
	tc := &Context{
		WorkDir: dir,
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied-by-test") },
		Files:   &FileState{},
	}
	// Empty old==new is invalid_args before Ask.
	_, err = NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.txt",
		"oldString": "x",
		"newString": "x",
	}), tc)
	if !errors.As(err, &te) || te.Code != CodeInvalidArgs {
		t.Fatalf("identical strings = %v", err)
	}
	if te.Code == CodePermissionDenied {
		t.Fatal("invalid_args must not be permission_denied")
	}
}
