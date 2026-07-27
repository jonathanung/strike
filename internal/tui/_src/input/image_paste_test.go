package tui

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Minimal valid 1x1 PNG.
var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
	0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00,
	0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestParseImagePasteDataURI(t *testing.T) {
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	att, notice, ok := parseImagePaste(uri)
	if !ok || notice != "" {
		t.Fatalf("parseImagePaste data URI: ok=%v notice=%q", ok, notice)
	}
	if att.MIME != "image/png" {
		t.Errorf("mime = %q", att.MIME)
	}
	raw, err := base64.StdEncoding.DecodeString(att.Data)
	if err != nil || len(raw) != len(tinyPNG) {
		t.Fatalf("decode: %v len=%d", err, len(raw))
	}
}

func TestParseImagePasteRawBytes(t *testing.T) {
	att, notice, ok := parseImagePaste(string(tinyPNG))
	if !ok || notice != "" {
		t.Fatalf("parse raw: ok=%v notice=%q", ok, notice)
	}
	if att.MIME != "image/png" {
		t.Errorf("mime = %q", att.MIME)
	}
}

func TestParseImagePasteFilePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	att, notice, ok := parseImagePaste(path)
	if !ok || notice != "" {
		t.Fatalf("parse path: ok=%v notice=%q", ok, notice)
	}
	if att.MIME != "image/png" {
		t.Errorf("mime = %q", att.MIME)
	}
}

func TestParseImagePasteTooLarge(t *testing.T) {
	// Build a PNG-magic blob larger than the cap without a real encoder.
	raw := make([]byte, maxImageBytes+1)
	copy(raw, tinyPNG)
	_, notice, ok := parseImagePaste(string(raw))
	if !ok {
		t.Fatal("expected image candidate")
	}
	if !strings.Contains(notice, "too large") {
		t.Fatalf("notice = %q, want too large", notice)
	}
}

func TestParseImagePasteNonImage(t *testing.T) {
	_, _, ok := parseImagePaste("hello world")
	if ok {
		t.Fatal("plain text should not be image paste")
	}
}

func TestComposerImagePasteChipAndSend(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "openai"
	m.modelName = "gpt-test"
	m.modelAttachment = true
	m.modelAttachmentKnown = true

	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(uri), Paste: true})
	if !strings.Contains(m.composer.Value(), "[image 1]") {
		t.Fatalf("composer = %q, want [image 1] chip", m.composer.Value())
	}
	if len(m.pendingImages) != 1 {
		t.Fatalf("pendingImages = %d, want 1", len(m.pendingImages))
	}

	m = typeAppText(t, m, " see this")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	_ = runAllAppCmds(t, cmd)

	op := receiveAppOp(t, ops)
	in, ok := op.(protocol.UserInput)
	if !ok {
		t.Fatalf("op type %T", op)
	}
	if !strings.Contains(in.Text, "see this") {
		t.Errorf("text = %q", in.Text)
	}
	if len(in.Images) != 1 || in.Images[0].MIME != "image/png" || in.Images[0].Data == "" {
		t.Fatalf("images = %#v", in.Images)
	}
	if m.composer.Value() != "" || len(m.pendingImages) != 0 {
		t.Fatalf("composer not cleared: value=%q images=%d", m.composer.Value(), len(m.pendingImages))
	}
}

func TestComposerCtrlVAttachesClipboardImage(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "openai"
	m.modelName = "gpt-test"
	m.modelAttachment = true
	m.modelAttachmentKnown = true
	m.clipboardImage = func() ([]byte, error) { return tinyPNG, nil }

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := m.composer.Value(); got != "[image 1]" {
		t.Fatalf("composer = %q, want image chip", got)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	_ = runAllAppCmds(t, cmd)

	op := receiveAppOp(t, ops)
	in, ok := op.(protocol.UserInput)
	if !ok {
		t.Fatalf("op type %T", op)
	}
	if len(in.Images) != 1 || in.Images[0].MIME != "image/png" {
		t.Fatalf("images = %#v", in.Images)
	}
	if got, err := base64.StdEncoding.DecodeString(in.Images[0].Data); err != nil || !bytes.Equal(got, tinyPNG) {
		t.Fatalf("image data = %x, err = %v", got, err)
	}
	if m.composer.Value() != "" || len(m.pendingImages) != 0 {
		t.Fatalf("composer not cleared: value=%q images=%d", m.composer.Value(), len(m.pendingImages))
	}
}

func TestComposerCtrlVWithoutImageKeepsDraft(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setComposerValueAt("keep me", len("keep me"))
	m.clipboardImage = func() ([]byte, error) { return nil, nil }

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlV})
	if got := m.composer.Value(); got != "keep me" {
		t.Fatalf("composer = %q, want draft retained", got)
	}
	if len(m.pendingImages) != 0 {
		t.Fatalf("pendingImages = %#v, want none", m.pendingImages)
	}
	if !strings.Contains(m.notice, "supported image") || !m.noticeErr {
		t.Fatalf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestComposerImageUnsupportedKeepsDraft(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.modelAttachmentKnown = true
	m.modelAttachment = false

	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(uri), Paste: true})
	m = typeAppText(t, m, " keep me")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	_ = runAllAppCmds(t, cmd)

	assertNoAppOp(t, ops)
	if !strings.Contains(m.composer.Value(), "keep me") {
		t.Fatalf("composer lost text: %q", m.composer.Value())
	}
	if len(m.pendingImages) != 1 {
		t.Fatalf("pendingImages = %d, want kept", len(m.pendingImages))
	}
	if !m.noticeErr || !strings.Contains(m.notice, "does not support") {
		t.Fatalf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestUserMessageDisplayTextNoBinary(t *testing.T) {
	got := userMessageDisplayText("look", []protocol.ImageAttachment{
		{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(tinyPNG)},
	})
	if strings.Contains(got, "iVBOR") || strings.Contains(got, string(tinyPNG[1:4])) {
		t.Fatalf("display leaked binary/base64: %q", got)
	}
	if !strings.Contains(got, "[image 1]") || !strings.Contains(got, "look") {
		t.Fatalf("display = %q", got)
	}
}

func TestUserMessageCellNoBinary(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UserMessage{
		Text: "caption",
		Images: []protocol.ImageAttachment{
			{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(tinyPNG)},
		},
	})
	if len(m.cells) != 1 {
		t.Fatalf("cells = %d", len(m.cells))
	}
	uc, ok := m.cells[0].(*userCell)
	if !ok {
		t.Fatalf("cell %T", m.cells[0])
	}
	if strings.Contains(uc.text, base64.StdEncoding.EncodeToString(tinyPNG)[:16]) {
		t.Fatalf("cell text has base64: %q", uc.text)
	}
	if !strings.Contains(uc.text, "[image 1]") {
		t.Fatalf("cell text = %q", uc.text)
	}
}
