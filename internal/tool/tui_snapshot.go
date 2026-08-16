package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// Headless TUI frame snapshot bounds (#1183). Keep in lockstep with
// internal/tui SnapshotFrame caps.
const (
	MaxTUISnapshotBytes = 32 << 10
	MaxTUISnapshotLines = 80
)

const errNoTUIFrame = "no TUI frame available (headless/non-TUI session)"

// TUISnapshotRequest is the model-facing capture request.
type TUISnapshotRequest struct {
	// IncludeImage asks for an optional addressable image ref (never the
	// payload). Text is always returned and does not require multimodal.
	IncludeImage bool `json:"include_image,omitempty"`
}

// TUISnapshotResult is a bounded, redacted frame dump.
type TUISnapshotResult struct {
	Text             string `json:"text"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	Truncated        bool   `json:"truncated,omitempty"`
	Redacted         bool   `json:"redacted,omitempty"`
	ImageRef         string `json:"image_ref,omitempty"`
	ImageUnavailable bool   `json:"image_unavailable,omitempty"`
}

// TUIFrameStore is a concurrency-safe last-frame buffer. The TUI publishes
// via Put from the paint path; tui_snapshot reads via Capture.
type TUIFrameStore struct {
	mu     sync.RWMutex
	raw    string
	width  int
	height int
	ok     bool
	// RenderImage, when set, turns the stripped frame into an addressable
	// image ref (path or content-address). Nil means image output is skipped.
	RenderImage func(text string, width, height int) (ref string, err error)
}

// Put records the latest painted frame. Empty frames are ignored.
func (s *TUIFrameStore) Put(raw string, width, height int) {
	if s == nil || strings.TrimSpace(raw) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = raw
	s.width = width
	s.height = height
	s.ok = true
}

// Capture returns a bounded, redacted snapshot of the last frame.
func (s *TUIFrameStore) Capture(ctx context.Context, req TUISnapshotRequest) (TUISnapshotResult, error) {
	if err := ctx.Err(); err != nil {
		return TUISnapshotResult{}, err
	}
	if s == nil {
		return TUISnapshotResult{}, ErrPrecondition(errNoTUIFrame)
	}
	s.mu.RLock()
	raw, w, h, ok := s.raw, s.width, s.height, s.ok
	render := s.RenderImage
	s.mu.RUnlock()
	if !ok {
		return TUISnapshotResult{}, ErrPrecondition(errNoTUIFrame)
	}
	out := NormalizeTUIFrame(raw, w, h)
	if req.IncludeImage {
		if render == nil {
			out.ImageUnavailable = true
		} else {
			ref, err := render(out.Text, out.Width, out.Height)
			if err != nil || strings.TrimSpace(ref) == "" {
				out.ImageUnavailable = true
			} else {
				out.ImageRef = strings.TrimSpace(ref)
			}
		}
	}
	return out, nil
}

// NormalizeTUIFrame strips ANSI, redacts secrets, and applies size bounds.
func NormalizeTUIFrame(raw string, width, height int) TUISnapshotResult {
	plain := ansi.Strip(raw)
	redacted := redact.String(plain)
	text, truncated := boundTUIFrameText(redacted)
	return TUISnapshotResult{
		Text:      text,
		Width:     width,
		Height:    height,
		Truncated: truncated,
		Redacted:  redacted != plain,
	}
}

func boundTUIFrameText(s string) (string, bool) {
	truncated := false
	if len(s) > MaxTUISnapshotBytes {
		cut := s[:MaxTUISnapshotBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > MaxTUISnapshotBytes/2 {
			cut = cut[:i]
		}
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		s = cut
		truncated = true
	}
	lines := strings.Split(s, "\n")
	if len(lines) > MaxTUISnapshotLines {
		s = strings.Join(lines[:MaxTUISnapshotLines], "\n")
		truncated = true
	}
	if truncated {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "... (truncated)"
	}
	return s, truncated
}

type tuiSnapshotTool struct{}

// NewTUISnapshot returns the headless TUI frame capture tool.
func NewTUISnapshot() Tool { return tuiSnapshotTool{} }

func (tuiSnapshotTool) Name() string { return "tui_snapshot" }

func (tuiSnapshotTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencySafeRetry)
}

func (tuiSnapshotTool) Description() string {
	return `Capture the current TUI frame as bounded, redacted text.

- Returns an ANSI-stripped dump of the last painted Bubble Tea frame.
- Text works without multimodal. Optional include_image asks for an
  addressable image ref (never embeds the image payload).
- Fails when no frame is available (headless/non-TUI session).
- Output is size-bounded and redacted like other session exports.`
}

func (tuiSnapshotTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"include_image": {
				"type": "boolean",
				"description": "Request an optional addressable image ref when the host can render one. Text is always returned."
			}
		},
		"additionalProperties": false
	}`)
}

func (tuiSnapshotTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var req TUISnapshotRequest
	if len(args) > 0 && string(args) != "null" && string(args) != "{}" {
		if err := json.Unmarshal(args, &req); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "tui_snapshot", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TUISnapshot == nil {
		return Result{}, ErrPrecondition(errNoTUIFrame)
	}
	res, err := tc.TUISnapshot(ctx, req)
	if err != nil {
		return Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return Result{}, err
	}
	title := "tui_snapshot"
	if res.Truncated {
		title += " truncated"
	}
	if res.ImageRef != "" {
		title += " +image"
	}
	return Result{Title: title, Output: string(out)}, nil
}
