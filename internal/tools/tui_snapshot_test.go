package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"testing"
)

func TestTUISnapshotNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "no TUI frame available") {
		t.Fatalf("err = %v, want no-frame", err)
	}
}

func TestTUISnapshotEmptyStore(t *testing.T) {
	store := &tool.TUIFrameStore{}
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = store.Capture
	_, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "no TUI frame available") {
		t.Fatalf("err = %v, want no-frame", err)
	}
}

func TestTUISnapshotSuccess(t *testing.T) {
	store := &tool.TUIFrameStore{}
	store.Put("hello \x1b[31mfixture\x1b[0m frame", 80, 24)
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = store.Capture
	res, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var parsed tool.TUISnapshotResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Text != "hello fixture frame" {
		t.Fatalf("text = %q", parsed.Text)
	}
	if parsed.Width != 80 || parsed.Height != 24 {
		t.Fatalf("size = %dx%d", parsed.Width, parsed.Height)
	}
	if parsed.Truncated || parsed.ImageRef != "" || parsed.ImageUnavailable {
		t.Fatalf("unexpected flags: %+v", parsed)
	}
	if res.Title != "tui_snapshot" {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestTUISnapshotRedactsAndBounds(t *testing.T) {
	secret := "sk-ant-" + strings.Repeat("b", 16)
	var b strings.Builder
	for i := 0; i < tool.MaxTUISnapshotLines+10; i++ {
		b.WriteString("line token ")
		b.WriteString(secret)
		b.WriteByte('\n')
	}
	got := tool.NormalizeTUIFrame(b.String(), 40, 100)
	if !got.Truncated {
		t.Fatal("expected truncation")
	}
	if strings.Contains(got.Text, secret) {
		t.Fatal("secret leaked")
	}
	if !got.Redacted {
		t.Fatal("expected Redacted")
	}
	if !strings.Contains(got.Text, "... (truncated)") {
		t.Fatalf("missing trunc marker: %q", got.Text)
	}
}

func TestTUISnapshotIncludeImageUnavailable(t *testing.T) {
	store := &tool.TUIFrameStore{}
	store.Put("plain frame", 40, 10)
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = store.Capture
	res, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{
		"include_image": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var parsed tool.TUISnapshotResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Text != "plain frame" {
		t.Fatalf("text = %q", parsed.Text)
	}
	if !parsed.ImageUnavailable {
		t.Fatal("expected image_unavailable")
	}
	if parsed.ImageRef != "" {
		t.Fatalf("image_ref should be empty, got %q", parsed.ImageRef)
	}
	if strings.Contains(res.Output, "iVBORw") || strings.Contains(res.Output, "\\u0000") {
		t.Fatalf("embedded image payload: %s", res.Output)
	}
}

func TestTUISnapshotIncludeImageRef(t *testing.T) {
	store := &tool.TUIFrameStore{
		RenderImage: func(text string, width, height int) (string, error) {
			if text == "" || width == 0 {
				t.Fatal("renderer missing frame")
			}
			return "blob:sha256:abc", nil
		},
	}
	store.Put("plain frame", 40, 10)
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = store.Capture
	res, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{
		"include_image": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	var parsed tool.TUISnapshotResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ImageRef != "blob:sha256:abc" || parsed.ImageUnavailable {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !strings.Contains(res.Title, "+image") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestTUISnapshotPermissionDenied(t *testing.T) {
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask: func(context.Context, tool.AskRequest) error {
			return errors.New("denied")
		},
		TUISnapshot: func(context.Context, tool.TUISnapshotRequest) (tool.TUISnapshotResult, error) {
			t.Fatal("should not run")
			return tool.TUISnapshotResult{}, nil
		},
	}
	if _, err := NewTUISnapshot().Execute(context.Background(), mustJSON(t, map[string]any{}), tc); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestTUISnapshotInvalidJSON(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = func(context.Context, tool.TUISnapshotRequest) (tool.TUISnapshotResult, error) {
		return tool.TUISnapshotResult{Text: "x"}, nil
	}
	if _, err := NewTUISnapshot().Execute(context.Background(), json.RawMessage(`{`), tc); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestTUISnapshotEmptyArgsOK(t *testing.T) {
	store := &tool.TUIFrameStore{}
	store.Put("ok", 10, 5)
	tc := allowAll(t.TempDir())
	tc.TUISnapshot = store.Capture
	for _, args := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		if _, err := NewTUISnapshot().Execute(context.Background(), args, tc); err != nil {
			t.Fatalf("args %s: %v", args, err)
		}
	}
}

func TestTUISnapshotContract(t *testing.T) {
	c := tool.LookupContract(NewTUISnapshot())
	if c.SideEffect != tool.SideEffectNone || c.Idempotency != tool.IdempotencySafeRetry {
		t.Fatalf("contract = %+v", c)
	}
	if !tool.IsDeferredTool("tui_snapshot") || tool.IsCoreTool("tui_snapshot") {
		t.Fatal("tui_snapshot should be deferred non-core")
	}
}
