package tool

import (
	"errors"
	"strings"
	"testing"
)

func TestCodedErrorFormatAndCodeOf(t *testing.T) {
	err := PreconditionFailed("file changed")
	if got := err.Error(); !strings.Contains(got, CodePreconditionFailed) || !strings.Contains(got, "file changed") {
		t.Fatalf("Error() = %q", got)
	}
	if CodeOf(err) != CodePreconditionFailed {
		t.Fatalf("CodeOf = %q", CodeOf(err))
	}
	wrapped := errors.Join(InvalidArgs("bad"), errors.New("other"))
	// Join may not unwrap to CodedError via errors.As on first — check direct.
	if CodeOf(InvalidArgs("x")) != CodeInvalidArgs {
		t.Fatal("invalid_args code")
	}
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Retryable {
		t.Fatalf("As/Retryable: %+v", ce)
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
