package tui

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Image paste/drop limits and allowlist (composer chips → multimodal send).
const (
	maxImageBytes       = 5 << 20 // 5 MiB per image
	maxPendingImages    = 4
	imageChipPrefix     = "[image "
	imageChipClose      = "]"
	imageUnsupportedMsg = "selected model does not support image attachments"
)

// imageChip is one attached image: Placeholder appears in the composer Value,
// Attachment is the protocol payload expanded on send.
type imageChip struct {
	Placeholder string
	Attachment  protocol.ImageAttachment
}

func imagePlaceholderLabel(n int) string {
	return fmt.Sprintf("%s%d%s", imageChipPrefix, n, imageChipClose)
}

func (m *Model) imagePlaceholderInUse(label string) bool {
	if strings.Contains(m.composer.Value(), label) {
		return true
	}
	for _, p := range m.pendingImages {
		if p.Placeholder == label {
			return true
		}
	}
	return false
}

func (m *Model) nextImagePlaceholder() string {
	for n := 1; ; n++ {
		label := imagePlaceholderLabel(n)
		if !m.imagePlaceholderInUse(label) {
			return label
		}
	}
}

func expandPendingImages(text string, images []imageChip) string {
	// Chips stay as labels in display text; attachments travel separately.
	_ = images
	return text
}

func prunePendingImages(value string, images []imageChip) []imageChip {
	if len(images) == 0 {
		return nil
	}
	kept := make([]imageChip, 0, len(images))
	for _, img := range images {
		if img.Placeholder != "" && strings.Contains(value, img.Placeholder) {
			kept = append(kept, img)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func pendingImageAttachments(images []imageChip) []protocol.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]protocol.ImageAttachment, 0, len(images))
	for _, img := range images {
		if img.Attachment.MIME == "" || img.Attachment.Data == "" {
			continue
		}
		out = append(out, img.Attachment)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// displayPromptWithImages appends chip labels so history/transcript stay
// human-readable without embedding base64.
func displayPromptWithImages(text string, images []imageChip) string {
	if len(images) == 0 {
		return text
	}
	// Composer already contains chip placeholders when present; ensure labels
	// appear even if the user cleared surrounding text oddly.
	var missing []string
	for _, img := range images {
		if img.Placeholder != "" && !strings.Contains(text, img.Placeholder) {
			missing = append(missing, img.Placeholder)
		}
	}
	if len(missing) == 0 {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return strings.Join(missing, " ")
	}
	return text + " " + strings.Join(missing, " ")
}

// userMessageDisplayText is transcript-facing: text plus [image N] labels,
// never raw binary/base64.
func userMessageDisplayText(text string, images []protocol.ImageAttachment) string {
	if len(images) == 0 {
		return text
	}
	labels := make([]string, 0, len(images))
	for i := range images {
		labels = append(labels, imagePlaceholderLabel(i+1))
	}
	joined := strings.Join(labels, " ")
	if strings.TrimSpace(text) == "" {
		return joined
	}
	// Avoid duplicating chips already in the text.
	for _, lab := range labels {
		if strings.Contains(text, lab) {
			return text
		}
	}
	return text + "\n" + joined
}

// tryAttachImagePaste detects an image paste/drop and attaches a chip.
// Returns true when the paste was handled as an image (including refuse notices).
func (m *Model) tryAttachImagePaste(raw string) bool {
	att, notice, ok := parseImagePaste(raw)
	if !ok {
		return false
	}
	m.resetHistoryBrowsing()
	if notice != "" {
		m.setNotice(notice, true)
		return true
	}
	if len(m.pendingImages) >= maxPendingImages {
		m.setNotice(fmt.Sprintf("too many images (max %d)", maxPendingImages), true)
		return true
	}
	placeholder := m.nextImagePlaceholder()
	m.pendingImages = append(m.pendingImages, imageChip{
		Placeholder: placeholder,
		Attachment:  att,
	})
	m.composer.InsertString(placeholder)
	return true
}

// parseImagePaste tries data-URI, raw image bytes, then filesystem path(s).
// ok is false when the paste is not an image candidate.
//
// Raw image bytes are sniffed before CR/LF normalization — PNG signatures
// contain 0x0d 0x0a and must not be rewritten as text.
func parseImagePaste(raw string) (protocol.ImageAttachment, string, bool) {
	if att, notice, ok := parseRawImageBytes([]byte(raw)); ok {
		return att, notice, true
	}
	s := strings.TrimSpace(normalizePaste(raw))
	if s == "" {
		return protocol.ImageAttachment{}, "", false
	}
	if att, notice, ok := parseDataURIImage(s); ok {
		return att, notice, true
	}
	if att, notice, ok := parseImageFilePath(s); ok {
		return att, notice, true
	}
	return protocol.ImageAttachment{}, "", false
}

func parseDataURIImage(s string) (protocol.ImageAttachment, string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(strings.ToLower(s), prefix) {
		return protocol.ImageAttachment{}, "", false
	}
	// data:image/png;base64,<data>
	rest := s[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return protocol.ImageAttachment{}, "invalid image data URI", true
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	parts := strings.Split(meta, ";")
	mime := strings.TrimSpace(parts[0])
	if !allowedImageMIME(mime) {
		return protocol.ImageAttachment{}, "unsupported image format (png/jpeg/webp/gif)", true
	}
	isB64 := false
	for _, p := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "base64") {
			isB64 = true
			break
		}
	}
	if !isB64 {
		return protocol.ImageAttachment{}, "image data URI must be base64", true
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return protocol.ImageAttachment{}, "invalid image base64", true
		}
	}
	return encodeImageAttachment(mime, raw)
}

func parseRawImageBytes(raw []byte) (protocol.ImageAttachment, string, bool) {
	mime := sniffImageMIME(raw)
	if mime == "" {
		return protocol.ImageAttachment{}, "", false
	}
	return encodeImageAttachment(mime, raw)
}

func parseImageFilePath(s string) (protocol.ImageAttachment, string, bool) {
	path := strings.Trim(s, `"'`)
	path = strings.TrimPrefix(path, "file://")
	// Single path only (drag-drop often pastes one path).
	if strings.ContainsAny(path, "\n\r\t") {
		// Take first non-empty line if multi-line drop of one path.
		for _, line := range strings.Split(path, "\n") {
			line = strings.TrimSpace(strings.Trim(line, "\"'"))
			line = strings.TrimPrefix(line, "file://")
			if line != "" {
				path = line
				break
			}
		}
	}
	if path == "" || strings.ContainsAny(path, "\n\r") {
		return protocol.ImageAttachment{}, "", false
	}
	// Reject obvious non-paths (long prose, spaces without existing file).
	if utf8.RuneCountInString(path) > 4096 {
		return protocol.ImageAttachment{}, "", false
	}
	if !looksLikeImagePath(path) {
		// Still try if the file exists and sniffs as image.
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			return protocol.ImageAttachment{}, "", false
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if looksLikeImagePath(path) {
			return protocol.ImageAttachment{}, "could not read image file", true
		}
		return protocol.ImageAttachment{}, "", false
	}
	mime := sniffImageMIME(raw)
	if mime == "" {
		if looksLikeImagePath(path) {
			return protocol.ImageAttachment{}, "unsupported image format (png/jpeg/webp/gif)", true
		}
		return protocol.ImageAttachment{}, "", false
	}
	return encodeImageAttachment(mime, raw)
}

func looksLikeImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func encodeImageAttachment(mime string, raw []byte) (protocol.ImageAttachment, string, bool) {
	mime = normalizeImageMIME(mime)
	if !allowedImageMIME(mime) {
		return protocol.ImageAttachment{}, "unsupported image format (png/jpeg/webp/gif)", true
	}
	if len(raw) == 0 {
		return protocol.ImageAttachment{}, "empty image", true
	}
	if n := len(raw); n > maxImageBytes {
		return protocol.ImageAttachment{}, fmt.Sprintf("image too large (max %d MB)", maxImageBytes>>20), true
	}
	return protocol.ImageAttachment{
		MIME: mime,
		Data: base64.StdEncoding.EncodeToString(raw),
	}, "", true
}

func normalizeImageMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpg":
		return "image/jpeg"
	default:
		return strings.ToLower(strings.TrimSpace(mime))
	}
}

func allowedImageMIME(mime string) bool {
	switch normalizeImageMIME(mime) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func sniffImageMIME(raw []byte) string {
	if len(raw) < 3 {
		return ""
	}
	// Magic first so short/minimal fixtures and paste blobs still match.
	switch {
	case len(raw) >= 8 && raw[0] == 0x89 && raw[1] == 0x50 && raw[2] == 0x4e && raw[3] == 0x47:
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "image/jpeg"
	case len(raw) >= 6 && (string(raw[0:6]) == "GIF87a" || string(raw[0:6]) == "GIF89a"):
		return "image/gif"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	}
	ct := http.DetectContentType(raw)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	default:
		return ""
	}
}

// modelSupportsImages reports whether the selected model can receive images.
// echo never supports; otherwise use cached catalog Attachment when known.
func (m Model) modelSupportsImages() (ok bool, known bool) {
	if m.providerName == "" {
		return false, true
	}
	if m.providerName == "echo" {
		return false, true
	}
	if m.modelAttachmentKnown {
		return m.modelAttachment, true
	}
	return true, false // optimistic for real providers until catalog answers
}
