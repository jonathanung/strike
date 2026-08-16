package engine

import "testing"

func TestAttachmentRefFor(t *testing.T) {
	got := attachmentRefFor("ABC")
	if got != "att:sha256:abc" {
		t.Fatalf("got %q", got)
	}
}

func TestSelectAttachmentForProvider(t *testing.T) {
	if err := selectAttachmentForProvider("", "image/png", true); err != nil {
		t.Fatalf("png: %v", err)
	}
	if err := selectAttachmentForProvider("", "image/png", false); err == nil {
		t.Fatal("expected reject when images disabled")
	}
	if err := selectAttachmentForProvider("pdf", "application/pdf", true); err == nil {
		t.Fatal("expected reject non-image")
	}
}

func TestAttachmentKindFromMIME(t *testing.T) {
	if got := attachmentKindFromMIME("image/jpeg"); got != attachmentKindImage {
		t.Fatalf("got %q", got)
	}
	if got := attachmentKindFromMIME("application/pdf"); got != "" {
		t.Fatalf("got %q", got)
	}
}
