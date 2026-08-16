package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

const (
	attachmentRefPrefix = "att:sha256:"
	attachmentKindImage = "image"
)

var providerImageMIME = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

func attachmentRefFor(hexSum string) string {
	return attachmentRefPrefix + strings.ToLower(strings.TrimSpace(hexSum))
}

func attachmentRedactName(name string) string {
	return redact.String(name)
}

func normalizeAttachmentMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	if mime == "image/jpg" {
		return "image/jpeg"
	}
	return mime
}

func attachmentKindFromMIME(mime string) string {
	switch normalizeAttachmentMIME(mime) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return attachmentKindImage
	default:
		return ""
	}
}

func selectAttachmentForProvider(kind, mime string, imagesOK bool) error {
	if !imagesOK {
		return fmt.Errorf("attachment: unsupported format: selected model does not support image attachments")
	}
	mime = normalizeAttachmentMIME(mime)
	if kind == "" {
		kind = attachmentKindFromMIME(mime)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != attachmentKindImage {
		if kind == "" {
			kind = mime
		}
		return fmt.Errorf("attachment: unsupported format: %s cannot be sent as a provider image", kind)
	}
	for _, a := range providerImageMIME {
		if normalizeAttachmentMIME(a) == mime {
			return nil
		}
	}
	return fmt.Errorf("attachment: unsupported format: unsupported image format (%s)", mime)
}
