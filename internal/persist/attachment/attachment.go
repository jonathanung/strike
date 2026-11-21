// Package attachment stores typed, content-addressed user and generated
// payloads (images, PDFs, diagrams, logs, archives, build artifacts).
//
// History and protocol events keep addressable refs (att:sha256:<hex>) instead
// of embedding full bytes. The engine resolves refs when sending selected
// representations to a provider.
//
// Non-overlap vs sibling stores:
//   - pkg/timeline blob spill — oversized redacted tool text, not typed attachments
//   - internal/persist/artifact — versioned multi-agent work products (findings/patches)
package attachment

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KindImage   = "image"
	KindPDF     = "pdf"
	KindDiagram = "diagram"
	KindLog     = "log"
	KindArchive = "archive"
	KindBuild   = "build"
	// KindDocument is an Office Open XML document (docx/xlsx/pptx).
	KindDocument = "document"
	KindAudio    = "audio"
	KindVideo    = "video"

	// RefPrefix is the scheme for content-addressed attachment refs.
	// Full form: att:sha256:<hex>
	RefPrefix = "att:sha256:"

	// DefaultMaxBytes is the per-blob ingest cap (matches TUI image paste).
	DefaultMaxBytes = 5 << 20
)

// Office Open XML media types, detected from zip entry names.
const (
	MIMEDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MIMEXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	MIMEPptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// maxZipSniffEntries bounds the entry names inspected for OOXML markers.
// Only the central directory is read; no entry is decompressed.
const maxZipSniffEntries = 4096

// ProviderImageMIME is the default set sent to vision-capable providers.
var ProviderImageMIME = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

var (
	ErrEmpty       = errors.New("attachment: empty payload")
	ErrTooLarge    = errors.New("attachment: payload too large")
	ErrUnsupported = errors.New("attachment: unsupported format")
	ErrNotFound    = errors.New("attachment: not found")
	ErrClosed      = errors.New("attachment: store is closed")
	ErrInvalidRef  = errors.New("attachment: invalid ref")
	ErrKind        = errors.New("attachment: kind mismatch")
)

// Meta is durable sidecar metadata for one stored blob.
type Meta struct {
	SHA256    string    `json:"sha256"`
	Kind      string    `json:"kind"`
	MIME      string    `json:"mime"`
	Name      string    `json:"name,omitempty"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"created_at"`
	SessionID string    `json:"session_id,omitempty"`
	Redacted  bool      `json:"redacted,omitempty"`
	Links     []Link    `json:"links,omitempty"`
}

// Link associates an attachment with a plan, finding, test, patch, or review.
type Link struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// PutInput is optional classification supplied at ingest.
type PutInput struct {
	MIME      string
	Name      string
	Kind      string
	SessionID string
	Links     []Link
	MaxBytes  int
}

// Region is a pixel rectangle for redaction (x,y origin top-left).
type Region struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Capabilities describes what a provider can consume.
type Capabilities struct {
	Images    bool
	ImageMIME []string // empty uses ProviderImageMIME
}

// CompareResult is structured visual-comparison evidence for verification gates.
type CompareResult struct {
	Equal      bool   `json:"equal"`
	Method     string `json:"method"`
	DiffPixels int    `json:"diffPixels,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SHA256A    string `json:"sha256A"`
	SHA256B    string `json:"sha256B"`
	Detail     string `json:"detail,omitempty"`
}

// RefFor returns att:sha256:<hex> for a hex digest.
func RefFor(hexSum string) string {
	return RefPrefix + strings.ToLower(strings.TrimSpace(hexSum))
}

// ParseRef extracts the hex digest from an attachment ref.
func ParseRef(ref string) (hexSum string, ok bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, RefPrefix) {
		return "", false
	}
	hexSum = strings.TrimPrefix(ref, RefPrefix)
	if len(hexSum) != 64 {
		return "", false
	}
	for _, c := range hexSum {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return hexSum, true
}

// SumHex returns the lowercase sha256 hex of raw.
func SumHex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// KindFromMIME maps a media type to a stored kind. Empty if unknown.
func KindFromMIME(mime string) string {
	switch normalizeMIME(mime) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return KindImage
	case "application/pdf":
		return KindPDF
	case "image/svg+xml":
		return KindDiagram
	case "text/plain", "text/x-log", "text/log":
		return KindLog
	case "application/zip", "application/gzip", "application/x-tar", "application/x-gzip":
		return KindArchive
	case MIMEDocx, MIMEXlsx, MIMEPptx:
		return KindDocument
	case "audio/mpeg", "audio/wav", "audio/ogg", "audio/webm", "audio/mp4":
		return KindAudio
	case "video/mp4", "video/webm":
		return KindVideo
	default:
		return ""
	}
}

// ValidKind reports whether kind is a known attachment type.
func ValidKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindImage, KindPDF, KindDiagram, KindLog, KindArchive, KindBuild, KindDocument, KindAudio, KindVideo:
		return true
	default:
		return false
	}
}

// SniffMIME detects a supported media type from magic bytes.
func SniffMIME(raw []byte) string {
	if len(raw) < 3 {
		return ""
	}
	switch {
	case len(raw) >= 8 && raw[0] == 0x89 && raw[1] == 0x50 && raw[2] == 0x4e && raw[3] == 0x47:
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "image/jpeg"
	case len(raw) >= 6 && (string(raw[0:6]) == "GIF87a" || string(raw[0:6]) == "GIF89a"):
		return "image/gif"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WAVE":
		return "audio/wav"
	case len(raw) >= 5 && string(raw[0:5]) == "%PDF-":
		return "application/pdf"
	case len(raw) >= 4 && raw[0] == 0x50 && raw[1] == 0x4b && (raw[2] == 0x03 || raw[2] == 0x05 || raw[2] == 0x07) && (raw[3] == 0x04 || raw[3] == 0x06 || raw[3] == 0x08):
		if m := sniffOOXML(raw); m != "" {
			return m
		}
		return "application/zip"
	case len(raw) >= 3 && string(raw[0:3]) == "ID3":
		return "audio/mpeg"
	case len(raw) >= 4 && string(raw[0:4]) == "OggS":
		return "audio/ogg"
	case len(raw) >= 4 && raw[0] == 0x1a && raw[1] == 0x45 && raw[2] == 0xdf && raw[3] == 0xa3:
		return "video/webm"
	case len(raw) >= 12 && string(raw[4:8]) == "ftyp":
		if m := sniffISOBrand(string(raw[8:12])); m != "" {
			return m
		}
	case len(raw) >= 3 && raw[0] == 0xff && raw[1]&0xe0 == 0xe0 && raw[1]&0x06 != 0 && raw[2]&0xf0 != 0xf0:
		// MPEG audio frame sync without an ID3 tag (layer bits set,
		// bitrate index not "bad"). JPEG (ff d8) is matched above.
		return "audio/mpeg"
	case len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b:
		return "application/gzip"
	case bytes.HasPrefix(raw, []byte("<svg")) || bytes.HasPrefix(raw, []byte("<?xml")):
		if bytes.Contains(raw[:min(len(raw), 256)], []byte("<svg")) {
			return "image/svg+xml"
		}
	}
	ct := http.DetectContentType(raw)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "text/plain; charset=utf-8":
		if ct == "text/plain; charset=utf-8" {
			if utf8.Valid(raw) {
				return "text/plain"
			}
			return ""
		}
		return ct
	default:
		return ""
	}
}

// sniffOOXML returns the OOXML media type when a zip's entry names carry
// the package markers ([Content_Types].xml plus word/, xl/ or ppt/).
// Only the central directory is parsed, bounded by maxZipSniffEntries;
// anything unreadable is left as a plain zip ("").
func sniffOOXML(raw []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return ""
	}
	var types, word, xl, ppt bool
	for i, f := range zr.File {
		if i >= maxZipSniffEntries {
			break
		}
		name := f.Name
		switch {
		case name == "[Content_Types].xml":
			types = true
		case strings.HasPrefix(name, "word/"):
			word = true
		case strings.HasPrefix(name, "xl/"):
			xl = true
		case strings.HasPrefix(name, "ppt/"):
			ppt = true
		}
	}
	if !types {
		return ""
	}
	switch {
	case word && !xl && !ppt:
		return MIMEDocx
	case xl && !word && !ppt:
		return MIMEXlsx
	case ppt && !word && !xl:
		return MIMEPptx
	default:
		return ""
	}
}

// sniffISOBrand maps an ISO base media (ftyp) major brand to a type.
// HEIC/AVIF and QuickTime brands are left unknown rather than guessed.
func sniffISOBrand(brand string) string {
	switch brand {
	case "isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "dash", "M4V ", "mmp4", "MSNV":
		return "video/mp4"
	case "M4A ":
		return "audio/mp4"
	default:
		return ""
	}
}

func isOOXML(mime string) bool {
	switch mime {
	case MIMEDocx, MIMEXlsx, MIMEPptx:
		return true
	default:
		return false
	}
}

// Classify resolves kind and MIME from bytes plus optional declared values.
func Classify(raw []byte, declaredMIME, declaredKind string) (kind, mime string, err error) {
	if len(raw) == 0 {
		return "", "", ErrEmpty
	}
	sniffed := SniffMIME(raw)
	mime = normalizeMIME(declaredMIME)
	if mime == "" {
		mime = sniffed
	}
	if mime == "" && strings.EqualFold(strings.TrimSpace(declaredKind), KindBuild) {
		mime = "application/octet-stream"
	}
	if mime == "" {
		return "", "", fmt.Errorf("%w: could not detect type", ErrUnsupported)
	}
	if sniffed != "" && mime != sniffed && !compatibleMIME(mime, sniffed) {
		return "", "", fmt.Errorf("%w: declared %s but content is %s", ErrKind, mime, sniffed)
	}
	// Content wins over a generic declaration: a docx declared as a plain
	// zip (or octet-stream-ish upload paths that say zip) is a document.
	if mime == "application/zip" && isOOXML(sniffed) {
		mime = sniffed
	}
	kind = strings.ToLower(strings.TrimSpace(declaredKind))
	inferred := KindFromMIME(mime)
	if kind == "" {
		kind = inferred
	}
	if kind == "" {
		return "", "", fmt.Errorf("%w: %s", ErrUnsupported, mime)
	}
	if !ValidKind(kind) {
		return "", "", fmt.Errorf("%w: %s", ErrUnsupported, kind)
	}
	if inferred != "" && kind != inferred && kind != KindBuild {
		return "", "", fmt.Errorf("%w: declared %s but content is %s", ErrKind, kind, inferred)
	}
	return kind, mime, nil
}

// SelectForProvider reports whether kind/mime may be sent to a provider.
// Unsupported combinations return a visible error (no silent drop).
func SelectForProvider(kind, mime string, caps Capabilities) error {
	if !caps.Images {
		return fmt.Errorf("%w: selected model does not support image attachments", ErrUnsupported)
	}
	mime = normalizeMIME(mime)
	if kind == "" {
		kind = KindFromMIME(mime)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != KindImage {
		if kind == "" {
			kind = mime
		}
		return fmt.Errorf("%w: %s cannot be sent as a provider image", ErrUnsupported, kind)
	}
	allowed := caps.ImageMIME
	if len(allowed) == 0 {
		allowed = ProviderImageMIME
	}
	for _, a := range allowed {
		if normalizeMIME(a) == mime {
			return nil
		}
	}
	return fmt.Errorf("%w: unsupported image format (%s)", ErrUnsupported, mime)
}

func normalizeMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	switch mime {
	case "image/jpg":
		return "image/jpeg"
	case "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "audio/wav"
	case "audio/mp3":
		return "audio/mpeg"
	}
	return mime
}

// compatibleMIME allows a declared type that differs from the sniffed one
// only where the sniff cannot tell them apart: OOXML is a zip, so a zip
// sniff (markers not found) still admits a declared OOXML type, and an
// OOXML sniff admits a declared plain zip. WebM and MP4 containers carry
// audio-only files too (browser voice notes record audio/webm), so an
// audio declaration on those containers is accepted.
func compatibleMIME(declared, sniffed string) bool {
	declared, sniffed = normalizeMIME(declared), normalizeMIME(sniffed)
	switch {
	case declared == sniffed:
		return true
	case isOOXML(declared) && sniffed == "application/zip":
		return true
	case declared == "application/zip" && isOOXML(sniffed):
		return true
	case declared == "audio/webm" && sniffed == "video/webm":
		return true
	case declared == "audio/mp4" && sniffed == "video/mp4":
		return true
	case declared == "video/mp4" && sniffed == "audio/mp4":
		return true
	default:
		return false
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
