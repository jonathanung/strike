package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestDeliverImagesAnnotatesEachAttachment(t *testing.T) {
	png := protocol.ImageAttachment{MIME: "image/png", Data: "iVBORw0KGgo=", Name: "shot.png"}
	pdf := protocol.ImageAttachment{MIME: "application/pdf", Data: "JVBERi0=", Kind: protocol.AttachmentKindPDF, Name: "spec.pdf"}
	refOnly := protocol.ImageAttachment{MIME: "image/png", Ref: "att:sha256:" + strings.Repeat("ab", 32)}
	forged := pdf
	forged.Delivery = protocol.AttachmentDeliverySent

	cases := []struct {
		name       string
		provider   string
		in         protocol.ImageAttachment
		delivery   string
		reasonPart string
		sent       int
		errors     int
	}{
		{"png on a vision provider", "anthropic", png, protocol.AttachmentDeliverySent, "", 1, 0},
		{"png on echo", "echo", png, protocol.AttachmentDeliveryDropped, "does not support image attachments", 0, 1},
		{"pdf on any provider", "anthropic", pdf, protocol.AttachmentDeliveryDropped, "cannot be sent as a provider image", 0, 1},
		{"ref with no stored bytes", "anthropic", refOnly, protocol.AttachmentDeliveryDropped, "bytes unavailable", 0, 0},
		{"client-forged delivery is overwritten", "anthropic", forged, protocol.AttachmentDeliveryDropped, "cannot be sent", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New(Options{Registry: tool.NewRegistry()})
			e.provName = tc.provider
			e.events = make(chan protocol.Event, 8)
			annotated, sent := e.deliverImages([]protocol.ImageAttachment{tc.in})
			if len(annotated) != 1 {
				t.Fatalf("annotated = %#v", annotated)
			}
			got := annotated[0]
			if got.Delivery != tc.delivery {
				t.Fatalf("delivery = %q, want %q", got.Delivery, tc.delivery)
			}
			if tc.reasonPart == "" && got.DeliveryReason != "" {
				t.Fatalf("sent attachment carries reason %q", got.DeliveryReason)
			}
			if !strings.Contains(got.DeliveryReason, tc.reasonPart) {
				t.Fatalf("reason = %q, want it to contain %q", got.DeliveryReason, tc.reasonPart)
			}
			if strings.HasPrefix(got.DeliveryReason, "attachment:") {
				t.Fatalf("reason keeps the error prefix: %q", got.DeliveryReason)
			}
			if got.Name != tc.in.Name || got.MIME != tc.in.MIME {
				t.Fatalf("attachment metadata changed: %#v", got)
			}
			if len(sent) != tc.sent {
				t.Fatalf("sent %d images, want %d", len(sent), tc.sent)
			}
			if n := len(e.events); n != tc.errors {
				t.Fatalf("emitted %d events, want %d engine.error", n, tc.errors)
			}
		})
	}
}

func TestDeliverImagesKeepsOrderForMixedTurn(t *testing.T) {
	e := New(Options{Registry: tool.NewRegistry()})
	e.provName = "anthropic"
	e.events = make(chan protocol.Event, 8)
	in := []protocol.ImageAttachment{
		{MIME: "application/pdf", Data: "JVBERi0=", Name: "a.pdf"},
		{MIME: "image/png", Data: "iVBORw0KGgo=", Name: "b.png"},
	}
	annotated, sent := e.deliverImages(in)
	if annotated[0].Delivery != protocol.AttachmentDeliveryDropped || annotated[1].Delivery != protocol.AttachmentDeliverySent {
		t.Fatalf("annotated = %#v", annotated)
	}
	if len(sent) != 1 || sent[0].MIME != "image/png" {
		t.Fatalf("sent = %#v", sent)
	}
	if in[0].Delivery != "" {
		t.Fatal("deliverImages mutated its input")
	}
	if a, s := e.deliverImages(nil); a != nil || s != nil {
		t.Fatalf("nil input = %#v, %#v", a, s)
	}
}
