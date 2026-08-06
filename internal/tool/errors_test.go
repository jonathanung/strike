package tool

import (
	"errors"
	"strings"
	"testing"
)

func TestCodedErrorFormatAndCodeOf(t *testing.T) {
	err := PreconditionFailed("file changed")
	if got := err.Error(); !strings.Contains(got, string(CodePreconditionFailed)) || !strings.Contains(got, "file changed") {
		t.Fatalf("Error() = %q", got)
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("CodeOf = %q", CodeOf(err))
	}
	wrapped := errors.Join(InvalidArgs("bad"), errors.New("other"))
	if CodeOf(InvalidArgs("x")) != string(CodeInvalidArgs) {
		t.Fatal("invalid_args code")
	}
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Retryable {
		t.Fatalf("As/Retryable: %+v", ce)
	}
	// "code: message" greppable form.
	if !strings.HasPrefix(ce.Error(), string(CodePreconditionFailed)+": ") {
		t.Fatalf("format = %q", ce.Error())
	}
	_ = wrapped
}

func TestCodeOfNil(t *testing.T) {
	if CodeOf(nil) != "" {
		t.Fatal("want empty")
	}
	if CodeOf(errors.New("plain")) != "" {
		t.Fatal("want empty for plain")
	}
}

func TestErrorHelpers(t *testing.T) {
	t.Parallel()
	if !ErrTimeout("t").Retryable || ErrTimeout("t").Code != CodeTimeout {
		t.Fatal("timeout")
	}
	if !ErrTransient("t").Retryable {
		t.Fatal("transient")
	}
	if ErrInternal("x").Retryable || !strings.Contains(ErrInternal("x").Error(), "x") {
		t.Fatal("internal")
	}
	if RetryableForCode(CodeTransient) != true || RetryableForCode(CodeInvalidArgs) != false {
		t.Fatal("RetryableForCode")
	}
	if !ValidErrorCode(CodeBlocked) {
		t.Fatal("blocked")
	}
}
